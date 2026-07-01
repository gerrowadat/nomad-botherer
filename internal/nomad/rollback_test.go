package nomad_test

import (
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/gerrowadat/nomad-gitops/internal/config"
	"github.com/gerrowadat/nomad-gitops/internal/nomad"
)

func intPtr(i int) *int { return &i }

// --- pure helpers -----------------------------------------------------------

func TestSpecFingerprint_IgnoresVolatileAndManagedMeta(t *testing.T) {
	base := &nomadapi.Job{
		ID:   strPtr("web"),
		Meta: map[string]string{"gitops_managed": "true", "gitops_update_policy": "full", "team": "infra"},
		TaskGroups: []*nomadapi.TaskGroup{
			{Name: strPtr("g"), Count: intPtr(2)},
		},
	}
	// Same spec, but with Nomad-injected version bookkeeping and different
	// managed-meta keys: must fingerprint identically.
	withBookkeeping := &nomadapi.Job{
		ID:             strPtr("web"),
		Version:        uint64Ptr(9),
		Stable:         boolPtr(false),
		JobModifyIndex: uint64Ptr(123),
		Namespace:      strPtr("default"),
		Meta:           map[string]string{"gitops_managed": "false", "team": "infra"},
		TaskGroups: []*nomadapi.TaskGroup{
			{Name: strPtr("g"), Count: intPtr(2)},
		},
	}

	a, err := nomad.SpecFingerprint(base, "gitops")
	if err != nil {
		t.Fatalf("fingerprint base: %v", err)
	}
	b, err := nomad.SpecFingerprint(withBookkeeping, "gitops")
	if err != nil {
		t.Fatalf("fingerprint withBookkeeping: %v", err)
	}
	if a != b {
		t.Errorf("volatile fields and managed meta must not affect the fingerprint:\n a=%s\n b=%s", a, b)
	}

	// A real spec change (non-managed meta) must change the fingerprint.
	changed := *base
	changed.Meta = map[string]string{"gitops_managed": "true", "team": "platform"}
	c, _ := nomad.SpecFingerprint(&changed, "gitops")
	if c == a {
		t.Error("a real spec change must change the fingerprint")
	}
}

func TestSpecFingerprint_IgnoresAutoscaledCount(t *testing.T) {
	mk := func(count int) *nomadapi.Job {
		return &nomadapi.Job{
			ID: strPtr("web"),
			TaskGroups: []*nomadapi.TaskGroup{{
				Name:    strPtr("g"),
				Count:   intPtr(count),
				Scaling: &nomadapi.ScalingPolicy{Enabled: boolPtr(true)},
			}},
		}
	}
	a, _ := nomad.SpecFingerprint(mk(2), "gitops")
	b, _ := nomad.SpecFingerprint(mk(9), "gitops")
	if a != b {
		t.Errorf("autoscaler-owned Count must not affect the fingerprint:\n a=%s\n b=%s", a, b)
	}
}

func TestLastStableVersion(t *testing.T) {
	versions := []*nomadapi.Job{
		{Version: uint64Ptr(5), Stable: boolPtr(false)},
		{Version: uint64Ptr(4), Stable: boolPtr(true)},
		{Version: uint64Ptr(2), Stable: boolPtr(true)},
		{Version: uint64Ptr(6), Stable: boolPtr(true)}, // above failed, ignored
	}
	got, ok := nomad.LastStableVersion(versions, 5)
	if !ok || got != 4 {
		t.Errorf("want stable version 4 below 5, got %d (ok=%v)", got, ok)
	}

	none := []*nomadapi.Job{{Version: uint64Ptr(5), Stable: boolPtr(false)}}
	if _, ok := nomad.LastStableVersion(none, 5); ok {
		t.Error("expected no stable version below 5")
	}
}

func TestJobHasAutoRevert(t *testing.T) {
	if nomad.JobHasAutoRevert(&nomadapi.Job{ID: strPtr("x")}) {
		t.Error("a job with no update stanza must not report auto_revert")
	}
	jobLevel := &nomadapi.Job{ID: strPtr("x"), Update: &nomadapi.UpdateStrategy{AutoRevert: boolPtr(true)}}
	if !nomad.JobHasAutoRevert(jobLevel) {
		t.Error("job-level auto_revert must be detected")
	}
	groupLevel := &nomadapi.Job{ID: strPtr("x"), TaskGroups: []*nomadapi.TaskGroup{
		{Name: strPtr("g"), Update: &nomadapi.UpdateStrategy{AutoRevert: boolPtr(true)}},
	}}
	if !nomad.JobHasAutoRevert(groupLevel) {
		t.Error("group-level auto_revert must be detected")
	}
}

// --- flap-loop guard --------------------------------------------------------

// flapMock builds a mock where test-job is live and drifted (planDiff), with a
// failed deployment at failedVersion whose stored version spec matches the HCL
// job (so the flap-guard fingerprints them equal). register calls are recorded.
func flapMock(meta map[string]string, planDiff *nomadapi.JobDiff, failedVersion uint64, calls *[]registerCall) *mockJobsClient {
	mock := defaultMock()
	mock.parseHCLFn = func(string, bool) (*nomadapi.Job, error) {
		return &nomadapi.Job{ID: strPtr("test-job"), Meta: meta}, nil
	}
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		return &nomadapi.Job{ID: strPtr(jobID), Status: strPtr("running"), JobModifyIndex: uint64Ptr(42)}, nil, nil
	}
	mock.planFn = func(j *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: planDiff}, nil, nil
	}
	mock.deploymentsFn = func(jobID string, all bool, q *nomadapi.QueryOptions) ([]*nomadapi.Deployment, *nomadapi.QueryMeta, error) {
		return []*nomadapi.Deployment{{JobVersion: failedVersion, Status: nomadapi.DeploymentStatusFailed}}, nil, nil
	}
	mock.versionsFn = func(jobID string, diffs bool, q *nomadapi.QueryOptions) ([]*nomadapi.Job, []*nomadapi.JobDiff, *nomadapi.QueryMeta, error) {
		return []*nomadapi.Job{{ID: strPtr("test-job"), Version: uint64Ptr(failedVersion), Meta: meta}}, nil, nil, nil
	}
	mock.registerFn = func(j *nomadapi.Job, opts *nomadapi.RegisterOptions, q *nomadapi.WriteOptions) (*nomadapi.JobRegisterResponse, *nomadapi.WriteMeta, error) {
		*calls = append(*calls, registerCall{job: j, opts: opts})
		return &nomadapi.JobRegisterResponse{EvalID: "e", JobModifyIndex: 43}, nil, nil
	}
	return mock
}

func flapCfg(mode string) *config.Config {
	c := applyCfg("full", false)
	c.FlapGuard = mode
	return c
}

func TestFlapGuard_BlocksKnownFailedSpec(t *testing.T) {
	var calls []registerCall
	mock := flapMock(nil, editedMixedDiff(), 7, &calls)
	d := nomad.NewWithClient(flapCfg("history"), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(calls) != 0 {
		t.Errorf("flap-guard must withhold the register, got %d calls", len(calls))
	}
	if updates := d.Updates(); len(updates) != 0 {
		t.Errorf("flap-guard must not enqueue an update, got %+v", updates)
	}
	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 || diffs[0].ApplyAction != nomad.ApplyActionKnownFailed {
		t.Fatalf("want one diff with apply_action blocked_known_failed, got %+v", diffs)
	}
	if got := testutil.ToFloat64(nomad.UpdatesBlockedKnownFailed(d).WithLabelValues("test-job")); got != 1 {
		t.Errorf("updates_blocked_known_failed_total: want 1, got %v", got)
	}
}

func TestFlapGuard_Off_DoesNotBlock(t *testing.T) {
	var calls []registerCall
	mock := flapMock(nil, editedMixedDiff(), 7, &calls)
	d := nomad.NewWithClient(flapCfg("off"), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(calls) != 1 {
		t.Errorf("with --flap-guard=off the known-failed spec should re-apply, got %d calls", len(calls))
	}
}

func TestFlapGuard_MetaOverrideOff(t *testing.T) {
	var calls []registerCall
	meta := map[string]string{"gitops_flap_guard": "off"}
	mock := flapMock(meta, editedMixedDiff(), 7, &calls)
	d := nomad.NewWithClient(flapCfg("history"), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(calls) != 1 {
		t.Errorf("gitops_flap_guard=off must disable the guard for this job, got %d calls", len(calls))
	}
}

func TestFlapGuard_NoFailedDeployment_Applies(t *testing.T) {
	var calls []registerCall
	mock := flapMock(nil, editedMixedDiff(), 7, &calls)
	// Override: the deployment at the matching version succeeded, not failed.
	mock.deploymentsFn = func(jobID string, all bool, q *nomadapi.QueryOptions) ([]*nomadapi.Deployment, *nomadapi.QueryMeta, error) {
		return []*nomadapi.Deployment{{JobVersion: 7, Status: nomadapi.DeploymentStatusSuccessful}}, nil, nil
	}
	d := nomad.NewWithClient(flapCfg("history"), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(calls) != 1 {
		t.Errorf("a spec with no failed deployment must apply normally, got %d calls", len(calls))
	}
}

func TestFlapGuard_TagMode_TagsFailedVersion(t *testing.T) {
	var calls []registerCall
	var tagged []string
	mock := flapMock(nil, editedMixedDiff(), 7, &calls)
	mock.tagVersionFn = func(jobID string, version uint64, name, description string, q *nomadapi.WriteOptions) (*nomadapi.WriteMeta, error) {
		tagged = append(tagged, name)
		return nil, nil
	}
	d := nomad.NewWithClient(flapCfg("tag"), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(calls) != 0 {
		t.Errorf("tag mode still blocks the known-failed spec, got %d register calls", len(calls))
	}
	if len(tagged) != 1 || !strings.HasPrefix(tagged[0], "gitops-failed-") {
		t.Fatalf("tag mode must tag the failed version with a gitops-failed- name, got %v", tagged)
	}
	if got := testutil.ToFloat64(nomad.FailedVersionsTagged(d).WithLabelValues("test-job")); got != 1 {
		t.Errorf("failed_versions_tagged_total: want 1, got %v", got)
	}
}

// --- active rollback --------------------------------------------------------

type revertCall struct {
	jobID   string
	version uint64
	enforce *uint64
}

// rollbackMock builds a mock with no drift (plan None) where the latest
// deployment failed at version 5 and version history has a stable 3 below it.
// liveUpdate sets the live job's update stanza (to exercise auto_revert).
func rollbackMock(meta map[string]string, liveUpdate *nomadapi.UpdateStrategy, reverts *[]revertCall) *mockJobsClient {
	mock := defaultMock()
	mock.parseHCLFn = func(string, bool) (*nomadapi.Job, error) {
		return &nomadapi.Job{ID: strPtr("test-job"), Meta: meta}, nil
	}
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		return &nomadapi.Job{ID: strPtr(jobID), Status: strPtr("running"), JobModifyIndex: uint64Ptr(42), Update: liveUpdate}, nil, nil
	}
	mock.planFn = func(j *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{Type: "None"}}, nil, nil
	}
	mock.latestDeploymentFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Deployment, *nomadapi.QueryMeta, error) {
		return &nomadapi.Deployment{JobVersion: 5, Status: nomadapi.DeploymentStatusFailed}, nil, nil
	}
	mock.versionsFn = func(jobID string, diffs bool, q *nomadapi.QueryOptions) ([]*nomadapi.Job, []*nomadapi.JobDiff, *nomadapi.QueryMeta, error) {
		return []*nomadapi.Job{
			{ID: strPtr("test-job"), Version: uint64Ptr(5), Stable: boolPtr(false)},
			{ID: strPtr("test-job"), Version: uint64Ptr(3), Stable: boolPtr(true)},
		}, nil, nil, nil
	}
	mock.revertFn = func(jobID string, version uint64, enforce *uint64, q *nomadapi.WriteOptions, _, _ string) (*nomadapi.JobRegisterResponse, *nomadapi.WriteMeta, error) {
		*reverts = append(*reverts, revertCall{jobID: jobID, version: version, enforce: enforce})
		return &nomadapi.JobRegisterResponse{EvalID: "e", JobModifyIndex: 99}, nil, nil
	}
	return mock
}

func rollbackCfg(allow bool) *config.Config {
	c := applyCfg("none", false)
	c.AllowRollback = allow
	return c
}

func TestActiveRollback_RevertsFailedDeployment(t *testing.T) {
	var reverts []revertCall
	mock := rollbackMock(nil, nil, &reverts)
	d := nomad.NewWithClient(rollbackCfg(true), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(reverts) != 1 {
		t.Fatalf("a failed deployment without auto_revert should revert, got %d calls", len(reverts))
	}
	if reverts[0].version != 3 {
		t.Errorf("revert target: want last stable version 3, got %d", reverts[0].version)
	}
	if reverts[0].enforce == nil || *reverts[0].enforce != 5 {
		t.Errorf("revert must CAS-guard on the failed version 5, got %v", reverts[0].enforce)
	}
	if got := testutil.ToFloat64(nomad.Rollbacks(d).WithLabelValues("test-job", "queued")); got != 1 {
		t.Errorf("rollbacks_total{result=queued}: want 1, got %v", got)
	}
}

func TestActiveRollback_AutoRevertWins(t *testing.T) {
	var reverts []revertCall
	mock := rollbackMock(nil, &nomadapi.UpdateStrategy{AutoRevert: boolPtr(true)}, &reverts)
	d := nomad.NewWithClient(rollbackCfg(true), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(reverts) != 0 {
		t.Errorf("auto_revert always wins: nomad-gitops must not revert, got %d calls", len(reverts))
	}
	if got := testutil.ToFloat64(nomad.Rollbacks(d).WithLabelValues("test-job", "deferred_auto_revert")); got != 1 {
		t.Errorf("rollbacks_total{result=deferred_auto_revert}: want 1, got %v", got)
	}
}

func TestActiveRollback_DisabledByDefault(t *testing.T) {
	var reverts []revertCall
	mock := rollbackMock(nil, nil, &reverts)
	d := nomad.NewWithClient(rollbackCfg(false), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(reverts) != 0 {
		t.Errorf("rollback is off by default: no revert, got %d calls", len(reverts))
	}
}

func TestActiveRollback_MetaOptIn(t *testing.T) {
	var reverts []revertCall
	mock := rollbackMock(map[string]string{"gitops_rollback": "true"}, nil, &reverts)
	d := nomad.NewWithClient(rollbackCfg(false), mock) // global off

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(reverts) != 1 {
		t.Errorf("gitops_rollback=true must enable rollback for this job despite the global default off, got %d calls", len(reverts))
	}
}

func TestActiveRollback_NoStableVersion(t *testing.T) {
	var reverts []revertCall
	mock := rollbackMock(nil, nil, &reverts)
	mock.versionsFn = func(jobID string, diffs bool, q *nomadapi.QueryOptions) ([]*nomadapi.Job, []*nomadapi.JobDiff, *nomadapi.QueryMeta, error) {
		return []*nomadapi.Job{{ID: strPtr("test-job"), Version: uint64Ptr(5), Stable: boolPtr(false)}}, nil, nil, nil
	}
	d := nomad.NewWithClient(rollbackCfg(true), mock)

	runCheck(t, d, "aaaa111fffff")
	nomad.DrainUpdates(d)

	if len(reverts) != 0 {
		t.Errorf("no stable version to revert to: must not revert, got %d calls", len(reverts))
	}
	if got := testutil.ToFloat64(nomad.Rollbacks(d).WithLabelValues("test-job", "no_stable_version")); got != 1 {
		t.Errorf("rollbacks_total{result=no_stable_version}: want 1, got %v", got)
	}
}
