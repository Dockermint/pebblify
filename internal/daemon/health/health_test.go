package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// ---- fakeReadinessProvider ----

type fakeReady struct{ ready bool }

func (f *fakeReady) Ready() bool { return f.ready }

// ---- handleLiveness ----

// TestHandleLiveness_GET returns 200 OK with body "ok".
func TestHandleLiveness_GET(t *testing.T) {
	t.Parallel()
	s := &healthServer{ready: &fakeReady{ready: true}}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.handleLiveness(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleLiveness GET = %d, want %d", rr.Code, http.StatusOK)
	}
	if body := rr.Body.String(); body != "ok\n" {
		t.Errorf("handleLiveness GET body = %q, want %q", body, "ok\n")
	}
}

// TestHandleLiveness_POST returns 405 Method Not Allowed.
func TestHandleLiveness_POST(t *testing.T) {
	t.Parallel()
	s := &healthServer{ready: &fakeReady{ready: true}}
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.handleLiveness(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("handleLiveness POST = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// ---- handleReadiness ----

// TestHandleReadiness_Ready returns 200 when provider reports ready.
func TestHandleReadiness_Ready(t *testing.T) {
	t.Parallel()
	s := &healthServer{ready: &fakeReady{ready: true}}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	s.handleReadiness(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleReadiness ready = %d, want %d", rr.Code, http.StatusOK)
	}
	if body := rr.Body.String(); body != "ok\n" {
		t.Errorf("handleReadiness ready body = %q, want %q", body, "ok\n")
	}
}

// TestHandleReadiness_NotReady returns 503 when provider reports not ready.
func TestHandleReadiness_NotReady(t *testing.T) {
	t.Parallel()
	s := &healthServer{ready: &fakeReady{ready: false}}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	s.handleReadiness(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("handleReadiness not ready = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if body := rr.Body.String(); body != "not ready\n" {
		t.Errorf("handleReadiness not ready body = %q, want %q", body, "not ready\n")
	}
}

// TestHandleReadiness_POST returns 405.
func TestHandleReadiness_POST(t *testing.T) {
	t.Parallel()
	s := &healthServer{ready: &fakeReady{ready: true}}
	req := httptest.NewRequest(http.MethodPost, "/readyz", nil)
	rr := httptest.NewRecorder()
	s.handleReadiness(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("handleReadiness POST = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// ---- New constructor ----

// TestNew_DisabledReturnsNilNoError when enable = false.
func TestNew_DisabledReturnsNilNoError(t *testing.T) {
	t.Parallel()
	srv, err := New(config.HealthSection{Enable: false}, &fakeReady{ready: true}, nil)
	if err != nil {
		t.Fatalf("New(disabled) error = %v", err)
	}
	if srv != nil {
		t.Errorf("New(disabled) = %+v, want nil", srv)
	}
}

// TestNew_NilReadinessProviderReturnsError.
func TestNew_NilReadinessProviderReturnsError(t *testing.T) {
	t.Parallel()
	cfg := config.HealthSection{Enable: true, Host: "127.0.0.1", Port: 2325}
	_, err := New(cfg, nil, nil)
	if !errors.Is(err, ErrNilReadinessProvider) {
		t.Errorf("New(nil rp) error = %v, want %v", err, ErrNilReadinessProvider)
	}
}

// TestNew_ValidConfig builds a server without error.
func TestNew_ValidConfig(t *testing.T) {
	t.Parallel()
	cfg := config.HealthSection{Enable: true, Host: "127.0.0.1", Port: 2325}
	srv, err := New(cfg, &fakeReady{ready: true}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if srv == nil {
		t.Fatal("New() returned nil")
	}
}

// TestReadinessProvider_InterfaceCompliance fakeReady implements ReadinessProvider.
func TestReadinessProvider_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ ReadinessProvider = &fakeReady{}
}
