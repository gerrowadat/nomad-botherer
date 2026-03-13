package nomad_test

import (
	"fmt"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"github.com/gerrowadat/nomad-botherer/internal/config"
	"github.com/gerrowadat/nomad-botherer/internal/nomad"
)

func strPtr(s string) *string { return &s }

// mockJobsClient lets individual test cases override only the methods they care about.
type mockJobsClient struct {
	parseHCLFn func(jobHCL string, normalize bool) (*nomadapi.Job, error)
	planFn     func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error)
	infoFn     func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error)
	listFn     func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error)
}

func (m *mockJobsClient) ParseHCL(jobHCL string, normalize bool) (*nomadapi.Job, error) {
	return m.parseHCLFn(jobHCL, normalize)
}
func (m *mockJobsClient) Plan(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
	return m.planFn(job, diff, q)
}
func (m *mockJobsClient) Info(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
	return m.infoFn(jobID, q)
}
func (m *mockJobsClient) List(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
	return m.listFn(q)
}

// defaultMock returns a client where everything succeeds with no diffs.
func defaultMock() *mockJobsClient {
	return &mockJobsClient{
		parseHCLFn: func(jobHCL string, normalize bool) (*nomadapi.Job, error) {
			return &nomadapi.Job{ID: strPtr("test-job")}, nil
		},
		planFn: func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
			return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{Type: "None"}}, nil, nil
		},
		infoFn: func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
			return &nomadapi.Job{ID: strPtr(jobID)}, nil, nil
		},
		listFn: func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
			return nil, nil, nil
		},
	}
}

func newTestDiffer(mock *mockJobsClient) *nomad.Differ {
	cfg := &config.Config{NomadAddr: "http://localhost:4646", NomadNamespace: "default"}
	return nomad.NewWithClient(cfg, mock)
}

func newTestDifferWithDeadJobs(mock *mockJobsClient) *nomad.Differ {
	cfg := &config.Config{NomadAddr: "http://localhost:4646", NomadNamespace: "default", IncludeDeadJobs: true}
	return nomad.NewWithClient(cfg, mock)
}

func TestDiffer_NoChanges(t *testing.T) {
	d := newTestDiffer(defaultMock())

	if err := d.Check(map[string]string{"jobs/test-job.hcl": `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, lastCheck, commit := d.Diffs()
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs, got %d: %+v", len(diffs), diffs)
	}
	if lastCheck.IsZero() {
		t.Error("lastCheck should not be zero after Check()")
	}
	if commit != "abc123" {
		t.Errorf("expected commit abc123, got %q", commit)
	}
}

func TestDiffer_MissingFromNomad(t *testing.T) {
	mock := defaultMock()
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		return nil, nil, fmt.Errorf("Unexpected response code: 404 (job not found)")
	}
	d := newTestDiffer(mock)

	if err := d.Check(map[string]string{"jobs/test-job.hcl": `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].DiffType != nomad.DiffTypeMissingFromNomad {
		t.Errorf("expected %s, got %s", nomad.DiffTypeMissingFromNomad, diffs[0].DiffType)
	}
	if diffs[0].HCLFile != "jobs/test-job.hcl" {
		t.Errorf("unexpected HCLFile: %q", diffs[0].HCLFile)
	}
}

func TestDiffer_Modified(t *testing.T) {
	mock := defaultMock()
	mock.planFn = func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{Type: "Edited"}}, nil, nil
	}
	d := newTestDiffer(mock)

	if err := d.Check(map[string]string{"jobs/test-job.hcl": `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].DiffType != nomad.DiffTypeModified {
		t.Errorf("expected %s, got %s", nomad.DiffTypeModified, diffs[0].DiffType)
	}
}

func TestDiffer_MissingFromHCL(t *testing.T) {
	mock := defaultMock()
	mock.listFn = func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
		return []*nomadapi.JobListStub{{ID: "orphan-job", Status: "running"}}, nil, nil
	}
	d := newTestDiffer(mock)

	// No HCL files → every running Nomad job is orphaned.
	if err := d.Check(map[string]string{}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].DiffType != nomad.DiffTypeMissingFromHCL {
		t.Errorf("expected %s, got %s", nomad.DiffTypeMissingFromHCL, diffs[0].DiffType)
	}
	if diffs[0].JobID != "orphan-job" {
		t.Errorf("unexpected job ID: %q", diffs[0].JobID)
	}
}

func TestDiffer_HCLParseError_Skipped(t *testing.T) {
	mock := defaultMock()
	mock.parseHCLFn = func(jobHCL string, normalize bool) (*nomadapi.Job, error) {
		return nil, fmt.Errorf("HCL syntax error")
	}
	d := newTestDiffer(mock)

	// Content has a job stanza but the (mock) parser rejects it — should log,
	// increment the error counter, and move on without returning an error.
	if err := d.Check(map[string]string{`bad.hcl`: `job "broken" { INVALID }`}, "abc123"); err != nil {
		t.Fatalf("Check should not fail on parse errors: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs after parse error, got %d", len(diffs))
	}
}

func TestDiffer_NonJobHCL_Skipped(t *testing.T) {
	mock := defaultMock()
	parseCalled := false
	mock.parseHCLFn = func(jobHCL string, normalize bool) (*nomadapi.Job, error) {
		parseCalled = true
		return nil, fmt.Errorf("should not be called")
	}
	d := newTestDiffer(mock)

	aclPolicy := `
name        = "my-policy"
description = "ACL policy for readers"
rules       = <<EOT
namespace "default" {
  policy = "read"
}
EOT`
	volume := `
id        = "database"
name      = "database"
type      = "csi"
plugin_id = "aws-ebs"
`
	if err := d.Check(map[string]string{
		"policies/readers.hcl": aclPolicy,
		"volumes/db.hcl":       volume,
	}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if parseCalled {
		t.Error("ParseHCL should not be called for non-job HCL files")
	}
	diffs, _, _ := d.Diffs()
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs for non-job files, got %d", len(diffs))
	}
}

func TestDiffer_MultipleDiffTypes(t *testing.T) {
	mock := defaultMock()

	// job-a: exists but modified
	// job-b: missing from Nomad
	// job-c: running in Nomad but not in HCL
	mock.parseHCLFn = func(jobHCL string, normalize bool) (*nomadapi.Job, error) {
		if strings.Contains(jobHCL, "job-a") {
			return &nomadapi.Job{ID: strPtr("job-a")}, nil
		}
		return &nomadapi.Job{ID: strPtr("job-b")}, nil
	}
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		if jobID == "job-b" {
			return nil, nil, fmt.Errorf("404: not found")
		}
		return &nomadapi.Job{ID: strPtr(jobID)}, nil, nil
	}
	mock.planFn = func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{Type: "Edited"}}, nil, nil
	}
	mock.listFn = func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
		return []*nomadapi.JobListStub{
			{ID: "job-a", Status: "running"},
			{ID: "job-b", Status: "pending"},
			{ID: "job-c", Status: "running"},
		}, nil, nil
	}

	d := newTestDiffer(mock)
	if err := d.Check(map[string]string{`a.hcl`: `job "job-a" {}`, `b.hcl`: `job "job-b" {}`}, "xyz"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 3 {
		t.Errorf("expected 3 diffs, got %d: %+v", len(diffs), diffs)
	}
}

// TestDiffer_DeadJob_TreatedAsMissing verifies that a job found in Nomad with
// status "dead" is reported as missing_from_nomad by default.
func TestDiffer_DeadJob_TreatedAsMissing(t *testing.T) {
	mock := defaultMock()
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		return &nomadapi.Job{ID: strPtr(jobID), Status: strPtr("dead")}, nil, nil
	}
	d := newTestDiffer(mock)

	if err := d.Check(map[string]string{`jobs/test-job.hcl`: `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].DiffType != nomad.DiffTypeMissingFromNomad {
		t.Errorf("expected %s for dead job, got %s", nomad.DiffTypeMissingFromNomad, diffs[0].DiffType)
	}
}

// TestDiffer_DeadJob_IncludeDeadJobs verifies that with IncludeDeadJobs=true a
// dead job is planned against normally (not treated as missing).
func TestDiffer_DeadJob_IncludeDeadJobs(t *testing.T) {
	mock := defaultMock()
	mock.infoFn = func(jobID string, q *nomadapi.QueryOptions) (*nomadapi.Job, *nomadapi.QueryMeta, error) {
		return &nomadapi.Job{ID: strPtr(jobID), Status: strPtr("dead")}, nil, nil
	}
	// Plan returns no diff — job is dead but config matches.
	mock.planFn = func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{Type: "None"}}, nil, nil
	}
	d := newTestDifferWithDeadJobs(mock)

	if err := d.Check(map[string]string{`jobs/test-job.hcl`: `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs with IncludeDeadJobs=true and no plan diff, got %d: %+v", len(diffs), diffs)
	}
}

// TestDiffer_DeadJobInNomad_NoHCL_NotReported verifies that a dead job in
// Nomad without an HCL file is NOT reported as missing_from_hcl by default.
func TestDiffer_DeadJobInNomad_NoHCL_NotReported(t *testing.T) {
	mock := defaultMock()
	mock.listFn = func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
		return []*nomadapi.JobListStub{
			{ID: "stopped-job", Status: "dead"},
		}, nil, nil
	}
	d := newTestDiffer(mock)

	if err := d.Check(map[string]string{}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 0 {
		t.Errorf("dead job without HCL should not be reported by default, got %d diffs: %+v", len(diffs), diffs)
	}
}

// TestDiffer_DeadJobInNomad_NoHCL_IncludeDeadJobs verifies that with
// IncludeDeadJobs=true a dead Nomad job without HCL IS reported as missing_from_hcl.
func TestDiffer_DeadJobInNomad_NoHCL_IncludeDeadJobs(t *testing.T) {
	mock := defaultMock()
	mock.listFn = func(q *nomadapi.QueryOptions) ([]*nomadapi.JobListStub, *nomadapi.QueryMeta, error) {
		return []*nomadapi.JobListStub{
			{ID: "stopped-job", Status: "dead"},
		}, nil, nil
	}
	d := newTestDifferWithDeadJobs(mock)

	if err := d.Check(map[string]string{}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff with IncludeDeadJobs=true, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].DiffType != nomad.DiffTypeMissingFromHCL {
		t.Errorf("expected %s, got %s", nomad.DiffTypeMissingFromHCL, diffs[0].DiffType)
	}
}
