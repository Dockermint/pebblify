// Package health exposes the /healthz (liveness) and /readyz (readiness)
// endpoints for the daemon.
//
// Liveness returns 200 as long as the process is alive; readiness returns 200
// only when the daemon's ReadinessProvider reports true. The listener is
// independent from the API and telemetry listeners per the daemon spec so an
// operator may enable readiness checks without exposing the API.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// Listener timeouts mirror the spec health row.
const (
	healthReadHeaderTimeout = 5 * time.Second
	healthReadTimeout       = 5 * time.Second
	healthWriteTimeout      = 10 * time.Second
	healthIdleTimeout       = 30 * time.Second
	healthShutdownTimeout   = 5 * time.Second
)

// ReadinessProvider reports whether the daemon is currently accepting jobs.
// The runner is expected to implement this (Ready returns true when the queue
// has not been shut down).
type ReadinessProvider interface {
	// Ready returns true when the daemon is accepting new jobs.
	Ready() bool
}

// Server is the contract implemented by the health listener.
type Server interface {
	// Start binds the listener and serves until ctx is cancelled. A nil
	// return means clean shutdown.
	Start(ctx context.Context) error
	// Stop performs a graceful shutdown bounded by ctx.
	Stop(ctx context.Context) error
}

// healthServer binds its own http.Server and serves /healthz and /readyz.
type healthServer struct {
	httpServer *http.Server
	ready      ReadinessProvider
	logger     *slog.Logger
}

// ErrNilReadinessProvider is returned by New when rp is nil.
var ErrNilReadinessProvider = errors.New("health: readiness provider is nil")

// New constructs a Server bound to cfg.Host:cfg.Port. When cfg.Enable is false,
// New returns (nil, nil); callers must treat the nil Server as disabled.
func New(cfg config.HealthSection, rp ReadinessProvider, logger *slog.Logger) (Server, error) {
	if !cfg.Enable {
		return nil, nil
	}
	if rp == nil {
		return nil, ErrNilReadinessProvider
	}
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	s := &healthServer{
		ready:  rp,
		logger: logger,
	}
	mux.HandleFunc("/healthz", s.handleLiveness)
	mux.HandleFunc("/readyz", s.handleReadiness)

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: healthReadHeaderTimeout,
		ReadTimeout:       healthReadTimeout,
		WriteTimeout:      healthWriteTimeout,
		IdleTimeout:       healthIdleTimeout,
	}
	return s, nil
}

// Start implements Server.
func (s *healthServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("health listen %s: %w", s.httpServer.Addr, err)
	}
	s.logger.Info("health listener started", "addr", s.httpServer.Addr)

	serveErr := make(chan error, 1)
	go func() {
		err := s.httpServer.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), healthShutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("health shutdown: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

// Stop implements Server.
func (s *healthServer) Stop(ctx context.Context) error {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("health stop: %w", err)
	}
	return nil
}

// handleLiveness responds 200 as long as the process is able to respond.
func (s *healthServer) handleLiveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadiness delegates the decision to ReadinessProvider.Ready().
func (s *healthServer) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.ready.Ready() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("not ready\n"))
}
