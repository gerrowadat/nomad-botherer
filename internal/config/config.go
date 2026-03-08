package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type Config struct {
	// Git
	RepoURL       string
	Branch        string
	PollInterval  time.Duration
	HCLDir        string
	GitToken      string
	GitSSHKeyPath string
	GitSSHKeyPass string

	// Nomad
	NomadAddr      string
	NomadToken     string
	NomadNamespace string

	// Server
	ListenAddr    string
	WebhookSecret string
	WebhookPath   string

	// Diff
	DiffInterval time.Duration

	// Logging
	LogLevel string
}

func Load() (*Config, error) {
	c := &Config{}

	flag.StringVar(&c.RepoURL, "repo-url", envOrDefault("GIT_REPO_URL", ""), "Remote git repo URL (required)")
	flag.StringVar(&c.Branch, "branch", envOrDefault("GIT_BRANCH", "main"), "Branch to watch")
	flag.DurationVar(&c.PollInterval, "poll-interval", envDurationOrDefault("POLL_INTERVAL", 5*time.Minute), "Git poll interval (e.g. 5m, 30s)")
	flag.StringVar(&c.HCLDir, "hcl-dir", envOrDefault("HCL_DIR", ""), "Directory within repo containing HCL job files (empty = repo root)")
	flag.StringVar(&c.GitToken, "git-token", envOrDefault("GIT_TOKEN", ""), "Git HTTP token for private repos (e.g. GitHub PAT)")
	flag.StringVar(&c.GitSSHKeyPath, "git-ssh-key", envOrDefault("GIT_SSH_KEY", ""), "Path to SSH private key for git auth")
	flag.StringVar(&c.GitSSHKeyPass, "git-ssh-key-password", envOrDefault("GIT_SSH_KEY_PASSWORD", ""), "SSH private key passphrase")

	flag.StringVar(&c.NomadAddr, "nomad-addr", envOrDefault("NOMAD_ADDR", "http://127.0.0.1:4646"), "Nomad API address")
	flag.StringVar(&c.NomadToken, "nomad-token", envOrDefault("NOMAD_TOKEN", ""), "Nomad ACL token")
	flag.StringVar(&c.NomadNamespace, "nomad-namespace", envOrDefault("NOMAD_NAMESPACE", "default"), "Nomad namespace")

	flag.StringVar(&c.ListenAddr, "listen-addr", envOrDefault("LISTEN_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&c.WebhookSecret, "webhook-secret", envOrDefault("WEBHOOK_SECRET", ""), "GitHub webhook secret for verifying payloads")
	flag.StringVar(&c.WebhookPath, "webhook-path", envOrDefault("WEBHOOK_PATH", "/webhook"), "HTTP path for webhook endpoint")

	flag.DurationVar(&c.DiffInterval, "diff-interval", envDurationOrDefault("DIFF_INTERVAL", time.Minute), "How often to run a diff check regardless of git changes")

	flag.StringVar(&c.LogLevel, "log-level", envOrDefault("LOG_LEVEL", "info"), "Log level: debug, info, warn, error")

	flag.Parse()

	if c.RepoURL == "" {
		return nil, fmt.Errorf("--repo-url / GIT_REPO_URL is required")
	}

	return c, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return def
}
