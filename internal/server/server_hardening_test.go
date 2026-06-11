package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/gerrowadat/nomad-botherer/internal/config"
	"github.com/gerrowadat/nomad-botherer/internal/server"
)

// TestSecurityHeaders verifies that hardening headers are set on every
// response, including non-HTML endpoints.
func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t, nil)

	for _, path := range []string{"/", "/healthz", "/diffs", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "no-referrer",
			} {
				if got := w.Header().Get(header); got != want {
					t.Errorf("%s: got %q, want %q", header, got, want)
				}
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Error("Content-Security-Policy header not set")
			}
		})
	}
}

// TestWebhook_OversizedBody_Rejected verifies that a webhook body larger than
// the 25 MB cap is rejected rather than read fully into memory.
func TestWebhook_OversizedBody_Rejected(t *testing.T) {
	srv, gitSrc := newTestServer(t, nil)

	body := bytes.Repeat([]byte("a"), 25<<20+1)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "oversized-body")

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", w.Code)
	}
	if gitSrc.triggered {
		t.Error("oversized body must not trigger a git fetch")
	}
}

// TestAPIKey_SameLengthWrongKey_Rejected verifies that a wrong key of the same
// length as the configured key is rejected. Guards the hashed constant-time
// comparison in requireAPIKey.
func TestAPIKey_SameLengthWrongKey_Rejected(t *testing.T) {
	cfg := &config.Config{
		ListenAddr:  ":0",
		WebhookPath: "/webhook",
		Branch:      "main",
		APIKey:      "correct-key-0123456789",
	}
	diffSrc := &mockDiffSource{lastCheck: time.Now()}
	gitSrc := &mockGitSource{lastUpdate: time.Now()}
	srv := server.NewWithRegistry(cfg, diffSrc, gitSrc, server.BuildInfo{Version: "test"}, prometheus.NewRegistry())

	for key, wantCode := range map[string]int{
		"correct-key-0123456789": http.StatusOK,
		"correct-key-9876543210": http.StatusUnauthorized, // same length, wrong value
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != wantCode {
			t.Errorf("key %q: want %d, got %d", key, wantCode, w.Code)
		}
	}
}
