package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gerrowadat/nomad-botherer/internal/config"
	"github.com/gerrowadat/nomad-botherer/internal/gitwatch"
	"github.com/gerrowadat/nomad-botherer/internal/nomad"
	"github.com/gerrowadat/nomad-botherer/internal/server"
)

// Injected at build time via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Loading config", "err", err)
		os.Exit(1)
	}

	setupLogging(cfg.LogLevel)
	slog.Info("Starting nomad-botherer", "version", version, "commit", commit, "buildDate", buildDate)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	differ, err := nomad.NewDiffer(cfg)
	if err != nil {
		slog.Error("Creating Nomad differ", "err", err)
		os.Exit(1)
	}

	// onChange is called by the watcher whenever the branch HEAD advances.
	// We close over watcher, which is set below before Run is called.
	var watcher *gitwatch.Watcher
	onChange := func(newCommit string) {
		hclFiles, err := watcher.ReadHCLFiles()
		if err != nil {
			slog.Error("Reading HCL files from repo", "err", err)
			return
		}
		if err := differ.Check(hclFiles, newCommit); err != nil {
			slog.Error("Running diff check", "err", err)
		}
	}

	watcher = gitwatch.New(cfg, onChange)

	if err := watcher.Clone(ctx); err != nil {
		slog.Error("Cloning repository", "err", err)
		os.Exit(1)
	}

	// Run an initial diff check immediately after clone.
	onChange(watcher.LastCommit())

	// Periodic diff checks independent of git changes (catches Nomad-side drift).
	go func() {
		ticker := time.NewTicker(cfg.DiffInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				commit, _ := watcher.Status()
				hclFiles, err := watcher.ReadHCLFiles()
				if err != nil {
					slog.Error("Reading HCL files for periodic check", "err", err)
					continue
				}
				if err := differ.Check(hclFiles, commit); err != nil {
					slog.Error("Periodic diff check failed", "err", err)
				}
			}
		}
	}()

	// Watcher polls git and triggers onChange on new commits.
	go watcher.Run(ctx)

	srv := server.New(cfg, differ, watcher, version)
	if err := srv.Run(ctx); err != nil {
		slog.Error("HTTP server error", "err", err)
		os.Exit(1)
	}
}

func setupLogging(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}
