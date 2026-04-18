// Package queue implements the in-memory FIFO job queue consumed by the
// daemon's single worker goroutine.
//
// The queue canonicalizes snapshot URLs to detect duplicates. A URL is
// considered duplicate while it is pending or running; completed or failed
// jobs are not tracked for dedup purposes. The queue is unbounded in count
// but bounded by a configurable buffer; a full buffer returns ErrQueueFull
// so callers may translate that to HTTP 503.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors returned by Queue operations.
var (
	// ErrDuplicate indicates the job's canonicalized URL is already running or queued.
	ErrDuplicate = errors.New("duplicate job: url already running or queued")
	// ErrShuttingDown indicates the queue is closed to new submissions.
	ErrShuttingDown = errors.New("daemon shutting down")
	// ErrQueueFull indicates the bounded buffer cannot accept another job.
	ErrQueueFull = errors.New("queue full")
	// ErrInvalidURL indicates the job URL failed canonicalization.
	ErrInvalidURL = errors.New("invalid job url")
)

// defaultSchemePorts maps URL schemes to their default TCP port. Canonicalization
// strips the port when it matches the default for the scheme.
var defaultSchemePorts = map[string]string{
	"http":  "80",
	"https": "443",
	"ftp":   "21",
	"ssh":   "22",
	"sftp":  "22",
}

// Job is a single conversion request accepted from the API.
type Job struct {
	// ID is a daemon-assigned opaque identifier (the API handler generates this,
	// typically a UUID v4 string).
	ID string
	// URL is the snapshot archive URL as submitted by the client.
	URL string
	// SubmittedAt is the wall-clock time the enqueue succeeded.
	SubmittedAt time.Time
}

// Queue is the contract between the API handler and the worker goroutine.
type Queue interface {
	// Enqueue admits job if its canonicalized URL is not already pending or
	// running and the buffer has capacity. Returns ErrDuplicate, ErrQueueFull,
	// or ErrShuttingDown on the corresponding rejection paths.
	Enqueue(job Job) error
	// Dequeue blocks until a job is available, ctx is cancelled, or the queue
	// is shut down. Returns ctx.Err() or ErrShuttingDown otherwise.
	Dequeue(ctx context.Context) (Job, error)
	// Depth returns the number of jobs waiting in the buffer, not counting a
	// job currently being processed.
	Depth() int
	// Contains reports whether the canonicalization of rawURL matches a pending
	// or running job.
	Contains(rawURL string) bool
	// Current returns a copy of the job currently being processed, or nil if
	// idle.
	Current() *Job
	// Shutdown closes the queue to new submissions and blocks until either the
	// in-flight job completes or ctx is cancelled. Pending (not-yet-started)
	// jobs are dropped; the returned error is ctx.Err() if the wait times out.
	Shutdown(ctx context.Context) error
}

// Options configures queue construction.
type Options struct {
	// BufferSize is the bounded FIFO capacity. Must be >= 1.
	BufferSize int
	// Logger receives structured log events. A nil logger is replaced by
	// slog.Default().
	Logger *slog.Logger
}

// FIFOQueue is a Queue backed by a buffered channel and a map of canonical
// URLs for dedup lookups. All exported methods are safe for concurrent use.
type FIFOQueue struct {
	ch      chan Job
	logger  *slog.Logger
	mu      sync.Mutex
	pending map[string]struct{} // canonical URL -> {} for pending jobs
	current *Job                // nil when idle
	closed  bool
}

// New returns an initialized FIFOQueue. BufferSize is clamped to a minimum of 1.
func New(opts Options) *FIFOQueue {
	size := opts.BufferSize
	if size < 1 {
		size = 1
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &FIFOQueue{
		ch:      make(chan Job, size),
		logger:  logger,
		pending: make(map[string]struct{}),
	}
}

// Enqueue implements Queue.
func (q *FIFOQueue) Enqueue(job Job) error {
	canonical, err := Canonicalize(job.URL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrShuttingDown
	}
	if _, dup := q.pending[canonical]; dup {
		q.mu.Unlock()
		return ErrDuplicate
	}
	if q.current != nil {
		if currentCanonical, cErr := Canonicalize(q.current.URL); cErr == nil && currentCanonical == canonical {
			q.mu.Unlock()
			return ErrDuplicate
		}
	}
	q.pending[canonical] = struct{}{}
	q.mu.Unlock()

	select {
	case q.ch <- job:
		q.logger.Info("queue enqueue", "job_id", job.ID, "depth", len(q.ch))
		return nil
	default:
		q.mu.Lock()
		delete(q.pending, canonical)
		q.mu.Unlock()
		return ErrQueueFull
	}
}

// Dequeue implements Queue. The returned job is marked current and removed from
// the pending dedup set; the worker must call CompleteCurrent when the job
// finishes (success or failure) to clear the current slot.
func (q *FIFOQueue) Dequeue(ctx context.Context) (Job, error) {
	select {
	case <-ctx.Done():
		return Job{}, ctx.Err()
	case job, ok := <-q.ch:
		if !ok {
			return Job{}, ErrShuttingDown
		}
		canonical, _ := Canonicalize(job.URL)
		q.mu.Lock()
		delete(q.pending, canonical)
		jobCopy := job
		q.current = &jobCopy
		q.mu.Unlock()
		return job, nil
	}
}

// CompleteCurrent clears the current job slot. The worker calls this after
// finishing (or failing) the job returned from Dequeue so Current() reports nil
// until the next dequeue.
func (q *FIFOQueue) CompleteCurrent() {
	q.mu.Lock()
	q.current = nil
	q.mu.Unlock()
}

// Depth implements Queue.
func (q *FIFOQueue) Depth() int {
	return len(q.ch)
}

// Contains implements Queue. A canonicalization error causes Contains to
// return false, mirroring the Enqueue rejection path for invalid URLs.
func (q *FIFOQueue) Contains(rawURL string) bool {
	canonical, err := Canonicalize(rawURL)
	if err != nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[canonical]; ok {
		return true
	}
	if q.current != nil {
		if currentCanonical, cErr := Canonicalize(q.current.URL); cErr == nil && currentCanonical == canonical {
			return true
		}
	}
	return false
}

// Current implements Queue.
func (q *FIFOQueue) Current() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		return nil
	}
	c := *q.current
	return &c
}

// Shutdown implements Queue. Once invoked, further Enqueue calls return
// ErrShuttingDown. Pending buffered jobs are drained from the channel and
// logged as dropped. The call then blocks until the in-flight job (if any)
// clears via CompleteCurrent or ctx is cancelled.
func (q *FIFOQueue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	close(q.ch)
	q.mu.Unlock()

	q.drainPending()

	return q.waitForCurrent(ctx)
}

// drainPending removes any buffered jobs left in the channel after close and
// logs each one as dropped. Called with the queue already marked closed so no
// new jobs can be pushed.
func (q *FIFOQueue) drainPending() {
	for job := range q.ch {
		canonical, _ := Canonicalize(job.URL)
		q.mu.Lock()
		delete(q.pending, canonical)
		q.mu.Unlock()
		q.logger.Warn("queue job dropped on shutdown", "job_id", job.ID, "url", job.URL)
	}
}

// waitForCurrent polls the current-job slot with a short ticker until the
// worker clears it or ctx is done.
func (q *FIFOQueue) waitForCurrent(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		q.mu.Lock()
		done := q.current == nil
		q.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Canonicalize returns a stable string form of rawURL for duplicate detection.
//
// The transformation lowercases scheme and host, strips the default port for
// the scheme, cleans the path via path.Clean semantics, sorts query keys then
// values, and discards the fragment. Authentication info (userinfo) is also
// stripped since it does not address the resource.
func Canonicalize(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("empty url")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("url must have scheme and host")
	}

	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if def, ok := defaultSchemePorts[scheme]; ok && port == def {
		port = ""
	}

	cleanedPath := cleanURLPath(u.EscapedPath())

	canonicalQuery := sortedQuery(u.Query())

	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	if port != "" {
		b.WriteByte(':')
		b.WriteString(port)
	}
	b.WriteString(cleanedPath)
	if canonicalQuery != "" {
		b.WriteByte('?')
		b.WriteString(canonicalQuery)
	}
	return b.String(), nil
}

// cleanURLPath normalizes an escaped URL path. An empty path becomes "/"; any
// other path is passed through path.Clean-equivalent rules while preserving
// escaping.
func cleanURLPath(p string) string {
	if p == "" {
		return "/"
	}
	// Collapse duplicate slashes and resolve . / .. segments manually to keep
	// escaped characters untouched.
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch seg {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, seg)
		}
	}
	cleaned := "/" + strings.Join(out, "/")
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// sortedQuery encodes v with keys sorted alphabetically and each key's values
// sorted lexicographically. The result is suitable for concatenation after a
// leading '?' to form a canonical URL.
func sortedQuery(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		values := append([]string(nil), v[k]...)
		sort.Strings(values)
		ek := url.QueryEscape(k)
		for _, vv := range values {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(ek)
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(vv))
		}
	}
	return b.String()
}
