package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/queue"
	"github.com/Dockermint/Pebblify/internal/daemon/telemetry"
)

// Listener timeouts mirror the spec api row.
const (
	apiReadHeaderTimeout = 10 * time.Second
	apiReadTimeout       = 30 * time.Second
	apiWriteTimeout      = 120 * time.Second
	apiIdleTimeout       = 60 * time.Second
	apiShutdownTimeout   = 30 * time.Second
)

// Server is the contract implemented by the API listener.
type Server interface {
	// Start binds the listener and serves until ctx is cancelled. A nil
	// return means clean shutdown.
	Start(ctx context.Context) error
	// Stop performs a graceful shutdown bounded by ctx.
	Stop(ctx context.Context) error
}

// Options groups the optional dependencies New accepts. Required fields live
// on the explicit parameter list; optional (nilable) dependencies stay here so
// the constructor remains small without losing extensibility.
type Options struct {
	// Version is the daemon build version surfaced by GET /v1/status.
	Version string
	// Collectors is the Prometheus collector set; nil disables metric updates.
	Collectors *telemetry.Collectors
}

// apiServer owns the http.Server and handler wiring for the API listener.
type apiServer struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// ErrMissingBasicAuthToken is returned by New when basic_auth is selected but
// the secrets bundle does not carry a token. Config validation should reject
// this earlier; the guard exists to keep construction self-consistent.
var ErrMissingBasicAuthToken = errors.New("api: basic_auth enabled but token is empty")

// New constructs an API Server bound to cfg.Host:cfg.Port.
//
// The API listener is always active in daemon mode; there is no enable gate.
// The middleware chain is recover -> access log -> auth, wrapping a mux with
// the three v1 routes. The caller is expected to have validated cfg (port
// range, host, authentication mode) before invoking New.
func New(cfg config.APISection, secrets config.Secrets, q queue.Queue,
	logger *slog.Logger, opts Options) (Server, error) {
	if q == nil {
		return nil, errors.New("api: queue is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.AuthentificationMode == config.APIAuthBasic && secrets.BasicAuthToken == "" {
		return nil, ErrMissingBasicAuthToken
	}
	if cfg.AuthentificationMode == config.APIAuthUnsecure {
		logger.Warn("api listener running without authentication; set api.authentification_mode=basic_auth for production")
	}

	h := newHandler(q, logger, opts.Collectors, opts.Version, time.Now())

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs", h.handleJobs)
	mux.HandleFunc("/v1/status", h.handleStatus)

	authenticated := basicAuth(secrets.BasicAuthToken, cfg.AuthentificationMode, mux)
	logged := logRequests(logger, authenticated)
	protected := recoverPanic(logger, logged)

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	return &apiServer{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           protected,
			ReadHeaderTimeout: apiReadHeaderTimeout,
			ReadTimeout:       apiReadTimeout,
			WriteTimeout:      apiWriteTimeout,
			IdleTimeout:       apiIdleTimeout,
		},
		logger: logger,
	}, nil
}

// handleJobs is the single dispatcher for /v1/jobs; it branches on method so
// the mux registration stays compact.
func (h *handler) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleSubmitJob(w, r)
	case http.MethodGet:
		h.handleListJobs(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
	}
}

// Start implements Server.
func (s *apiServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("api listen %s: %w", s.httpServer.Addr, err)
	}
	s.logger.Info("api listener started", "addr", s.httpServer.Addr)

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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), apiShutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("api shutdown: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

// Stop implements Server.
func (s *apiServer) Stop(ctx context.Context) error {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("api stop: %w", err)
	}
	return nil
}
