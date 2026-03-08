package config

import (
	"os"
	"testing"
	"time"
)

func TestEnvOrDefault(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_FOO"
	os.Unsetenv(key)

	if got := envOrDefault(key, "default"); got != "default" {
		t.Errorf("want default, got %q", got)
	}

	os.Setenv(key, "fromenv")
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envOrDefault(key, "default"); got != "fromenv" {
		t.Errorf("want fromenv, got %q", got)
	}
}

func TestEnvDurationOrDefault(t *testing.T) {
	const key = "TEST_NOMAD_BOTHERER_DUR"
	os.Unsetenv(key)

	if got := envDurationOrDefault(key, time.Minute); got != time.Minute {
		t.Errorf("want 1m, got %v", got)
	}

	os.Setenv(key, "30s")
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envDurationOrDefault(key, time.Minute); got != 30*time.Second {
		t.Errorf("want 30s, got %v", got)
	}

	os.Setenv(key, "not-a-duration")
	if got := envDurationOrDefault(key, time.Minute); got != time.Minute {
		t.Errorf("invalid duration should fall back to default, got %v", got)
	}
}
