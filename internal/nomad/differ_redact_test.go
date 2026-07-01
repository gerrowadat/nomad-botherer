package nomad_test

import (
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/gerrowadat/nomad-gitops/internal/config"
	"github.com/gerrowadat/nomad-gitops/internal/nomad"
)

// envDiffMock returns a client whose plan reports an edited env var and an
// edited image for test-job.
func envDiffMock() *mockJobsClient {
	mock := defaultMock()
	mock.planFn = func(job *nomadapi.Job, diff bool, q *nomadapi.WriteOptions) (*nomadapi.JobPlanResponse, *nomadapi.WriteMeta, error) {
		return &nomadapi.JobPlanResponse{Diff: &nomadapi.JobDiff{
			Type: "Edited",
			ID:   "test-job",
			TaskGroups: []*nomadapi.TaskGroupDiff{
				{
					Type: "Edited",
					Name: "web",
					Tasks: []*nomadapi.TaskDiff{
						{
							Type: "Edited",
							Name: "app",
							Fields: []*nomadapi.FieldDiff{
								{Type: "Edited", Name: "Env[API_TOKEN]", Old: "tok-old", New: "tok-new"},
								{Type: "Edited", Name: "Image", Old: "app:1", New: "app:2"},
							},
						},
					},
				},
			},
		}}, nil, nil
	}
	return mock
}

func TestDiffer_RedactSecrets_StoredDiffIsRedacted(t *testing.T) {
	cfg := &config.Config{NomadNamespace: "default", JobSelectorGlob: "*", RedactSecrets: true}
	reg := prometheus.NewRegistry()
	d := nomad.NewWithClientAndRegistry(cfg, envDiffMock(), reg)

	if err := d.Check(map[string]string{"jobs/test-job.hcl": `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 || diffs[0].PlanDiff == nil {
		t.Fatalf("expected 1 diff with PlanDiff, got %+v", diffs)
	}
	fields := diffs[0].PlanDiff.TaskGroups[0].Tasks[0].Fields
	if fields[0].Old != nomad.RedactedValue || fields[0].New != nomad.RedactedValue {
		t.Errorf("env var not redacted in stored diff: old=%q new=%q", fields[0].Old, fields[0].New)
	}
	if fields[1].Old != "app:1" || fields[1].New != "app:2" {
		t.Errorf("non-sensitive field should be untouched: old=%q new=%q", fields[1].Old, fields[1].New)
	}

	got := testutil.ToFloat64(nomad.RedactedFieldsCounter(d))
	if got != 1 {
		t.Errorf("nomad_gitops_diff_fields_redacted_total: want 1, got %v", got)
	}
}

func TestDiffer_RedactSecretsDisabled_ValuesIntact(t *testing.T) {
	cfg := &config.Config{NomadNamespace: "default", JobSelectorGlob: "*", RedactSecrets: false}
	d := nomad.NewWithClient(cfg, envDiffMock())

	if err := d.Check(map[string]string{"jobs/test-job.hcl": `job "test-job" {}`}, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffs, _, _ := d.Diffs()
	if len(diffs) != 1 || diffs[0].PlanDiff == nil {
		t.Fatalf("expected 1 diff with PlanDiff, got %+v", diffs)
	}
	fields := diffs[0].PlanDiff.TaskGroups[0].Tasks[0].Fields
	if fields[0].Old != "tok-old" || fields[0].New != "tok-new" {
		t.Errorf("redaction disabled: env values should be intact, got old=%q new=%q", fields[0].Old, fields[0].New)
	}
}
