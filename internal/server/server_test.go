package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/gerrowadat/nomad-botherer/internal/config"
	"github.com/gerrowadat/nomad-botherer/internal/nomad"
	"github.com/gerrowadat/nomad-botherer/internal/server"
)

// mockDiffSource implements server.DiffSource.
type mockDiffSource struct {
	diffs      []nomad.JobDiff
	lastCheck  time.Time
	lastCommit string
}

func (m *mockDiffSource) Diffs() ([]nomad.JobDiff, time.Time, string) {
	return m.diffs, m.lastCheck, m.lastCommit
}

// mockGitSource implements server.GitStatusSource.
type mockGitSource struct {
	lastCommit string
	lastUpdate time.Time
	triggered  bool
}

func (m *mockGitSource) Trigger()                                    { m.triggered = true }
func (m *mockGitSource) Status() (string, time.Time)                 { return m.lastCommit, m.lastUpdate }

func newTestServer(t *testing.T, diffs []nomad.JobDiff) (*server.Server, *mockGitSource) {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:    ":0",
		WebhookPath:   "/webhook",
		WebhookSecret: "",
		Branch:        "main",
	}
	diffSrc := &mockDiffSource{
		diffs:      diffs,
		lastCheck:  time.Now(),
		lastCommit: "deadbeef",
	}
	gitSrc := &mockGitSource{
		lastCommit: "deadbeef",
		lastUpdate: time.Now(),
	}
	// Use a fresh registry per test to avoid duplicate-registration panics.
	srv := server.NewWithRegistry(cfg, diffSrc, gitSrc, "test", prometheus.NewRegistry())
	return srv, gitSrc
}

func TestHealthz_NoDiffs(t *testing.T) {
	srv, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp server.HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.DiffCount != 0 {
		t.Errorf("expected 0 diffs, got %d", resp.DiffCount)
	}
}

func TestHealthz_WithDiffs(t *testing.T) {
	diffs := []nomad.JobDiff{
		{JobID: "my-job", HCLFile: "jobs/my-job.hcl", DiffType: nomad.DiffTypeModified, Detail: "plan diff type \"Edited\""},
		{JobID: "lost-job", DiffType: nomad.DiffTypeMissingFromHCL, Detail: "running but no HCL"},
	}
	srv, _ := newTestServer(t, diffs)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp server.HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "diffs_detected" {
		t.Errorf("expected status diffs_detected, got %q", resp.Status)
	}
	if resp.DiffCount != 2 {
		t.Errorf("expected 2 diffs, got %d", resp.DiffCount)
	}
}

func TestHealthz_ContentType(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}
