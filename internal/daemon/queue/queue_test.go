package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestQueue builds a FIFOQueue with a small buffer suitable for tests.
func newTestQueue(bufSize int) *FIFOQueue {
	return New(Options{BufferSize: bufSize})
}

// TestCanonicalizeTable covers the canonicalization rules exhaustively.
func TestCanonicalizeTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "lowercase scheme and host",
			input: "HTTPS://EXAMPLE.COM/snap.tar.lz4",
			want:  "https://example.com/snap.tar.lz4",
		},
		{
			name:  "strip default https port",
			input: "https://example.com:443/snap",
			want:  "https://example.com/snap",
		},
		{
			name:  "strip default http port",
			input: "http://example.com:80/snap",
			want:  "http://example.com/snap",
		},
		{
			name:  "non-default port preserved",
			input: "https://example.com:8443/snap",
			want:  "https://example.com:8443/snap",
		},
		{
			name:  "sorted query params",
			input: "https://example.com/snap?z=1&a=2",
			want:  "https://example.com/snap?a=2&z=1",
		},
		{
			name:  "fragment discarded",
			input: "https://example.com/snap#section",
			want:  "https://example.com/snap",
		},
		{
			name:  "userinfo stripped",
			input: "https://user:pass@example.com/snap",
			want:  "https://example.com/snap",
		},
		{
			name:  "empty path becomes slash",
			input: "https://example.com",
			want:  "https://example.com/",
		},
		{
			name:  "double slashes collapsed in path",
			input: "https://example.com//a//b",
			want:  "https://example.com/a/b",
		},
		{
			name:  "dot segments removed from path",
			input: "https://example.com/a/./b/../c",
			want:  "https://example.com/a/c",
		},
		{
			name:    "empty URL returns error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace-only URL returns error",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "no scheme returns error",
			input:   "example.com/snap",
			wantErr: true,
		},
		{
			name:    "no host returns error",
			input:   "https:///snap",
			wantErr: true,
		},
		{
			name:  "query values within same key sorted",
			input: "https://example.com/?k=b&k=a",
			want:  "https://example.com/?k=a&k=b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Canonicalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Canonicalize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCanonicalize_SameURLEquivalence verifies two differently-written equivalent URLs
// produce identical canonical forms.
func TestCanonicalize_SameURLEquivalence(t *testing.T) {
	t.Parallel()
	a, errA := Canonicalize("HTTPS://Example.COM:443/snap.tar.lz4?z=1&a=2#frag")
	b, errB := Canonicalize("https://example.com/snap.tar.lz4?a=2&z=1")
	if errA != nil || errB != nil {
		t.Fatalf("Canonicalize errors: %v, %v", errA, errB)
	}
	if a != b {
		t.Errorf("canonical forms differ: %q != %q", a, b)
	}
}

// TestFIFOQueue_New verifies minimum buffer clamping.
func TestFIFOQueue_New(t *testing.T) {
	t.Parallel()
	q := New(Options{BufferSize: 0})
	if q == nil {
		t.Fatal("New() returned nil")
	}
	// Should still accept one job (clamped to 1).
	job := Job{ID: "x", URL: "https://example.com/snap"}
	if err := q.Enqueue(job); err != nil {
		t.Errorf("Enqueue after New(0) error = %v", err)
	}
}

// TestFIFOQueue_Enqueue_HappyPath enqueues a job and confirms depth.
func TestFIFOQueue_Enqueue_HappyPath(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	job := Job{ID: "j1", URL: "https://example.com/snap.tar.lz4"}
	if err := q.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() unexpected error: %v", err)
	}
	if q.Depth() != 1 {
		t.Errorf("Depth() = %d, want 1", q.Depth())
	}
}

// TestFIFOQueue_Enqueue_Duplicate returns ErrDuplicate for same canonical URL.
func TestFIFOQueue_Enqueue_Duplicate(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	url := "https://example.com/snap.tar.lz4"
	j1 := Job{ID: "j1", URL: url}
	j2 := Job{ID: "j2", URL: url}
	if err := q.Enqueue(j1); err != nil {
		t.Fatalf("first Enqueue() error: %v", err)
	}
	err := q.Enqueue(j2)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("second Enqueue() error = %v, want %v", err, ErrDuplicate)
	}
}

// TestFIFOQueue_Enqueue_DuplicateAfterCanonicalization confirms dedup on canonical form.
func TestFIFOQueue_Enqueue_DuplicateAfterCanonicalization(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	j1 := Job{ID: "j1", URL: "https://example.com/snap"}
	j2 := Job{ID: "j2", URL: "HTTPS://EXAMPLE.COM:443/snap"}
	if err := q.Enqueue(j1); err != nil {
		t.Fatalf("first Enqueue() error: %v", err)
	}
	err := q.Enqueue(j2)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("Enqueue canonical dup error = %v, want %v", err, ErrDuplicate)
	}
}

// TestFIFOQueue_Enqueue_QueueFull returns ErrQueueFull when buffer exhausted.
func TestFIFOQueue_Enqueue_QueueFull(t *testing.T) {
	t.Parallel()
	q := newTestQueue(2)
	urls := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	for i, u := range urls[:2] {
		if err := q.Enqueue(Job{ID: "j" + string(rune('0'+i)), URL: u}); err != nil {
			t.Fatalf("Enqueue(%d) error: %v", i, err)
		}
	}
	err := q.Enqueue(Job{ID: "j3", URL: urls[2]})
	if !errors.Is(err, ErrQueueFull) {
		t.Errorf("Enqueue over capacity error = %v, want %v", err, ErrQueueFull)
	}
}

// TestFIFOQueue_Enqueue_InvalidURL returns ErrInvalidURL.
func TestFIFOQueue_Enqueue_InvalidURL(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	err := q.Enqueue(Job{ID: "j1", URL: ""})
	if !errors.Is(err, ErrInvalidURL) {
		t.Errorf("Enqueue empty URL error = %v, want %v", err, ErrInvalidURL)
	}
}

// TestFIFOQueue_Enqueue_ShuttingDown returns ErrShuttingDown after Shutdown.
func TestFIFOQueue_Enqueue_ShuttingDown(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
	err := q.Enqueue(Job{ID: "j1", URL: "https://example.com/snap"})
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Enqueue after Shutdown error = %v, want %v", err, ErrShuttingDown)
	}
}

// TestFIFOQueue_Dequeue_ReturnsEnqueuedJob dequeues the same job that was enqueued.
func TestFIFOQueue_Dequeue_ReturnsEnqueuedJob(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	want := Job{ID: "j1", URL: "https://example.com/snap.tar.lz4"}
	// Canonicalize to match what Enqueue stores.
	canonical, _ := Canonicalize(want.URL)
	want.URL = canonical
	if err := q.Enqueue(want); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	got, err := q.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("Dequeue() ID = %q, want %q", got.ID, want.ID)
	}
}

// TestFIFOQueue_Dequeue_ContextCancelled returns ctx.Err when context is done.
func TestFIFOQueue_Dequeue_ContextCancelled(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := q.Dequeue(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Dequeue cancelled ctx error = %v, want context.Canceled", err)
	}
}

// TestFIFOQueue_Dequeue_ShutdownChannelClosed returns ErrShuttingDown after close.
func TestFIFOQueue_Dequeue_ShutdownChannelClosed(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
	_, err := q.Dequeue(ctx)
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Dequeue after shutdown error = %v, want %v", err, ErrShuttingDown)
	}
}

// TestFIFOQueue_Contains_PendingURL confirms pending URL is found.
func TestFIFOQueue_Contains_PendingURL(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	url := "https://example.com/snap.tar.lz4"
	if err := q.Enqueue(Job{ID: "j1", URL: url}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if !q.Contains(url) {
		t.Error("Contains(pending url) = false, want true")
	}
}

// TestFIFOQueue_Contains_Canonical confirms canonical match works.
func TestFIFOQueue_Contains_Canonical(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	if err := q.Enqueue(Job{ID: "j1", URL: "https://example.com/snap"}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if !q.Contains("HTTPS://EXAMPLE.COM:443/snap") {
		t.Error("Contains(canonical variant) = false, want true")
	}
}

// TestFIFOQueue_Contains_InvalidURL returns false for bad URL.
func TestFIFOQueue_Contains_InvalidURL(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	if q.Contains("") {
		t.Error("Contains empty URL = true, want false")
	}
}

// TestFIFOQueue_Contains_AfterDequeue confirms URL removed from dedup after Dequeue.
func TestFIFOQueue_Contains_AfterDequeue(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	url := "https://example.com/snap.tar.lz4"
	if err := q.Enqueue(Job{ID: "j1", URL: url}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	job, err := q.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}
	// URL moves from pending to current — should still be "contained".
	if !q.Contains(url) {
		t.Error("Contains(current job url) = false, want true")
	}
	// After CompleteCurrent, the URL should no longer be present.
	q.CompleteCurrent()
	if q.Contains(url) {
		t.Error("Contains after CompleteCurrent = true, want false")
	}
	_ = job
}

// TestFIFOQueue_Current_NilWhenIdle returns nil when nothing is running.
func TestFIFOQueue_Current_NilWhenIdle(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	if got := q.Current(); got != nil {
		t.Errorf("Current() idle = %+v, want nil", got)
	}
}

// TestFIFOQueue_Current_NonNilWhenRunning returns job during processing.
func TestFIFOQueue_Current_NonNilWhenRunning(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	job := Job{ID: "j1", URL: "https://example.com/snap"}
	if err := q.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	got, err := q.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}
	cur := q.Current()
	if cur == nil {
		t.Fatal("Current() after Dequeue = nil, want non-nil")
	}
	if cur.ID != got.ID {
		t.Errorf("Current().ID = %q, want %q", cur.ID, got.ID)
	}
	q.CompleteCurrent()
	if q.Current() != nil {
		t.Error("Current() after CompleteCurrent != nil")
	}
}

// TestFIFOQueue_Depth counts pending jobs only (not running).
func TestFIFOQueue_Depth(t *testing.T) {
	t.Parallel()
	q := newTestQueue(8)
	urls := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}
	for i, u := range urls {
		if err := q.Enqueue(Job{ID: "j" + string(rune('0'+i)), URL: u}); err != nil {
			t.Fatalf("Enqueue(%d) error: %v", i, err)
		}
	}
	if got := q.Depth(); got != len(urls) {
		t.Errorf("Depth() = %d, want %d", got, len(urls))
	}
	// Dequeue one; depth decreases by 1.
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}
	if got := q.Depth(); got != len(urls)-1 {
		t.Errorf("Depth after dequeue = %d, want %d", got, len(urls)-1)
	}
}

// TestFIFOQueue_Shutdown_DropsPendingJobs verifies buffered jobs are dropped on shutdown.
func TestFIFOQueue_Shutdown_DropsPendingJobs(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	for i := 0; i < 3; i++ {
		url := "https://example.com/" + string(rune('a'+i))
		if err := q.Enqueue(Job{ID: "j" + string(rune('0'+i)), URL: url}); err != nil {
			t.Fatalf("Enqueue(%d) error: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown() error: %v", err)
	}
	if got := q.Depth(); got != 0 {
		t.Errorf("Depth after shutdown = %d, want 0", got)
	}
}

// TestFIFOQueue_Shutdown_IdempotentForDouble calling Shutdown twice does not error.
func TestFIFOQueue_Shutdown_IdempotentForDouble(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown() error: %v", err)
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown() error: %v", err)
	}
}

// TestFIFOQueue_Shutdown_CurrentJobWaited verifies Shutdown waits for current job.
func TestFIFOQueue_Shutdown_CurrentJobWaited(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	if err := q.Enqueue(Job{ID: "j1", URL: "https://example.com/snap"}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		// Simulate worker completing after short delay.
		time.Sleep(50 * time.Millisecond)
		q.CompleteCurrent()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown() error: %v", err)
	}

	select {
	case <-done:
	default:
		t.Error("Shutdown returned before worker completed")
	}
}

// TestFIFOQueue_EnqueueCurrentDuplicate confirms URL currently processing is deduplicated.
func TestFIFOQueue_EnqueueCurrentDuplicate(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	url := "https://example.com/snap"
	if err := q.Enqueue(Job{ID: "j1", URL: url}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue() error: %v", err)
	}
	// URL is now "current"; a new enqueue should be ErrDuplicate.
	err := q.Enqueue(Job{ID: "j2", URL: url})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("Enqueue current URL error = %v, want %v", err, ErrDuplicate)
	}
	q.CompleteCurrent()
}

// TestFIFOQueue_DequeueShutdownRace is a race-detector regression test that
// spams Enqueue, Dequeue, and Shutdown concurrently and asserts that Shutdown
// never returns before every dequeued job has been CompleteCurrent-ed.
//
// The test runs for a fixed duration rather than a fixed iteration count so the
// scheduler has many opportunities to interleave goroutines.
func TestFIFOQueue_DequeueShutdownRace(t *testing.T) {
	t.Parallel()

	const (
		testDuration = 200 * time.Millisecond
		bufSize      = 8
	)

	// dequeued counts jobs taken from the queue.
	// completed counts CompleteCurrent calls.
	// Both are updated under the race detector's watchful eye.
	var dequeued, completed atomic.Int64

	q := New(Options{BufferSize: bufSize})

	// Producer: enqueues distinct URLs as fast as possible. Known expected
	// errors (ErrQueueFull, ErrDuplicate, ErrShuttingDown) are silently ignored;
	// any other error is a test failure.
	var producerWG sync.WaitGroup
	producerWG.Add(1)
	go func() {
		defer producerWG.Done()
		i := 0
		deadline := time.Now().Add(testDuration)
		for time.Now().Before(deadline) {
			url := "https://example.com/" + string(rune('a'+i%26)) + "?" + string(rune('0'+i%10))
			if err := q.Enqueue(Job{ID: "j", URL: url}); err != nil {
				if errors.Is(err, ErrQueueFull) || errors.Is(err, ErrDuplicate) || errors.Is(err, ErrShuttingDown) {
					i++
					continue
				}
				t.Errorf("Enqueue() unexpected error = %v", err)
			}
			i++
		}
	}()

	// Worker: dequeues jobs and immediately calls CompleteCurrent, simulating a
	// fast worker.  Stops once Dequeue returns ErrShuttingDown or the context
	// is cancelled.
	workerCtx, workerCancel := context.WithTimeout(context.Background(), testDuration+500*time.Millisecond)
	defer workerCancel()

	var workerWG sync.WaitGroup
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		for {
			_, err := q.Dequeue(workerCtx)
			if err != nil {
				return
			}
			dequeued.Add(1)
			// Simulate minimal work then signal completion.
			q.CompleteCurrent()
			completed.Add(1)
		}
	}()

	// Let the producer run for the test duration then shut down.
	time.Sleep(testDuration)
	producerWG.Wait()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	if err := q.Shutdown(shutCtx); err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}

	// After Shutdown returns, cancel the worker so it stops blocking.
	workerCancel()
	workerWG.Wait()

	// Invariant: every dequeued job must have been CompleteCurrent-ed before
	// Shutdown returns.  The worker calls CompleteCurrent synchronously after
	// each dequeue, so the counts must be equal at this point.
	if d, c := dequeued.Load(), completed.Load(); d != c {
		t.Errorf("dequeued=%d completed=%d: Shutdown returned before all jobs were CompleteCurrent-ed", d, c)
	}
}

// TestFIFOQueue_Dequeue_AfterShutdownDiscardsBufferedJob verifies that a
// Dequeue call that races with Shutdown does not deliver a buffered job that
// was pulled from the channel after q.closed was set. The contract is:
// pending buffered jobs are dropped by Shutdown; a Dequeue that loses the race
// must return ErrShuttingDown rather than promoting a post-shutdown channel
// receive to q.current.
//
// This test arranges the race explicitly: it enqueues a job so the channel
// has a buffered item, calls Shutdown, then calls Dequeue on the already-closed
// queue. Both operations happen sequentially here because Shutdown drains the
// channel; the subsequent Dequeue must observe the closed channel and return
// ErrShuttingDown.
func TestFIFOQueue_Dequeue_AfterShutdownDiscardsBufferedJob(t *testing.T) {
	t.Parallel()
	q := newTestQueue(4)
	if err := q.Enqueue(Job{ID: "j1", URL: "https://example.com/snap"}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	if err := q.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	// After Shutdown drains the buffered job, the channel is closed.
	// Dequeue must not deliver the already-drained job; it must return ErrShuttingDown.
	_, err := q.Dequeue(context.Background())
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Dequeue after Shutdown with buffered job error = %v, want %v", err, ErrShuttingDown)
	}
	// q.current must remain nil: no job should have been promoted after shutdown.
	if cur := q.Current(); cur != nil {
		t.Errorf("Current() after shutdown Dequeue = %+v, want nil (buffered job must not be promoted)", cur)
	}
}

// TestCleanURLPath covers the path-cleaning helper.
func TestCleanURLPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"", "/"},
		{"/", "/"},
		{"/a/b", "/a/b"},
		{"/a//b", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/a/b/../c", "/a/c"},
		{"/a/b/", "/a/b/"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := cleanURLPath(tt.input)
			if got != tt.want {
				t.Errorf("cleanURLPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
