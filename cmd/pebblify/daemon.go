//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/api"
	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/health"
	"github.com/Dockermint/Pebblify/internal/daemon/notify"
	"github.com/Dockermint/Pebblify/internal/daemon/queue"
	"github.com/Dockermint/Pebblify/internal/daemon/runner"
	"github.com/Dockermint/Pebblify/internal/daemon/store"
	"github.com/Dockermint/Pebblify/internal/daemon/telemetry"
)

// daemonShutdownTimeout bounds the graceful drain of each sub-server and the
// runner's in-flight job on SIGINT or SIGTERM.
const daemonShutdownTimeout = 30 * time.Second

// daemonCmd is the entrypoint for the `pebblify daemon` subcommand. It parses
// a minimal flag set (only --version / --help are recognised), loads config
// and secrets, wires every sub-server, and blocks until a termination signal
// arrives.
func daemonCmd(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.Usage = func() { daemonUsage(fs.Output()) }
	showVersion := fs.Bool("version", false, "print daemon version and exit")
	fs.BoolVar(showVersion, "V", false, "alias for --version")

	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		printVersion()
		return
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "pebblify daemon does not accept positional arguments\n\n")
		daemonUsage(os.Stderr)
		os.Exit(1)
	}

	loaded, err := config.Load("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pebblify daemon: load config: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger(loaded.Secrets.LogLevel)

	if err := runDaemonLoop(loaded, logger); err != nil {
		logger.Error("daemon exited with error", "error", err)
		os.Exit(1)
	}
}

// runDaemonLoop wires every sub-server and blocks until the root context is
// cancelled. It is extracted from daemonCmd so the wiring is testable in
// isolation without spawning signal handlers.
func runDaemonLoop(loaded *config.Loaded, logger *slog.Logger) error {
	cfg := loaded.Config

	rootCtx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := queue.New(queue.Options{
		BufferSize: cfg.Queue.BufferSize,
		Logger:     logger,
	})

	notifier, err := notify.New(cfg.Notify, loaded.Secrets)
	if err != nil {
		return fmt.Errorf("build notifier: %w", err)
	}

	targets, err := store.New(cfg.Save, loaded.Secrets)
	if err != nil {
		return fmt.Errorf("build store targets: %w", err)
	}

	telemetryServer, collectors, err := telemetry.New(cfg.Telemetry, logger)
	if err != nil {
		return fmt.Errorf("build telemetry: %w", err)
	}

	r := runner.New(runner.Deps{
		Cfg:        cfg,
		Secrets:    loaded.Secrets,
		Queue:      q,
		Notifier:   notifier,
		Targets:    targets,
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: 0},
		Collectors: collectors,
	})

	apiServer, err := api.New(cfg.API, loaded.Secrets, q, logger, api.Options{
		Version:    Version,
		Collectors: collectors,
	})
	if err != nil {
		return fmt.Errorf("build api server: %w", err)
	}

	healthServer, err := health.New(cfg.Health, newReadinessAdapter(rootCtx, q), logger)
	if err != nil {
		return fmt.Errorf("build health server: %w", err)
	}

	logger.Info("pebblify daemon started",
		"version", Version,
		"health_enabled", cfg.Health.Enable,
		"telemetry_enabled", cfg.Telemetry.Enable,
		"notify_enabled", cfg.Notify.Enable,
		"save_targets", len(targets),
	)

	errCh := startConcurrentServers(rootCtx, logger, r, apiServer, healthServer, telemetryServer)

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received; draining")
	case err := <-errCh:
		if err != nil {
			logger.Error("sub-server exited with error; shutting down", "error", err)
		} else {
			logger.Info("sub-server exited cleanly; shutting down")
		}
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), daemonShutdownTimeout)
	defer shutdownCancel()

	drainDaemon(shutdownCtx, logger, q, r, apiServer, healthServer, telemetryServer)
	return nil
}

// startConcurrentServers launches every sub-server and the runner under their
// own goroutines, returning a channel that surfaces the first error observed.
// The channel is buffered so no goroutine blocks when multiple errors surface.
func startConcurrentServers(ctx context.Context, logger *slog.Logger,
	r runner.Runner, apiSrv api.Server, healthSrv health.Server,
	telemetrySrv telemetry.Server) <-chan error {
	errCh := make(chan error, 4)

	go func() {
		if err := r.Start(ctx); err != nil {
			logger.Error("runner stopped with error", "error", err)
			errCh <- err
			return
		}
		errCh <- nil
	}()

	go func() {
		if err := apiSrv.Start(ctx); err != nil {
			logger.Error("api server exited", "error", err)
			errCh <- err
			return
		}
		errCh <- nil
	}()

	if healthSrv != nil {
		go func() {
			if err := healthSrv.Start(ctx); err != nil {
				logger.Error("health server exited", "error", err)
				errCh <- err
				return
			}
			errCh <- nil
		}()
	}
	if telemetrySrv != nil {
		go func() {
			if err := telemetrySrv.Start(ctx); err != nil {
				logger.Error("telemetry server exited", "error", err)
				errCh <- err
				return
			}
			errCh <- nil
		}()
	}

	return errCh
}

// drainDaemon stops every sub-server and the runner concurrently, bounded by
// ctx. Individual errors are logged but never aborted on; the daemon must
// attempt a full drain to leave external systems consistent.
func drainDaemon(ctx context.Context, logger *slog.Logger, q queue.Queue,
	r runner.Runner, apiSrv api.Server, healthSrv health.Server,
	telemetrySrv telemetry.Server) {
	if err := q.Shutdown(ctx); err != nil {
		logger.Warn("queue shutdown error", "error", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("runner stop error", "error", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := apiSrv.Stop(ctx); err != nil {
			logger.Warn("api server stop error", "error", err)
		}
	}()

	if healthSrv != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := healthSrv.Stop(ctx); err != nil {
				logger.Warn("health server stop error", "error", err)
			}
		}()
	}
	if telemetrySrv != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := telemetrySrv.Stop(ctx); err != nil {
				logger.Warn("telemetry server stop error", "error", err)
			}
		}()
	}

	wg.Wait()
	logger.Info("pebblify daemon stopped")
}

// newLogger configures slog with the level read from PEBBLIFY_LOG_LEVEL. An
// unrecognised or empty value defaults to INFO.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug":
		lvl = slog.LevelDebug
	case "", "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}

// readinessAdapter bridges the queue's shutdown gate to the ReadinessProvider
// contract consumed by the health package. Ready() reports false once the
// root context is cancelled so external orchestrators see the daemon go
// not-ready as soon as SIGTERM arrives, before the queue is fully drained.
type readinessAdapter struct {
	ctx context.Context
	q   queue.Queue
}

// newReadinessAdapter wires the root context and queue into a ReadinessProvider.
// The returned adapter reads ctx.Done() on every Ready() call so it reflects
// shutdown intent without polling.
func newReadinessAdapter(ctx context.Context, q queue.Queue) *readinessAdapter {
	return &readinessAdapter{ctx: ctx, q: q}
}

// Ready implements health.ReadinessProvider. It returns false when either the
// queue is absent or the root context has fired (daemon is shutting down).
func (a *readinessAdapter) Ready() bool {
	if a.q == nil {
		return false
	}
	if a.ctx != nil {
		select {
		case <-a.ctx.Done():
			return false
		default:
		}
	}
	return true
}

// daemonUsage prints the subcommand help to w. Write errors are deliberately
// ignored because the caller is always an already-failing CLI invocation; a
// failed help write would mask the primary error.
func daemonUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `pebblify daemon – long-running snapshot conversion service

Usage:
  pebblify daemon [options]

Options:
  -V, --version    Show version and exit
  -h, --help       Show this help

Configuration:
  All runtime settings are loaded from the TOML config file referenced by
  PEBBLIFY_CONFIG_PATH (default: ./config.toml). Secrets are read from
  environment variables only; see docs/specs/daemon-mode.md.
`)
}
