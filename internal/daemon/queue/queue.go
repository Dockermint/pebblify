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
	"strconv"
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
// strips the port when it matches the default for the scheme. The set mirrors
// allowedSchemes so every scheme that survives validation has a documented
// default port.
var defaultSchemePorts = map[string]string{
	"http":  "80",
	"https": "443",
}

// allowedSchemes is the whitelist of URL schemes the daemon accepts as job
// sources. Only HTTP and HTTPS are admitted because runner.download uses
// net/http; admitting ftp/sftp/ssh here would pass canonicalization only to
// fail at download time with an opaque "unsupported protocol" error. Any
// other scheme (javascript, file, data, ...) is rejected here so dangerous
// URLs never reach the runner.
var allowedSchemes = map[string]struct{}{
	"http":  {},
	"https": {},
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
	// CompleteCurrent clears the current-job slot so Shutdown can return and
	// Current reports nil until the next Dequeue. Implementations must be
	// idempotent.
	CompleteCurrent()
	// Shutdown closes the queue to new submissions and blocks until either the
	// in-flight job completes or ctx is cancelled. Pending (not-yet-started)
	// jobs are dropped; the returned error is ctx.Err() if the wait times out.
	// Subsequent calls do not re-close the queue but still wait for any
	// remaining in-flight work under the new ctx, so callers can supply a
	// fresh deadline on a second shutdown attempt.
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
//
// A sync.Cond (cond) attached to mu lets waitForCurrent block until either the
// in-flight slot clears or a dequeue handoff completes, without polling. The
// handoff counter tracks receives from the channel that have not yet been
// reflected in current; Shutdown treats (handoff > 0 || current != nil) as
// in-flight, closing the race where the worker has pulled a job off ch but has
// not yet acquired mu to mark it current.
type FIFOQueue struct {
	ch      chan Job
	logger  *slog.Logger
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]struct{} // canonical URL -> {} for pending jobs
	current *Job                // nil when idle
	handoff int                 // receives in flight but not yet marked current
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
	q := &FIFOQueue{
		ch:      make(chan Job, size),
		logger:  logger,
		pending: make(map[string]struct{}),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Enqueue implements Queue.
//
// The non-blocking channel send is performed while q.mu is held. This is safe
// because Shutdown also acquires q.mu before calling close(q.ch), so the two
// operations cannot race; the default branch below guarantees Enqueue never
// blocks while holding the mutex.
func (q *FIFOQueue) Enqueue(job Job) error {
	canonical, err := Canonicalize(job.URL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrShuttingDown
	}
	if _, dup := q.pending[canonical]; dup {
		return ErrDuplicate
	}
	if q.current != nil {
		if currentCanonical, cErr := Canonicalize(q.current.URL); cErr == nil && currentCanonical == canonical {
			return ErrDuplicate
		}
	}

	select {
	case q.ch <- job:
		q.pending[canonical] = struct{}{}
		q.logger.Info("queue enqueue", "job_id", job.ID, "depth", len(q.ch))
		return nil
	default:
		return ErrQueueFull
	}
}

// Dequeue implements Queue. The returned job is marked current and removed
// from the pending dedup set; the worker must call CompleteCurrent when the
// job finishes (success or failure) to clear the current slot.
//
// A handoff counter is incremented before the blocking receive so Shutdown
// observes the receiver as "in flight" even in the window between a successful
// channel receive and the subsequent mutex acquisition. If ctx fires or the
// channel is closed before a job is delivered, the counter is decremented and
// waitForCurrent is signalled so Shutdown can make progress.
func (q *FIFOQueue) Dequeue(ctx context.Context) (Job, error) {
	q.mu.Lock()
	q.handoff++
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.releaseHandoff(nil)
		return Job{}, ctx.Err()
	case job, ok := <-q.ch:
		if !ok {
			q.releaseHandoff(nil)
			return Job{}, ErrShuttingDown
		}
		jobCopy := job
		if !q.releaseHandoff(&jobCopy) {
			// Shutdown closed the queue between channel receive and handoff;
			// drop the job so pending buffered items never promote to current
			// after the Shutdown contract is already in effect.
			q.logger.Warn("queue job dropped on shutdown handoff",
				"job_id", job.ID, "url", RedactURL(job.URL))
			return Job{}, ErrShuttingDown
		}
		return job, nil
	}
}

// releaseHandoff decrements the handoff counter and either marks current (when
// current is non-nil and the queue is still open) or leaves the slot idle. The
// condition variable is broadcast in all cases so waitForCurrent revisits its
// predicate. The canonical URL is removed from pending when a job was handed
// off, regardless of whether it was promoted to current, so the dedup map does
// not retain stale entries. The return value reports whether the job was
// actually promoted to current: false means the queue was closed while the
// receiver was in flight and the caller must treat the job as dropped.
func (q *FIFOQueue) releaseHandoff(job *Job) bool {
	q.mu.Lock()
	q.handoff--
	delivered := false
	if job != nil {
		if canonical, err := Canonicalize(job.URL); err == nil {
			delete(q.pending, canonical)
		}
		if !q.closed {
			q.current = job
			delivered = true
		}
	}
	q.cond.Broadcast()
	q.mu.Unlock()
	return delivered
}

// CompleteCurrent clears the current job slot and signals any goroutine
// blocked in waitForCurrent so Shutdown can return promptly.
func (q *FIFOQueue) CompleteCurrent() {
	q.mu.Lock()
	q.current = nil
	q.cond.Broadcast()
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
// logged as dropped. The call then blocks until every in-flight receive has
// either been marked current and subsequently completed, or the handoff has
// been released on a ctx/close path. ctx cancellation during the wait returns
// ctx.Err() without forcing the in-flight job to stop.
//
// Shutdown is idempotent: a second call does not re-close the channel but
// still waits for any remaining in-flight work under the supplied ctx, so
// operators can retry with a fresh deadline after a timeout on the first call.
func (q *FIFOQueue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return q.waitForCurrent(ctx)
	}
	q.closed = true
	close(q.ch)
	q.cond.Broadcast()
	q.mu.Unlock()

	q.drainPending()

	return q.waitForCurrent(ctx)
}

// drainPending removes any buffered jobs left in the channel after close and
// logs each one as dropped. Called with the queue already marked closed so no
// new jobs can be pushed. Logged URLs are redacted so userinfo, query strings,
// and fragments that may carry secrets are never persisted to logs.
func (q *FIFOQueue) drainPending() {
	for job := range q.ch {
		canonical, err := Canonicalize(job.URL)
		if err != nil {
			q.logger.Warn("queue job had invalid url while draining",
				"job_id", job.ID, "url", RedactURL(job.URL), "error", err)
		}
		q.mu.Lock()
		if err == nil {
			delete(q.pending, canonical)
		}
		q.mu.Unlock()
		q.logger.Warn("queue job dropped on shutdown",
			"job_id", job.ID, "url", RedactURL(job.URL))
	}
}

// waitForCurrent blocks until both q.current is nil and q.handoff is zero, or
// ctx fires. The condition variable is broadcast by releaseHandoff and
// CompleteCurrent so the happy path is event-driven; a short ticker runs in a
// helper goroutine solely to wake the wait on ctx cancellation without
// requiring callers of CompleteCurrent to know about ctx.
func (q *FIFOQueue) waitForCurrent(ctx context.Context) error {
	done := make(chan struct{})
	defer close(done)

	// Bridge ctx.Done into a cond broadcast so the waiter unblocks promptly
	// on cancellation without the per-caller ticker that the polling version
	// needed. A single goroutine suffices since Shutdown is called at most
	// once per queue instance.
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for q.current != nil || q.handoff > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.cond.Wait()
	}
	return ctx.Err()
}

// RedactURL returns a log-safe form of raw with userinfo, query string, and
// fragment stripped. On parse failure the placeholder "[invalid-url]" is
// returned so logs and notifications never leak attacker-supplied payload
// fragments verbatim. Consumers must prefer this helper over logging raw job
// URLs.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
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
	if _, ok := allowedSchemes[scheme]; !ok {
		return "", fmt.Errorf("unsupported url scheme %q: only http and https are supported", scheme)
	}
	// url.Parse populates u.Host with "host:port" form, so a URL like
	// "http://:8080" has a non-empty u.Host ":8080" yet an empty hostname. Guard
	// against that so hostless URLs are rejected here instead of silently
	// canonicalizing to "http://:8080/".
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("url must have non-empty host")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid port %q: must be 1-65535", port)
		}
	}
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
