package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// ---- NoopNotifier ----

// TestNoopNotifier_AlwaysReturnsNil verifies the noop always succeeds.
func TestNoopNotifier_AlwaysReturnsNil(t *testing.T) {
	t.Parallel()
	n, err := New(config.NotifySection{Enable: false}, config.Secrets{})
	if err != nil {
		t.Fatalf("New(disabled) error = %v", err)
	}
	if err := n.Notify(context.Background(), Event{
		Kind:  EventStarted,
		JobID: "id1",
	}); err != nil {
		t.Errorf("noopNotifier.Notify() error = %v, want nil", err)
	}
}

// ---- EventKind.String ----

// TestEventKind_String covers all variants.
func TestEventKind_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind EventKind
		want string
	}{
		{EventStarted, "started"},
		{EventCompleted, "completed"},
		{EventFailed, "failed"},
		{EventKind(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.kind.String(); got != tt.want {
				t.Errorf("EventKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

// ---- renderMessage ----

// TestRenderMessage_ContainsJobIDAndURL checks the rendered text includes key fields.
func TestRenderMessage_ContainsJobIDAndURL(t *testing.T) {
	t.Parallel()
	ev := Event{
		Kind:    EventCompleted,
		JobID:   "abc123",
		JobURL:  "https://example.com/snap.tar.lz4",
		Details: "all done",
	}
	msg := renderMessage(ev)
	if !strings.Contains(msg, "abc123") {
		t.Errorf("renderMessage() missing JobID; got: %q", msg)
	}
	if !strings.Contains(msg, "https://example.com/snap.tar.lz4") {
		t.Errorf("renderMessage() missing JobURL; got: %q", msg)
	}
	if !strings.Contains(msg, "all done") {
		t.Errorf("renderMessage() missing Details; got: %q", msg)
	}
	if !strings.Contains(msg, "completed") {
		t.Errorf("renderMessage() missing event kind; got: %q", msg)
	}
}

// TestRenderMessage_IncludesErrorForFailed verifies error text appears on failure events.
func TestRenderMessage_IncludesErrorForFailed(t *testing.T) {
	t.Parallel()
	cause := errors.New("disk full")
	ev := Event{
		Kind:  EventFailed,
		JobID: "j1",
		Error: cause,
	}
	msg := renderMessage(ev)
	if !strings.Contains(msg, "disk full") {
		t.Errorf("renderMessage() missing error text; got: %q", msg)
	}
}

// ---- New factory ----

// TestNew_DisabledReturnsNoop returns a noopNotifier when enable = false.
func TestNew_DisabledReturnsNoop(t *testing.T) {
	t.Parallel()
	n, err := New(config.NotifySection{Enable: false}, config.Secrets{})
	if err != nil {
		t.Fatalf("New(disabled) error = %v", err)
	}
	if _, ok := n.(noopNotifier); !ok {
		t.Errorf("New(disabled) returned %T, want noopNotifier", n)
	}
}

// TestNew_TelegramReturnsNotifier returns a *TelegramNotifier for telegram mode.
func TestNew_TelegramReturnsNotifier(t *testing.T) {
	t.Parallel()
	cfg := config.NotifySection{Enable: true, Mode: "telegram", ChannelID: "42"}
	secrets := config.Secrets{TelegramBotToken: "mytoken"}
	n, err := New(cfg, secrets)
	if err != nil {
		t.Fatalf("New(telegram) error = %v", err)
	}
	if _, ok := n.(*TelegramNotifier); !ok {
		t.Errorf("New(telegram) returned %T, want *TelegramNotifier", n)
	}
}

// TestNew_UnknownModeReturnsError returns ErrUnsupportedMode for unknown mode.
func TestNew_UnknownModeReturnsError(t *testing.T) {
	t.Parallel()
	cfg := config.NotifySection{Enable: true, Mode: "webhook"}
	_, err := New(cfg, config.Secrets{})
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Errorf("New(webhook) error = %v, want %v", err, ErrUnsupportedMode)
	}
}

// ---- TelegramNotifier ----

// fakeTelegramServer wraps an httptest.Server and provides a mutex-safe getter
// for the last recorded request body. Access to lastReq must be via Last().
type fakeTelegramServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	lastReq telegramRequest
}

// Last returns a copy of the last recorded telegram request under the mutex.
func (f *fakeTelegramServer) Last() telegramRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq
}

// newFakeTelegramServer returns a test server that records the last request body
// and responds with the given status code.
func newFakeTelegramServer(t *testing.T, status int) *fakeTelegramServer {
	t.Helper()
	f := &fakeTelegramServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("newFakeTelegramServer: read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req telegramRequest
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("newFakeTelegramServer: unmarshal request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.lastReq = req
		f.mu.Unlock()
		w.WriteHeader(status)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("newFakeTelegramServer: write response: %v", err)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newFakeTelegramServerSequence responds with status codes in sequence.
func newFakeTelegramServerSequence(t *testing.T, statuses []int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("newFakeTelegramServerSequence: read request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = b
		mu.Lock()
		code := http.StatusInternalServerError
		if idx < len(statuses) {
			code = statuses[idx]
			idx++
		}
		mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTelegramNotifierWithBase creates a TelegramNotifier pointing at a custom base URL.
func newTelegramNotifierWithBase(token, channelID, base string, client *http.Client) *TelegramNotifier {
	n := NewTelegramNotifier(token, channelID, client)
	// Override the endpoint by monkey-patching: the notifier always builds the
	// endpoint from telegramAPIBase. Since we can't reassign the package-level
	// const, we create a custom client whose transport redirects to our server.
	// A simpler approach: create an http.Client with a custom transport.
	n.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &redirectTransport{base: base, inner: http.DefaultTransport},
	}
	return n
}

// redirectTransport rewrites the Host and scheme of every request to a fixed base URL.
type redirectTransport struct {
	base  string // e.g. "http://127.0.0.1:PORT"
	inner http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Build a clone so we don't mutate the caller's request.
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	// Extract just the host:port from base.
	trimmed := strings.TrimPrefix(rt.base, "http://")
	trimmed = strings.TrimPrefix(trimmed, "https://")
	clone.URL.Host = trimmed
	return rt.inner.RoundTrip(clone)
}

// TestTelegramNotifier_MissingToken returns ErrTelegramMissingToken.
func TestTelegramNotifier_MissingToken(t *testing.T) {
	t.Parallel()
	n := NewTelegramNotifier("", "42", nil)
	err := n.Notify(context.Background(), Event{Kind: EventStarted, JobID: "j1"})
	if !errors.Is(err, ErrTelegramMissingToken) {
		t.Errorf("Notify empty token error = %v, want %v", err, ErrTelegramMissingToken)
	}
}

// TestTelegramNotifier_MissingChannel returns ErrTelegramMissingChannel.
func TestTelegramNotifier_MissingChannel(t *testing.T) {
	t.Parallel()
	n := NewTelegramNotifier("mytoken", "", nil)
	err := n.Notify(context.Background(), Event{Kind: EventStarted, JobID: "j1"})
	if !errors.Is(err, ErrTelegramMissingChannel) {
		t.Errorf("Notify empty channel error = %v, want %v", err, ErrTelegramMissingChannel)
	}
}

// TestTelegramNotifier_HappyPath sends a notification and verifies payload.
func TestTelegramNotifier_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFakeTelegramServer(t, http.StatusOK)

	n := newTelegramNotifierWithBase("tok", "42", f.srv.URL, nil)
	ev := Event{
		Kind:   EventCompleted,
		JobID:  "job-123",
		JobURL: "https://example.com/snap.tar.lz4",
	}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Errorf("Notify() unexpected error: %v", err)
	}
	last := f.Last()
	if last.ChatID != "42" {
		t.Errorf("ChatID = %q, want %q", last.ChatID, "42")
	}
	if last.ParseMode != "HTML" {
		t.Errorf("ParseMode = %q, want HTML", last.ParseMode)
	}
	if !strings.Contains(last.Text, "job-123") {
		t.Errorf("Text missing job ID; got: %q", last.Text)
	}
}

// TestTelegramNotifier_PermanentFailure_NoBotTokenInLogs verifies 4xx returns ErrTelegramPermanent.
func TestTelegramNotifier_PermanentFailure(t *testing.T) {
	t.Parallel()
	srv := newFakeTelegramServerSequence(t, []int{http.StatusForbidden})
	n := newTelegramNotifierWithBase("tok", "42", srv.URL, nil)
	err := n.Notify(context.Background(), Event{Kind: EventStarted, JobID: "j1"})
	if !errors.Is(err, ErrTelegramPermanent) {
		t.Errorf("Notify 403 error = %v, want %v", err, ErrTelegramPermanent)
	}
}

// TestTelegramNotifier_TransientSuccessOnRetry first 5xx, second 200 -> nil.
func TestTelegramNotifier_TransientSuccessOnRetry(t *testing.T) {
	t.Parallel()
	srv := newFakeTelegramServerSequence(t, []int{http.StatusInternalServerError, http.StatusOK})
	n := newTelegramNotifierWithBase("tok", "42", srv.URL, nil)
	if err := n.Notify(context.Background(), Event{Kind: EventStarted, JobID: "j1"}); err != nil {
		t.Errorf("Notify transient retry error = %v, want nil", err)
	}
}

// TestTelegramNotifier_TransientFailsBothAttempts both 5xx -> ErrTelegramTransient.
func TestTelegramNotifier_TransientFailsBothAttempts(t *testing.T) {
	t.Parallel()
	srv := newFakeTelegramServerSequence(t, []int{http.StatusBadGateway, http.StatusBadGateway})
	n := newTelegramNotifierWithBase("tok", "42", srv.URL, nil)
	err := n.Notify(context.Background(), Event{Kind: EventStarted, JobID: "j1"})
	if !errors.Is(err, ErrTelegramTransient) {
		t.Errorf("Notify two 5xx error = %v, want %v", err, ErrTelegramTransient)
	}
}

// TestTelegramNotifier_ContextCancelled returns context.Canceled.
func TestTelegramNotifier_ContextCancelled(t *testing.T) {
	t.Parallel()
	// Server sleeps briefly; the context is already cancelled before Notify is
	// called, so the HTTP client must surface the cancellation without actually
	// waiting for the server response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	n := newTelegramNotifierWithBase("tok", "42", srv.URL, nil)
	err := n.Notify(ctx, Event{Kind: EventStarted, JobID: "j1"})
	if err == nil {
		t.Error("Notify cancelled ctx error = nil, want non-nil")
	}
}

// TestIsTransient_Table covers all classification branches.
func TestIsTransient_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{"nil error", 200, nil, false},
		{"4xx permanent", 400, errors.New("perm"), false},
		{"5xx transient", 500, errors.New("trans"), true},
		{"context canceled", 0, context.Canceled, false},
		{"context deadline", 0, context.DeadlineExceeded, false},
		{"5xx boundary", 599, errors.New("trans"), true},
		{"600 not transient", 600, errors.New("x"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTransient(tt.status, tt.err)
			if got != tt.want {
				t.Errorf("isTransient(%d, %v) = %v, want %v", tt.status, tt.err, got, tt.want)
			}
		})
	}
}

// TestSleepCtx_CancelledBeforeExpiry returns ctx.Err immediately.
func TestSleepCtx_CancelledBeforeExpiry(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := sleepCtx(ctx, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx cancelled error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("sleepCtx waited %v, want near-instant", elapsed)
	}
}

// TestSleepCtx_ExpiresNormally returns nil after duration elapses.
func TestSleepCtx_ExpiresNormally(t *testing.T) {
	t.Parallel()
	err := sleepCtx(context.Background(), 1*time.Millisecond)
	if err != nil {
		t.Errorf("sleepCtx normal error = %v, want nil", err)
	}
}
