package server_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func (m *mockGitSource) Trigger()                    { m.triggered = true }
func (m *mockGitSource) Status() (string, time.Time) { return m.lastCommit, m.lastUpdate }

// newTestServer builds a Server with fresh per-test Prometheus registry.
func newTestServer(t *testing.T, diffs []nomad.JobDiff) (*server.Server, *mockGitSource) {
	t.Helper()
	return newTestServerWithConfig(t, diffs, "", "main")
}

func newTestServerWithConfig(t *testing.T, diffs []nomad.JobDiff, webhookSecret, branch string) (*server.Server, *mockGitSource) {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:    ":0",
		WebhookPath:   "/webhook",
		WebhookSecret: webhookSecret,
		Branch:        branch,
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
	srv := server.NewWithRegistry(cfg, diffSrc, gitSrc, "test", prometheus.NewRegistry())
	return srv, gitSrc
}

// githubPushRequest builds a minimal GitHub push webhook request.
// If secret is non-empty, the correct HMAC-SHA256 signature is added.
func githubPushRequest(t *testing.T, secret, branch, commitSHA string) *http.Request {
	t.Helper()
	body := []byte(fmt.Sprintf(
		`{"ref":"refs/heads/%s","before":"0000000000000000000000000000000000000000","after":"%s","commits":[]}`,
		branch, commitSHA,
	))
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "test-delivery-id")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%x", mac.Sum(nil)))
	}
	return req
}

func githubPingRequest(t *testing.T) *http.Request {
	t.Helper()
	body := []byte(`{"hook_id":42,"hook":{"type":"Repository","id":42,"name":"web","active":true,"events":["push"]}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "test-ping-id")
	return req
}

// ── /healthz ──────────────────────────────────────────────────────────────────

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
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("want status ok, got %q", resp.Status)
	}
	if resp.DiffCount != 0 {
		t.Errorf("want 0 diffs, got %d", resp.DiffCount)
	}
}

func TestHealthz_WithDiffs(t *testing.T) {
	diffs := []nomad.JobDiff{
		{JobID: "api", HCLFile: "jobs/api.hcl", DiffType: nomad.DiffTypeModified, Detail: "Edited"},
		{JobID: "old", DiffType: nomad.DiffTypeMissingFromHCL, Detail: "running but no HCL"},
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
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "diffs_detected" {
		t.Errorf("want diffs_detected, got %q", resp.Status)
	}
	if resp.DiffCount != 2 {
		t.Errorf("want 2, got %d", resp.DiffCount)
	}
}

func TestHealthz_ContentType(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("want application/json, got %q", ct)
	}
}

func TestHealthz_IncludesGitInfo(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var resp server.HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GitCommit == "" {
		t.Error("GitCommit should be populated")
	}
	if resp.LastCheck == "" {
		t.Error("LastCheck should be populated")
	}
}

// ── /webhook ──────────────────────────────────────────────────────────────────

func TestWebhook_PushToWatchedBranch_Triggers(t *testing.T) {
	srv, gitSrc := newTestServer(t, nil)

	req := githubPushRequest(t, "", "main", "abc123")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !gitSrc.triggered {
		t.Error("Trigger() should have been called for a push to the watched branch")
	}
}

func TestWebhook_PushToOtherBranch_NoTrigger(t *testing.T) {
	srv, gitSrc := newTestServer(t, nil)

	req := githubPushRequest(t, "", "feature/foo", "abc123")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if gitSrc.triggered {
		t.Error("Trigger() should NOT have been called for a push to a different branch")
	}
}

func TestWebhook_UnknownEvent_Returns200(t *testing.T) {
	srv, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues") // registered: push + ping only
	req.Header.Set("X-GitHub-Delivery", "test-id")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for unknown event, got %d", rec.Code)
	}
}

func TestWebhook_PingEvent_Returns200(t *testing.T) {
	srv, gitSrc := newTestServer(t, nil)

	req := githubPingRequest(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for ping, got %d", rec.Code)
	}
	if gitSrc.triggered {
		t.Error("ping should not trigger a fetch")
	}
}

func TestWebhook_WithSecret_ValidSignature_Triggers(t *testing.T) {
	const secret = "super-secret-webhook-key"
	srv, gitSrc := newTestServerWithConfig(t, nil, secret, "main")

	req := githubPushRequest(t, secret, "main", "def456")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !gitSrc.triggered {
		t.Error("Trigger() should have been called with a valid signature")
	}
}

func TestWebhook_WithSecret_InvalidSignature_Rejected(t *testing.T) {
	const secret = "super-secret-webhook-key"
	srv, gitSrc := newTestServerWithConfig(t, nil, secret, "main")

	// Build the request with the correct payload structure but a wrong HMAC.
	body := []byte(`{"ref":"refs/heads/main","before":"000","after":"abc","commits":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "test-id")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalidsignature")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid signature, got %d", rec.Code)
	}
	if gitSrc.triggered {
		t.Error("Trigger() should NOT be called when signature is invalid")
	}
}
