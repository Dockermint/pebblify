package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Server is the HTTP health probe server. It exposes /healthz/live,
// /healthz/ready, and /healthz/startup backed by a shared ProbeState.
type Server struct {
	httpServer *http.Server
	state      *ProbeState
}

// NewServer returns a Server listening on the given port and wired to the
// supplied ProbeState. The server is not started until ListenAndServe is
// called.
func NewServer(port int, state *ProbeState) *Server {
	mux := http.NewServeMux()
	s := &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       5 * time.Second,
			WriteTimeout:      5 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
		state: state,
	}

	mux.HandleFunc("/healthz/live", s.handleLiveness)
	mux.HandleFunc("/healthz/ready", s.handleReadiness)
	mux.HandleFunc("/healthz/startup", s.handleStartup)

	return s
}

// ListenAndServe binds the configured port and blocks serving HTTP
// requests until the server is shut down.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully stops the server, waiting for in-flight requests
// up to the deadline carried by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.state.IsAlive() {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintln(w, "not alive")
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.state.IsReady() {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintln(w, "not ready")
}

func (s *Server) handleStartup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.state.IsStarted() {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintln(w, "not started")
}
