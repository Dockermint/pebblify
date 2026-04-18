package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/queue"
)

// ---- fakeQueue for api tests ----

type fakeQueue struct {
	enqueueErr  error
	depth       int
	current     *queue.Job
	containsOut bool
}

func (q *fakeQueue) Enqueue(job queue.Job) error {
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	q.depth++
	return nil
}
func (q *fakeQueue) Dequeue(_ context.Context) (queue.Job, error) { return queue.Job{}, nil }
func (q *fakeQueue) Depth() int                                    { return q.depth }
func (q *fakeQueue) Contains(_ string) bool                        { return q.containsOut }
func (q *fakeQueue) Current() *queue.Job                           { return q.current }
func (q *fakeQueue) Shutdown(_ context.Context) error              { return nil }

// ---- helpers ----

// newTestHandler creates a handler wired to the given queue.
func newTestHandler(q queue.Queue) *handler {
	return newHandler(q, nil, nil, "0.4.0-test", time.Now())
}

// postJob sends a POST /v1/jobs request to h and returns the response.
func postJob(h *handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleSubmitJob(rr, req)
	return rr
}

// ---- handleSubmitJob ----

// TestHandleSubmitJob_ValidURL returns 201 Created.
func TestHandleSubmitJob_ValidURL(t *testing.T) {
	t.Parallel()
	fq := &fakeQueue{}
	h := newTestHandler(fq)
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	if rr.Code != http.StatusCreated {
		t.Errorf("handleSubmitJob valid url = %d, want %d; body: %s",
			rr.Code, http.StatusCreated, rr.Body)
	}
	var resp submitJobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.JobID == "" {
		t.Error("job_id is empty")
	}
}

// TestHandleSubmitJob_EmptyBody returns 400.
func TestHandleSubmitJob_EmptyBody(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", nil)
	rr := httptest.NewRecorder()
	h.handleSubmitJob(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty body = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// TestHandleSubmitJob_EmptyURL returns 400.
func TestHandleSubmitJob_EmptyURL(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	rr := postJob(h, `{"url":""}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty url = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// TestHandleSubmitJob_InvalidURL returns 400.
func TestHandleSubmitJob_InvalidURL(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	rr := postJob(h, `{"url":"not-a-valid-url"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid url = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// TestHandleSubmitJob_QueueFull returns 503.
func TestHandleSubmitJob_QueueFull(t *testing.T) {
	t.Parallel()
	fq := &fakeQueue{enqueueErr: queue.ErrQueueFull}
	h := newTestHandler(fq)
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("queue full = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

// TestHandleSubmitJob_ShuttingDown returns 503.
func TestHandleSubmitJob_ShuttingDown(t *testing.T) {
	t.Parallel()
	fq := &fakeQueue{enqueueErr: queue.ErrShuttingDown}
	h := newTestHandler(fq)
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("shutting down = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

// TestHandleSubmitJob_Duplicate returns 409.
func TestHandleSubmitJob_Duplicate(t *testing.T) {
	t.Parallel()
	fq := &fakeQueue{enqueueErr: queue.ErrDuplicate}
	h := newTestHandler(fq)
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate = %d, want %d", rr.Code, http.StatusConflict)
	}
}

// TestHandleSubmitJob_InvalidURLFromQueue returns 400 for ErrInvalidURL from queue.
func TestHandleSubmitJob_InvalidURLFromQueue(t *testing.T) {
	t.Parallel()
	fq := &fakeQueue{enqueueErr: queue.ErrInvalidURL}
	h := newTestHandler(fq)
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid url from queue = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// TestHandleSubmitJob_UnknownFields returns 400 (DisallowUnknownFields).
func TestHandleSubmitJob_UnknownFields(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	rr := postJob(h, `{"url":"https://example.com/snap.tar","extra":"field"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown fields = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// TestHandleSubmitJob_ContentTypeJSON response has application/json.
func TestHandleSubmitJob_ContentTypeJSON(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	rr := postJob(h, `{"url":"https://example.com/snap.tar.lz4"}`)
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ---- handleListJobs ----

// TestHandleListJobs_EmptyQueue returns 200 with depth 0.
func TestHandleListJobs_EmptyQueue(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rr := httptest.NewRecorder()
	h.handleListJobs(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleListJobs = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp jobsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.QueueDepth != 0 {
		t.Errorf("queue_depth = %d, want 0", resp.QueueDepth)
	}
	if resp.Current != nil {
		t.Errorf("current = %+v, want nil", resp.Current)
	}
}

// TestHandleListJobs_WithCurrentJob includes current job in response.
func TestHandleListJobs_WithCurrentJob(t *testing.T) {
	t.Parallel()
	cur := &queue.Job{ID: "j1", URL: "https://example.com/snap", SubmittedAt: time.Now()}
	fq := &fakeQueue{current: cur, depth: 2}
	h := newTestHandler(fq)
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rr := httptest.NewRecorder()
	h.handleListJobs(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleListJobs = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp jobsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.QueueDepth != 2 {
		t.Errorf("queue_depth = %d, want 2", resp.QueueDepth)
	}
	if resp.Current == nil {
		t.Fatal("current = nil, want non-nil")
	}
	if resp.Current.JobID != "j1" {
		t.Errorf("current.job_id = %q, want j1", resp.Current.JobID)
	}
}

// ---- handleStatus ----

// TestHandleStatus_GET returns 200 with version and uptime.
func TestHandleStatus_GET(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.handleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleStatus GET = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp statusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != "0.4.0-test" {
		t.Errorf("version = %q, want 0.4.0-test", resp.Version)
	}
}

// TestHandleStatus_POST returns 405.
func TestHandleStatus_POST(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodPost, "/v1/status", bytes.NewBufferString("{}"))
	rr := httptest.NewRecorder()
	h.handleStatus(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("handleStatus POST = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// ---- handleJobs dispatcher ----

// TestHandleJobs_DispatchesGET routes GET to handleListJobs.
func TestHandleJobs_DispatchesGET(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rr := httptest.NewRecorder()
	h.handleJobs(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handleJobs GET = %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestHandleJobs_DispatchesPOST routes POST to handleSubmitJob.
func TestHandleJobs_DispatchesPOST(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"url":"https://example.com/s.tar"}`))
	rr := httptest.NewRecorder()
	h.handleJobs(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("handleJobs POST = %d, want %d", rr.Code, http.StatusCreated)
	}
}

// TestHandleJobs_UnknownMethod returns 405.
func TestHandleJobs_UnknownMethod(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeQueue{})
	req := httptest.NewRequest(http.MethodDelete, "/v1/jobs", nil)
	rr := httptest.NewRecorder()
	h.handleJobs(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("handleJobs DELETE = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// ---- auth middleware ----

// TestBasicAuth_Unsecure passes request through.
func TestBasicAuth_Unsecure(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "unsecure", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("unsecure auth = %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestBasicAuth_BasicAuth_ValidPassword passes through with correct password.
func TestBasicAuth_BasicAuth_ValidPassword(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "basic_auth", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("ignored", "secret")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("valid basic auth = %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestBasicAuth_BasicAuth_InvalidPassword returns 401.
func TestBasicAuth_BasicAuth_InvalidPassword(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "basic_auth", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("user", "wrongpassword")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("invalid basic auth = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// TestBasicAuth_BearerToken_Valid passes through with correct bearer token.
func TestBasicAuth_BearerToken_Valid(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "basic_auth", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("valid bearer = %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestBasicAuth_BearerToken_Invalid returns 401.
func TestBasicAuth_BearerToken_Invalid(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "basic_auth", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("invalid bearer = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// TestBasicAuth_NoCredentials returns 401.
func TestBasicAuth_NoCredentials(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := basicAuth("secret", "basic_auth", inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no credentials = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// ---- checkAuth ----

// TestCheckAuth_Table covers the auth helper.
func TestCheckAuth_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(r *http.Request)
		token    string
		wantOK   bool
	}{
		{
			name:   "basic auth correct",
			token:  "tok",
			setup:  func(r *http.Request) { r.SetBasicAuth("u", "tok") },
			wantOK: true,
		},
		{
			name:   "basic auth wrong",
			token:  "tok",
			setup:  func(r *http.Request) { r.SetBasicAuth("u", "bad") },
			wantOK: false,
		},
		{
			name:   "bearer correct",
			token:  "tok",
			setup:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer tok") },
			wantOK: true,
		},
		{
			name:   "bearer wrong",
			token:  "tok",
			setup:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer bad") },
			wantOK: false,
		},
		{
			name:   "no auth",
			token:  "tok",
			setup:  func(_ *http.Request) {},
			wantOK: false,
		},
		{
			name:   "empty token always fails",
			token:  "",
			setup:  func(r *http.Request) { r.SetBasicAuth("u", "") },
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tt.setup(req)
			got := checkAuth(req, tt.token)
			if got != tt.wantOK {
				t.Errorf("checkAuth() = %v, want %v", got, tt.wantOK)
			}
		})
	}
}

// ---- recoverPanic middleware ----

// TestRecoverPanic_HandlerPanics returns 500 instead of crashing.
func TestRecoverPanic_HandlerPanics(t *testing.T) {
	t.Parallel()
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	safe := recoverPanic(nil, panicking)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	safe.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("recovered panic status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

// TestRecoverPanic_NoPanic passes through normally.
func TestRecoverPanic_NoPanic(t *testing.T) {
	t.Parallel()
	normal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	safe := recoverPanic(nil, normal)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	safe.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("no panic status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// ---- newJobID ----

// TestNewJobID_Unique returns distinct IDs on successive calls.
func TestNewJobID_Unique(t *testing.T) {
	t.Parallel()
	ids := make(map[string]bool)
	for i := 0; i < 10; i++ {
		id, err := newJobID()
		if err != nil {
			t.Fatalf("newJobID() error: %v", err)
		}
		if ids[id] {
			t.Errorf("duplicate id: %q", id)
		}
		ids[id] = true
	}
}

// TestNewJobID_Format returns a 32-hex-character string.
func TestNewJobID_Format(t *testing.T) {
	t.Parallel()
	id, err := newJobID()
	if err != nil {
		t.Fatalf("newJobID() error: %v", err)
	}
	if len(id) != 32 {
		t.Errorf("newJobID() len = %d, want 32", len(id))
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("newJobID() contains non-hex char %q", c)
		}
	}
}

// ---- New constructor ----

// TestNew_BasicAuthMissingToken returns ErrMissingBasicAuthToken.
func TestNew_BasicAuthMissingToken(t *testing.T) {
	t.Parallel()
	cfg := config.APISection{
		Host:                 "127.0.0.1",
		Port:                 2324,
		AuthentificationMode: "basic_auth",
	}
	_, err := New(cfg, config.Secrets{BasicAuthToken: ""}, &fakeQueue{}, nil, Options{})
	if !errors.Is(err, ErrMissingBasicAuthToken) {
		t.Errorf("New basic_auth no token error = %v, want %v", err, ErrMissingBasicAuthToken)
	}
}

// TestNew_NilQueueReturnsError.
func TestNew_NilQueueReturnsError(t *testing.T) {
	t.Parallel()
	cfg := config.APISection{
		Host:                 "127.0.0.1",
		Port:                 2324,
		AuthentificationMode: "unsecure",
	}
	_, err := New(cfg, config.Secrets{}, nil, nil, Options{})
	if err == nil {
		t.Fatal("New(nil queue) expected error, got nil")
	}
}
