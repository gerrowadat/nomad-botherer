// Package nomad compares HCL job definitions against a live Nomad cluster and
// reports any diffs it finds.
package nomad

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"github.com/gerrowadat/nomad-botherer/internal/config"
)

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
	jobs      NomadJobsClient
	namespace string

	mu         sync.RWMutex
	diffs      []JobDiff
	lastCheck  time.Time
	lastCommit string
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

	return &Differ{
		jobs:      client.Jobs(),
		namespace: cfg.NomadNamespace,
	}, nil
}

// NewWithClient creates a Differ with a custom jobs client, intended for tests.
func NewWithClient(cfg *config.Config, jobs NomadJobsClient) *Differ {
	return &Differ{
		jobs:      jobs,
		namespace: cfg.NomadNamespace,
	}
}

// Check compares the given HCL files (path → content) against the live Nomad
// cluster and stores the results. commit is recorded for informational purposes.
func (d *Differ) Check(hclFiles map[string]string, commit string) error {
	slog.Info("Running diff check", "commit", commit, "hcl_files", len(hclFiles))

	q := &nomadapi.QueryOptions{Namespace: d.namespace}
	wq := &nomadapi.WriteOptions{Namespace: d.namespace}

	// Parse all HCL files via the Nomad API.
	hclJobs := make(map[string]*nomadapi.Job) // jobID → parsed job
	hclJobFile := make(map[string]string)      // jobID → source HCL file path

	for filename, content := range hclFiles {
		job, err := d.jobs.ParseHCL(content, true)
		if err != nil {
			slog.Warn("Failed to parse HCL file, skipping", "file", filename, "err", err)
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

		_, _, err := d.jobs.Info(jobID, q)
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
			slog.Warn("Failed to query job from Nomad", "job", jobID, "err", err)
			continue
		}

		// Job exists — run a plan to detect config drift.
		plan, _, err := d.jobs.Plan(job, true, wq)
		if err != nil {
			slog.Warn("Failed to plan job", "job", jobID, "err", err)
			continue
		}

		if plan.Diff != nil && plan.Diff.Type != "" && plan.Diff.Type != "None" {
			diffs = append(diffs, JobDiff{
				JobID:    jobID,
				HCLFile:  filename,
				DiffType: DiffTypeModified,
				Detail:   fmt.Sprintf("Nomad plan shows diff type %q", plan.Diff.Type),
			})
		}
	}

	// Find jobs running in Nomad that have no corresponding HCL file.
	allJobs, _, err := d.jobs.List(q)
	if err != nil {
		slog.Warn("Failed to list Nomad jobs", "err", err)
	} else {
		for _, j := range allJobs {
			if _, ok := hclJobs[j.ID]; !ok {
				diffs = append(diffs, JobDiff{
					JobID:    j.ID,
					DiffType: DiffTypeMissingFromHCL,
					Detail:   fmt.Sprintf("job is running in Nomad (status: %s) but has no HCL definition in the repo", j.Status),
				})
			}
		}
	}

	d.mu.Lock()
	d.diffs = diffs
	d.lastCheck = time.Now()
	d.lastCommit = commit
	d.mu.Unlock()

	slog.Info("Diff check complete", "diffs", len(diffs), "commit", commit)
	return nil
}

// Diffs returns a snapshot of the latest diffs, the time they were computed,
// and the git commit they were computed against.
func (d *Differ) Diffs() ([]JobDiff, time.Time, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]JobDiff, len(d.diffs))
	copy(result, d.diffs)
	return result, d.lastCheck, d.lastCommit
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "404") || strings.Contains(strings.ToLower(s), "not found")
}
