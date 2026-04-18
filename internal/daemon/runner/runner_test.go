package runner

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/notify"
	"github.com/Dockermint/Pebblify/internal/daemon/queue"
)

// ---- fakes ----

// fakeQueue implements queue.Queue backed by a channel.
type fakeQueue struct {
	jobs    chan queue.Job
	current *queue.Job
	depth   int
	closed  bool
}

func newFakeQueue(buf int) *fakeQueue {
	return &fakeQueue{jobs: make(chan queue.Job, buf)}
}

func (q *fakeQueue) Enqueue(job queue.Job) error {
	if q.closed {
		return queue.ErrShuttingDown
	}
	select {
	case q.jobs <- job:
		q.depth++
		return nil
	default:
		return queue.ErrQueueFull
	}
}

func (q *fakeQueue) Dequeue(ctx context.Context) (queue.Job, error) {
	select {
	case <-ctx.Done():
		return queue.Job{}, ctx.Err()
	case job, ok := <-q.jobs:
		if !ok {
			return queue.Job{}, queue.ErrShuttingDown
		}
		if q.depth > 0 {
			q.depth--
		}
		jobCopy := job
		q.current = &jobCopy
		return job, nil
	}
}

func (q *fakeQueue) Depth() int             { return q.depth }
func (q *fakeQueue) Contains(_ string) bool  { return false }
func (q *fakeQueue) Current() *queue.Job {
	if q.current == nil {
		return nil
	}
	c := *q.current
	return &c
}
func (q *fakeQueue) Shutdown(_ context.Context) error {
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.jobs)
	return nil
}
func (q *fakeQueue) CompleteCurrent() { q.current = nil }

// fakeNotifier records Notify calls.
type fakeNotifier struct {
	events []notify.Event
	err    error
}

func (n *fakeNotifier) Notify(_ context.Context, ev notify.Event) error {
	n.events = append(n.events, ev)
	return n.err
}

// minimalConfig returns a Config suitable for runner construction in tests.
func minimalConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Convertion: config.ConvertionSection{
			TemporaryDirectory: t.TempDir(),
		},
		Save: config.SaveSection{
			Compression: "lz4",
		},
	}
}

// ---- urlBasename ----

// TestURLBasename_Table covers all url-basename extraction cases.
func TestURLBasename_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/snap.tar.lz4", "snap.tar.lz4"},
		{"https://example.com/a/b/snap.tar", "snap.tar"},
		{"https://example.com/snap.tar?key=1", "snap.tar"},
		{"https://example.com/snap.tar#frag", "snap.tar"},
		{"https://example.com/", ""},
		{"https://example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := urlBasename(tt.input)
			if got != tt.want {
				t.Errorf("urlBasename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- archiveStem ----

// TestArchiveStem_Table strips known archive extensions.
func TestArchiveStem_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"snap.tar.gz", "snap"},
		{"snap.tar.zst", "snap"},
		{"snap.tar.lz4", "snap"},
		{"snap.tar", "snap"},
		{"snap.zip", "snap"},
		{"snap.db", "snap.db"},
		{"snap", "snap"},
		{"SNAP.TAR.GZ", "SNAP"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := archiveStem(tt.input)
			if got != tt.want {
				t.Errorf("archiveStem(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- hasDBChildren ----

// TestHasDBChildren_WithDBSubdirectory returns true when .db dir present.
func TestHasDBChildren_WithDBSubdirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/blockstore.db", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !hasDBChildren(dir) {
		t.Error("hasDBChildren = false, want true")
	}
}

// TestHasDBChildren_WithoutDBSubdirectory returns false when no .db dir.
func TestHasDBChildren_WithoutDBSubdirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/somedir", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if hasDBChildren(dir) {
		t.Error("hasDBChildren = true, want false")
	}
}

// TestHasDBChildren_EmptyDirectory returns false.
func TestHasDBChildren_EmptyDirectory(t *testing.T) {
	t.Parallel()
	if hasDBChildren(t.TempDir()) {
		t.Error("hasDBChildren(empty) = true, want false")
	}
}

// TestHasDBChildren_FileNotDir a .db entry that is a file, not dir, is not counted.
func TestHasDBChildren_FileNotDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a regular file named *.db — should NOT be considered a DB dir.
	if err := os.WriteFile(dir+"/data.db", []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasDBChildren(dir) {
		t.Error("hasDBChildren with file .db entry = true, want false")
	}
}

// ---- findLevelDBRoot ----

// TestFindLevelDBRoot_FindsDBDir returns the parent dir of a .db child.
func TestFindLevelDBRoot_FindsDBDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbParent := root + "/cosmos"
	if err := os.MkdirAll(dbParent+"/blockstore.db", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := findLevelDBRoot(root)
	if err != nil {
		t.Fatalf("findLevelDBRoot() error: %v", err)
	}
	if got != dbParent {
		t.Errorf("findLevelDBRoot() = %q, want %q", got, dbParent)
	}
}

// TestFindLevelDBRoot_NotFound returns ErrNoLevelDB.
func TestFindLevelDBRoot_NotFound(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := findLevelDBRoot(root)
	if !errors.Is(err, ErrNoLevelDB) {
		t.Errorf("findLevelDBRoot() error = %v, want %v", err, ErrNoLevelDB)
	}
}

// ---- Runner.Start/Stop lifecycle ----

// TestRunner_Start_StopsOnContextCancel returns nil when ctx is cancelled.
func TestRunner_Start_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	fq := newFakeQueue(4)
	r := New(Deps{
		Cfg:      minimalConfig(t),
		Queue:    fq,
		Notifier: &fakeNotifier{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after context cancel")
	}
}

// TestRunner_Start_StopsOnQueueShutdown returns nil when queue is shut down.
func TestRunner_Start_StopsOnQueueShutdown(t *testing.T) {
	t.Parallel()
	fq := newFakeQueue(4)
	r := New(Deps{
		Cfg:      minimalConfig(t),
		Queue:    fq,
		Notifier: &fakeNotifier{},
	})

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = fq.Shutdown(shutCtx)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() queue shutdown error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after queue shutdown")
	}
}

// TestRunner_Stop_AfterContextCancel waits for Start to exit.
func TestRunner_Stop_AfterContextCancel(t *testing.T) {
	t.Parallel()
	fq := newFakeQueue(4)
	r := New(Deps{
		Cfg:      minimalConfig(t),
		Queue:    fq,
		Notifier: &fakeNotifier{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.Start(ctx) }()

	// Give Start a moment to block on Dequeue.
	time.Sleep(10 * time.Millisecond)
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := r.Stop(stopCtx); err != nil {
		t.Errorf("Stop() error = %v", err)
	}
}

// TestRunner_Stop_Idempotent calling Stop twice returns nil both times.
func TestRunner_Stop_Idempotent(t *testing.T) {
	t.Parallel()
	fq := newFakeQueue(4)
	r := New(Deps{
		Cfg:      minimalConfig(t),
		Queue:    fq,
		Notifier: &fakeNotifier{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	_ = r.Stop(stopCtx)
	if err := r.Stop(stopCtx); err != nil {
		t.Errorf("second Stop() error = %v", err)
	}
}
