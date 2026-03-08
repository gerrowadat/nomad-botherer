package config

import (
	"flag"
	"os"
	"testing"
	"time"
)

func newFS() *flag.FlagSet {
	return flag.NewFlagSet("test", flag.ContinueOnError)
}

// ── envOrDefault / envDurationOrDefault ──────────────────────────────────────

func TestEnvOrDefault_Missing(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_FOO"
	os.Unsetenv(key)
	if got := envOrDefault(key, "default"); got != "default" {
		t.Errorf("want default, got %q", got)
	}
}

func TestEnvOrDefault_Set(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_FOO"
	os.Setenv(key, "fromenv")
	t.Cleanup(func() { os.Unsetenv(key) })
	if got := envOrDefault(key, "default"); got != "fromenv" {
		t.Errorf("want fromenv, got %q", got)
	}
}

func TestEnvDurationOrDefault_Missing(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_DUR"
	os.Unsetenv(key)
	if got := envDurationOrDefault(key, time.Minute); got != time.Minute {
		t.Errorf("want 1m, got %v", got)
	}
}

func TestEnvDurationOrDefault_Valid(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_DUR"
	os.Setenv(key, "30s")
	t.Cleanup(func() { os.Unsetenv(key) })
	if got := envDurationOrDefault(key, time.Minute); got != 30*time.Second {
		t.Errorf("want 30s, got %v", got)
	}
}

func TestEnvDurationOrDefault_Invalid(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_DUR"
	os.Setenv(key, "not-a-duration")
	t.Cleanup(func() { os.Unsetenv(key) })
	if got := envDurationOrDefault(key, time.Minute); got != time.Minute {
		t.Errorf("invalid duration should fall back to default, got %v", got)
	}
}

// ── LoadFromArgs ─────────────────────────────────────────────────────────────

func TestLoadFromArgs_RequiresRepoURL(t *testing.T) {
	os.Unsetenv("GIT_REPO_URL")
	_, err := LoadFromArgs(newFS(), []string{})
	if err == nil {
		t.Error("expected error when repo URL is not set")
	}
}

func TestLoadFromArgs_FlagSetsRepoURL(t *testing.T) {
	os.Unsetenv("GIT_REPO_URL")
	cfg, err := LoadFromArgs(newFS(), []string{"--repo-url", "https://example.com/repo.git"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RepoURL != "https://example.com/repo.git" {
		t.Errorf("unexpected RepoURL: %q", cfg.RepoURL)
	}
}

func TestLoadFromArgs_EnvSetsRepoURL(t *testing.T) {
	os.Setenv("GIT_REPO_URL", "https://env.example.com/repo.git")
	t.Cleanup(func() { os.Unsetenv("GIT_REPO_URL") })

	cfg, err := LoadFromArgs(newFS(), []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RepoURL != "https://env.example.com/repo.git" {
		t.Errorf("unexpected RepoURL: %q", cfg.RepoURL)
	}
}

func TestLoadFromArgs_FlagOverridesEnv(t *testing.T) {
	os.Setenv("GIT_REPO_URL", "https://env.example.com/repo.git")
	t.Cleanup(func() { os.Unsetenv("GIT_REPO_URL") })

	cfg, err := LoadFromArgs(newFS(), []string{"--repo-url", "https://flag.example.com/repo.git"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// flag takes priority: flags are registered with env default, then the flag
	// value overwrites it when explicitly passed.
	if cfg.RepoURL != "https://flag.example.com/repo.git" {
		t.Errorf("flag value should win over env default, got %q", cfg.RepoURL)
	}
}

func TestLoadFromArgs_Defaults(t *testing.T) {
	// Clear any env vars that could affect defaults.
	for _, k := range []string{
		"GIT_REPO_URL", "GIT_BRANCH", "NOMAD_ADDR", "NOMAD_NAMESPACE",
		"LISTEN_ADDR", "WEBHOOK_PATH", "LOG_LEVEL", "POLL_INTERVAL", "DIFF_INTERVAL",
	} {
		os.Unsetenv(k)
	}

	cfg, err := LoadFromArgs(newFS(), []string{"--repo-url", "https://example.com/r.git"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"Branch", cfg.Branch, "main"},
		{"NomadAddr", cfg.NomadAddr, "http://127.0.0.1:4646"},
		{"NomadNamespace", cfg.NomadNamespace, "default"},
		{"ListenAddr", cfg.ListenAddr, ":8080"},
		{"WebhookPath", cfg.WebhookPath, "/webhook"},
		{"LogLevel", cfg.LogLevel, "info"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, c.got)
		}
	}

	if cfg.PollInterval != 5*time.Minute {
		t.Errorf("PollInterval: want 5m, got %v", cfg.PollInterval)
	}
	if cfg.DiffInterval != time.Minute {
		t.Errorf("DiffInterval: want 1m, got %v", cfg.DiffInterval)
	}
}

func TestLoadFromArgs_BranchFlag(t *testing.T) {
	os.Unsetenv("GIT_BRANCH")
	cfg, err := LoadFromArgs(newFS(), []string{
		"--repo-url", "https://example.com/r.git",
		"--branch", "develop",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Branch != "develop" {
		t.Errorf("want develop, got %q", cfg.Branch)
	}
}

func TestLoadFromArgs_PollIntervalEnv(t *testing.T) {
	os.Setenv("POLL_INTERVAL", "10s")
	t.Cleanup(func() { os.Unsetenv("POLL_INTERVAL") })

	cfg, err := LoadFromArgs(newFS(), []string{"--repo-url", "https://example.com/r.git"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("want 10s, got %v", cfg.PollInterval)
	}
}
