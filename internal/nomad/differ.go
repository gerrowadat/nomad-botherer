// Package nomad compares HCL job definitions against a live Nomad cluster and
// reports any diffs it finds.
package nomad

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/gerrowadat/nomad-botherer/internal/config"
)

// jobBlockRe matches a top-level Nomad job stanza in HCL.
// Files without this pattern are silently skipped (e.g. ACL policies, volumes, namespaces).
var jobBlockRe = regexp.MustCompile(`(?m)^\s*job\s+"`)

// DiffType describes the relationship between a job in HCL and in Nomad.
type DiffType string

const (
	// DiffTypeModified means the job exists in both HCL and Nomad but the
	// definitions differ (Nomad plan shows changes).
	DiffTypeModified DiffType = "modified"

	// DiffTypeMissingFromNomad means the job is defined in HCL but not
	// currently registered in Nomad.
	DiffTypeMissingFromNomad DiffType = "missing_from_nomad"

	// DiffTypeMissingFromHCL means the job is running in Nomad but there is
	// no corresponding HCL file in the repo.
	DiffTypeMissingFromHCL DiffType = "missing_from_hcl"
)

// JobDiff describes a single divergence between the git repo and Nomad.
type JobDiff struct {
	JobID    string   `json:"job_id"`
	HCLFile  string   `json:"hcl_file,omitempty"` // empty for MissingFromHCL
	DiffType DiffType `json:"diff_type"`
	Detail   string   `json:"detail"`

	// PlanDiff holds the structured diff from the Nomad plan API.
	// Only populated for DiffTypeModified entries.
	PlanDiff *nomadapi.JobDiff `json:"-"`
}

// NomadJobsClient is the subset of the Nomad API jobs client we use.
// The concrete *nomadapi.Jobs satisfies this interface; tests inject a mock.
type NomadJobsClient interface {
	ParseHCL(jobHCL string, canonicalize bool) (*nomadapi.Job, error)
	Plan(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error)
	Info(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error)
	List(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error)
}

// Differ runs periodic diff checks and stores the latest results.
type Differ struct {
	jobs            NomadJobsClient
	namespace       string
	includeDeadJobs bool

	mu             sync.RWMutex
	diffs          []JobDiff
	lastCheckTime  time.Time
	lastCommit     string
	driftFirstSeen map[string]time.Time // key: driftKey(jobID, diffType); protected by mu

	hclParseErrors  prometheus.Counter
	hclFilesSkipped prometheus.Counter
	diffChecks      prometheus.Counter
	nomadAPIErrors  *prometheus.CounterVec
	lastCheck       prometheus.Gauge
	jobDiffs        *prometheus.GaugeVec
	driftedJobs     *prometheus.GaugeVec
	jobDriftSince   *prometheus.GaugeVec
}

// newDifferBase constructs a Differ with metrics registered into reg.
func newDifferBase(jobs NomadJobsClient, namespace string, includeDeadJobs bool, reg prometheus.Registerer) *Differ {
	f := promauto.With(reg)
	return &Differ{
		jobs:            jobs,
		namespace:       namespace,
		includeDeadJobs: includeDeadJobs,
		driftFirstSeen:  make(map[string]time.Time),
		hclParseErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "nomad_botherer_hcl_parse_errors_total",
			Help: "Total number of HCL files that failed to parse as Nomad job definitions.",
		}),
		hclFilesSkipped: f.NewCounter(prometheus.CounterOpts{
			Name: "nomad_botherer_hcl_non_job_files_skipped_total",
			Help: "Total number of HCL files skipped because they lack a top-level job stanza (e.g. ACL policies, volumes).",
		}),
		diffChecks: f.NewCounter(prometheus.CounterOpts{
			Name: "nomad_botherer_diff_checks_total",
			Help: "Total number of diff checks run against the Nomad cluster.",
		}),
		nomadAPIErrors: f.NewCounterVec(prometheus.CounterOpts{
			Name: "nomad_botherer_nomad_api_errors_total",
			Help: "Total number of Nomad API errors by operation.",
		}, []string{"op"}),
		lastCheck: f.NewGauge(prometheus.GaugeOpts{
			Name: "nomad_botherer_last_check_timestamp_seconds",
			Help: "Unix timestamp of the most recent diff check.",
		}),
		jobDiffs: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nomad_botherer_job_diffs",
			Help: "1 for each job/diff-type combination currently detected.",
		}, []string{"job", "diff_type"}),
		driftedJobs: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nomad_botherer_drifted_jobs",
			Help: "Number of jobs currently in each drift state.",
		}, []string{"diff_type"}),
		jobDriftSince: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nomad_botherer_job_drift_first_seen_timestamp_seconds",
			Help: "Unix timestamp when drift was first detected for each job. Cleared when drift resolves. Use time()-metric to get seconds in drift state.",
		}, []string{"job", "diff_type"}),
	}
}

// NewDiffer creates a Differ backed by a real Nomad API client.
func NewDiffer(cfg *config.Config) (*Differ, error) {
	nomadCfg := nomadapi.DefaultConfig()
	nomadCfg.Address = cfg.NomadAddr
	if cfg.NomadToken != "" {
		nomadCfg.SecretID = cfg.NomadToken
	}

	client, err := nomadapi.NewClient(nomadCfg)
	if err != nil {
		return nil, fmt.Errorf("creating nomad client: %w", err)
	}

	return newDifferBase(client.Jobs(), cfg.NomadNamespace, cfg.IncludeDeadJobs, prometheus.DefaultRegisterer), nil
}

// NewWithClient creates a Differ with a custom jobs client, intended for tests.
func NewWithClient(cfg *config.Config, jobs NomadJobsClient) *Differ {
	return newDifferBase(jobs, cfg.NomadNamespace, cfg.IncludeDeadJobs, prometheus.NewRegistry())
}

// NewWithClientAndRegistry creates a Differ with a custom jobs client and Prometheus
// registry. Use this in tests that need to inspect metric values.
func NewWithClientAndRegistry(cfg *config.Config, jobs NomadJobsClient, reg prometheus.Registerer) *Differ {
	return newDifferBase(jobs, cfg.NomadNamespace, cfg.IncludeDeadJobs, reg)
}

// Check compares the given HCL files (path → content) against the live Nomad
// cluster and stores the results. commit is recorded for informational purposes.
func (d *Differ) Check(hclFiles map[string]string, commit string) error {
	slog.Info("Running diff check", "commit", commit, "hcl_files", len(hclFiles))
	d.diffChecks.Inc()

	q := &nomadapi.QueryOptions{Namespace: d.namespace}
	wq := &nomadapi.WriteOptions{Namespace: d.namespace}

	// Parse all HCL files via the Nomad API.
	hclJobs := make(map[string]*nomadapi.Job) // jobID → parsed job
	hclJobFile := make(map[string]string)      // jobID → source HCL file path

	for filename, content := range hclFiles {
		if !jobBlockRe.MatchString(content) {
			slog.Debug("Skipping HCL file with no job stanza", "file", filename)
			d.hclFilesSkipped.Inc()
			continue
		}

		job, err := d.jobs.ParseHCL(content, true)
		if err != nil {
			slog.Warn("Failed to parse HCL file, skipping", "file", filename, "err", err)
			d.hclParseErrors.Inc()
			continue
		}
		if job == nil || job.ID == nil || *job.ID == "" {
			slog.Warn("HCL file yielded no job ID, skipping", "file", filename)
			continue
		}
		jobID := *job.ID
		hclJobs[jobID] = job
		hclJobFile[jobID] = filename
		slog.Debug("Parsed HCL file", "file", filename, "job_id", jobID)
	}

	var diffs []JobDiff

	// For each job defined in HCL, check Nomad.
	for jobID, job := range hclJobs {
		filename := hclJobFile[jobID]

		nomadJob, _, err := d.jobs.Info(jobID, q)
		if err != nil {
			if isNotFound(err) {
				diffs = append(diffs, JobDiff{
					JobID:    jobID,
					HCLFile:  filename,
					DiffType: DiffTypeMissingFromNomad,
					Detail:   "job is defined in HCL but not registered in Nomad",
				})
				continue
			}
			d.nomadAPIErrors.WithLabelValues("info").Inc()
			slog.Warn("Failed to query job from Nomad", "job", jobID, "err", err)
			continue
		}

		// Unless the caller explicitly wants dead jobs included, treat a dead
		// job the same as a missing one.
		if !d.includeDeadJobs && nomadJob != nil && nomadJob.Status != nil && *nomadJob.Status == "dead" {
			slog.Debug("Job is dead in Nomad, treating as missing", "job", jobID)
			diffs = append(diffs, JobDiff{
				JobID:    jobID,
				HCLFile:  filename,
				DiffType: DiffTypeMissingFromNomad,
				Detail:   "job is defined in HCL but is in 'dead' state in Nomad",
			})
			continue
		}

		// Job exists and is live — run a plan to detect config drift.
		plan, _, err := d.jobs.Plan(job, true, wq)
		if err != nil {
			d.nomadAPIErrors.WithLabelValues("plan").Inc()
			slog.Warn("Failed to plan job", "job", jobID, "err", err)
			continue
		}

		if plan.Diff != nil && plan.Diff.Type != "" && plan.Diff.Type != "None" {
			diffs = append(diffs, JobDiff{
				JobID:    jobID,
				HCLFile:  filename,
				DiffType: DiffTypeModified,
				Detail:   fmt.Sprintf("Nomad plan shows diff type %q", plan.Diff.Type),
				PlanDiff: plan.Diff,
			})
		}
	}

	// Find jobs in Nomad that have no corresponding HCL file.
	// Dead jobs are skipped unless --include-dead-jobs is set, since a dead
	// job without HCL is expected (it was stopped intentionally).
	allJobs, _, err := d.jobs.List(q)
	if err != nil {
		d.nomadAPIErrors.WithLabelValues("list").Inc()
		slog.Warn("Failed to list Nomad jobs", "err", err)
	} else {
		for _, j := range allJobs {
			if !d.includeDeadJobs && j.Status == "dead" {
				continue
			}
			if _, ok := hclJobs[j.ID]; !ok {
				diffs = append(diffs, JobDiff{
					JobID:    j.ID,
					DiffType: DiffTypeMissingFromHCL,
					Detail:   fmt.Sprintf("job is running in Nomad (status: %s) but has no HCL definition in the repo", j.Status),
				})
			}
		}
	}

	now := time.Now()

	// Build the set of currently-drifting job+type keys.
	currentKeys := make(map[string]struct{}, len(diffs))
	for _, diff := range diffs {
		currentKeys[driftKey(diff.JobID, string(diff.DiffType))] = struct{}{}
	}

	d.mu.Lock()
	d.diffs = diffs
	d.lastCheckTime = now
	d.lastCommit = commit

	// Remove entries that are no longer drifting.
	for k := range d.driftFirstSeen {
		if _, ok := currentKeys[k]; !ok {
			delete(d.driftFirstSeen, k)
		}
	}
	// Record the first time each new drift is observed.
	for k := range currentKeys {
		if _, ok := d.driftFirstSeen[k]; !ok {
			d.driftFirstSeen[k] = now
		}
	}
	// Snapshot first-seen times for metric updates below (outside the lock).
	firstSeenSnapshot := make(map[string]time.Time, len(d.driftFirstSeen))
	for k, v := range d.driftFirstSeen {
		firstSeenSnapshot[k] = v
	}
	d.mu.Unlock()

	d.lastCheck.Set(float64(now.Unix()))
	d.jobDiffs.Reset()
	d.driftedJobs.Reset()
	d.jobDriftSince.Reset()
	typeCounts := make(map[string]int)
	for _, diff := range diffs {
		d.jobDiffs.WithLabelValues(diff.JobID, string(diff.DiffType)).Set(1)
		typeCounts[string(diff.DiffType)]++
	}
	for typ, count := range typeCounts {
		d.driftedJobs.WithLabelValues(typ).Set(float64(count))
	}
	for _, diff := range diffs {
		k := driftKey(diff.JobID, string(diff.DiffType))
		if t, ok := firstSeenSnapshot[k]; ok {
			d.jobDriftSince.WithLabelValues(diff.JobID, string(diff.DiffType)).Set(float64(t.Unix()))
		}
	}

	slog.Info("Diff check complete", "diffs", len(diffs), "commit", commit)
	return nil
}

// driftKey returns a map key for a (jobID, diffType) pair.
func driftKey(jobID, diffType string) string {
	return jobID + "\x00" + diffType
}

// Diffs returns a snapshot of the latest diffs, the time they were computed,
// and the git commit they were computed against.
func (d *Differ) Diffs() ([]JobDiff, time.Time, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]JobDiff, len(d.diffs))
	copy(result, d.diffs)
	return result, d.lastCheckTime, d.lastCommit
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "404") || strings.Contains(strings.ToLower(s), "not found")
}
