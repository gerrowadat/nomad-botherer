// Package server provides the HTTP server exposing /healthz, /metrics, and
// the git webhook endpoint.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webhookgithub "github.com/go-playground/webhooks/v6/github"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gerrowadat/nomad-botherer/internal/config"
	"github.com/gerrowadat/nomad-botherer/internal/nomad"
)

// DiffSource is satisfied by *nomad.Differ.
type DiffSource interface {
	Diffs() ([]nomad.JobDiff, time.Time, string)
}

// GitStatusSource is satisfied by *gitwatch.Watcher.
type GitStatusSource interface {
	Trigger()
	Status() (lastCommit string, lastUpdate time.Time)
}

// Server holds the HTTP mux and all dependencies.
type Server struct {
	cfg     *config.Config
	diffs   DiffSource
	git     GitStatusSource
	version string
	mux     *http.ServeMux

	// Prometheus metrics
	jobDiffsGauge      *prometheus.GaugeVec
	lastCheckGauge     prometheus.Gauge
	gitLastUpdateGauge prometheus.Gauge
}

// New creates a Server that registers Prometheus metrics into the default registry.
func New(cfg *config.Config, diffs DiffSource, git GitStatusSource, version string) *Server {
	return NewWithRegistry(cfg, diffs, git, version, prometheus.DefaultRegisterer)
}

// NewWithRegistry creates a Server with a custom Prometheus Registerer.
// Useful in tests to avoid duplicate-registration panics when creating multiple servers.
func NewWithRegistry(cfg *config.Config, diffs DiffSource, git GitStatusSource, version string, reg prometheus.Registerer) *Server {
	s := &Server{
		cfg:     cfg,
		diffs:   diffs,
		git:     git,
		version: version,

		jobDiffsGauge: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "nomad_botherer_job_diffs",
			Help: "1 for each job/diff-type combination currently detected.",
		}, []string{"job", "diff_type"}),

		lastCheckGauge: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name: "nomad_botherer_last_check_timestamp_seconds",
			Help: "Unix timestamp of the most recent diff check.",
		}),

		gitLastUpdateGauge: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name: "nomad_botherer_git_last_update_timestamp_seconds",
			Help: "Unix timestamp of the most recent git fetch.",
		}),
	}

	// Static info metric carrying the build version.
	promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
		Name: "nomad_botherer_info",
		Help: "Build information.",
	}, []string{"version"}).WithLabelValues(version).Set(1)

	// Use the provided registry as the Prometheus gatherer if possible,
	// otherwise fall back to the global default.
	var metricsHandler http.Handler
	if g, ok := reg.(prometheus.Gatherer); ok {
		metricsHandler = promhttp.HandlerFor(g, promhttp.HandlerOpts{})
	} else {
		metricsHandler = promhttp.Handler()
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/{$}", s.handleIndex)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/diffs", s.handleDiffs)
	s.mux.Handle("/metrics", metricsHandler)
	s.mux.HandleFunc(cfg.WebhookPath, s.handleWebhook())

	return s
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: s.mux,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	slog.Info("HTTP server listening", "addr", s.cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Handler returns the underlying http.Handler, useful for testing without a
// real listener.
func (s *Server) Handler() http.Handler {
	return s.mux
}

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>nomad-botherer</title>
  <style>
    body { font-family: sans-serif; max-width: 640px; margin: 2em auto; color: #222; }
    h1   { margin-bottom: 0.2em; }
    .ok  { color: #2a7a2a; font-weight: bold; }
    .bad { color: #b94040; font-weight: bold; }
    code { background: #f4f4f4; padding: 0.1em 0.3em; border-radius: 3px; }
    ul   { line-height: 1.8; }
  </style>
</head>
<body>
  <h1>nomad-botherer <small>{{.Version}}</small></h1>
  <p>Status:
    {{- if .DiffCount}}
    <span class="bad">{{.DiffCount}} difference(s) detected</span>
    {{- else}}
    <span class="ok">OK — no differences</span>
    {{- end}}
  </p>
  {{- if .LastCheck}}
  <p>Last check: {{.LastCheck}}{{if .Commit}} (commit <code>{{.Commit}}</code>){{end}}</p>
  {{- end}}
  <ul>
    <li><a href="/diffs">/diffs</a> — current job diffs (plan-style)</li>
    <li><a href="/healthz">/healthz</a> — JSON health check</li>
    <li><a href="/metrics">/metrics</a> — Prometheus metrics</li>
  </ul>
</body>
</html>
`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	diffs, lastCheck, commit := s.diffs.Diffs()

	data := struct {
		Version   string
		DiffCount int
		LastCheck string
		Commit    string
	}{
		Version:   s.version,
		DiffCount: len(diffs),
		Commit:    commit,
	}
	if !lastCheck.IsZero() {
		data.LastCheck = lastCheck.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, data)
}

func (s *Server) handleDiffs(w http.ResponseWriter, r *http.Request) {
	diffs, lastCheck, commit := s.diffs.Diffs()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, renderDiffsText(diffs, lastCheck, commit))
}

// HealthResponse is the JSON body returned by /healthz.
type HealthResponse struct {
	Status     string      `json:"status"`
	DiffCount  int         `json:"diff_count"`
	Diffs      []DiffEntry `json:"diffs"`
	LastCheck  string      `json:"last_check,omitempty"`
	GitCommit  string      `json:"git_commit,omitempty"`
	GitUpdated string      `json:"git_updated,omitempty"`
}

// DiffEntry is one element of HealthResponse.Diffs.
type DiffEntry struct {
	JobID    string `json:"job_id"`
	HCLFile  string `json:"hcl_file,omitempty"`
	DiffType string `json:"diff_type"`
	Detail   string `json:"detail"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	diffs, lastCheck, gitCommit := s.diffs.Diffs()
	_, gitUpdated := s.git.Status()

	// Update Prometheus gauges on every scrape of /healthz too.
	s.jobDiffsGauge.Reset()
	for _, d := range diffs {
		s.jobDiffsGauge.WithLabelValues(d.JobID, string(d.DiffType)).Set(1)
	}
	if !lastCheck.IsZero() {
		s.lastCheckGauge.Set(float64(lastCheck.Unix()))
	}
	if !gitUpdated.IsZero() {
		s.gitLastUpdateGauge.Set(float64(gitUpdated.Unix()))
	}

	status := "ok"
	if len(diffs) > 0 {
		status = "diffs_detected"
	}

	entries := make([]DiffEntry, 0, len(diffs))
	for _, d := range diffs {
		entries = append(entries, DiffEntry{
			JobID:    d.JobID,
			HCLFile:  d.HCLFile,
			DiffType: string(d.DiffType),
			Detail:   d.Detail,
		})
	}

	resp := HealthResponse{
		Status:    status,
		DiffCount: len(diffs),
		Diffs:     entries,
	}
	if !lastCheck.IsZero() {
		resp.LastCheck = lastCheck.Format(time.RFC3339)
	}
	if gitCommit != "" {
		resp.GitCommit = gitCommit
	}
	if !gitUpdated.IsZero() {
		resp.GitUpdated = gitUpdated.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleWebhook() http.HandlerFunc {
	hook, err := webhookgithub.New(webhookgithub.Options.Secret(s.cfg.WebhookSecret))
	if err != nil {
		// This only errors with an invalid secret; log and serve a stub.
		slog.Error("Failed to initialise GitHub webhook handler", "err", err)
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "webhook handler misconfigured", http.StatusInternalServerError)
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := hook.Parse(r, webhookgithub.PushEvent, webhookgithub.PingEvent)
		if err != nil {
			if err == webhookgithub.ErrEventNotFound {
				// Event type we don't care about — acknowledge and move on.
				w.WriteHeader(http.StatusOK)
				return
			}
			slog.Warn("Webhook parse error", "err", err)
			http.Error(w, "bad webhook payload", http.StatusBadRequest)
			return
		}

		switch p := payload.(type) {
		case webhookgithub.PushPayload:
			branch := strings.TrimPrefix(p.Ref, "refs/heads/")
			slog.Info("Received push webhook", "branch", branch, "commit", p.After)
			if branch == s.cfg.Branch {
				s.git.Trigger()
			}
		case webhookgithub.PingPayload:
			slog.Info("Received ping webhook", "hook_id", p.HookID)
		}

		w.WriteHeader(http.StatusOK)
	}
}
