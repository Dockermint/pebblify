// Package runner is the daemon job orchestrator.
//
// It owns the single worker goroutine that consumes queue.Job values, runs
// the full pipeline (download -> extract -> convert -> verify -> repack ->
// upload -> cleanup -> notify), and emits structured slog events at every
// boundary.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/notify"
	"github.com/Dockermint/Pebblify/internal/daemon/queue"
	"github.com/Dockermint/Pebblify/internal/daemon/repack"
	"github.com/Dockermint/Pebblify/internal/daemon/store"
	"github.com/Dockermint/Pebblify/internal/daemon/telemetry"
	"github.com/Dockermint/Pebblify/internal/fsutil"
	"github.com/Dockermint/Pebblify/internal/migration"
	"github.com/Dockermint/Pebblify/internal/verify"
)

// Pipeline timings and tunables.
const (
	// downloadTimeout bounds an individual archive download.
	downloadTimeout = 10 * time.Minute
	// downloadProgressInterval controls how often slog receives download
	// progress telemetry during a long transfer.
	downloadProgressInterval = 5 * time.Second
	// diskSafetyFactor is the multiplier applied to the archive size to
	// estimate peak scratch-space consumption (download + extract + repack).
	diskSafetyFactor = 3
	// migrationWorkers is the fixed worker count for internal/migration
	// calls from the daemon; 0 lets the migration package pick a default
	// based on runtime.NumCPU().
	migrationWorkers = 0
	// migrationBatchMemoryMB mirrors the level-to-pebble CLI default so the
	// daemon has the same memory budget.
	migrationBatchMemoryMB = 512
	// verifySamplePercent is the sampling rate passed to internal/verify. 0
	// forces a full scan, matching the spec "no sampling skip in daemon".
	verifySamplePercent = 0
	// workspaceCleanupGrace caps how long cleanupWorkspace waits for any
	// background goroutine spawned by convert/verify to release its file
	// handles after ctx cancellation. The wait prevents os.RemoveAll from
	// racing with an open PebbleDB, which would otherwise return EBUSY on
	// Linux and permission-denied on Windows. A bounded timeout keeps the
	// daemon responsive even when the leaked goroutine stalls permanently.
	workspaceCleanupGrace = 30 * time.Second
)

// Sentinel errors returned by the runner.
var (
	// ErrFatal wraps an error that the runner considers unrecoverable for
	// the daemon process (distinct from a per-job failure). Callers that
	// receive this from Start should exit 1.
	ErrFatal = errors.New("runner fatal")
	// ErrNoLevelDB indicates the extracted archive did not contain a
	// recognisable LevelDB tree.
	ErrNoLevelDB = errors.New("no leveldb directory found in extracted archive")
)

// Runner is the contract between the daemon entrypoint and the orchestrator.
type Runner interface {
	// Start blocks, processing jobs until ctx is cancelled or a fatal error
	// occurs. A nil return means clean shutdown; a non-nil return wraps
	// ErrFatal.
	Start(ctx context.Context) error
	// Stop signals the worker to finish the in-flight job and return. It
	// returns when the worker has exited or ctx is cancelled.
	Stop(ctx context.Context) error
}

// Deps groups the runner's collaborators so the constructor has a small
// argument list and the orchestration is trivially testable via interfaces.
type Deps struct {
	// Cfg is the parsed daemon configuration.
	Cfg *config.Config
	// Secrets is the env-sourced secrets bundle; held only for downstream
	// factories, the runner never reads fields directly.
	Secrets config.Secrets
	// Queue supplies jobs.
	Queue queue.Queue
	// Notifier delivers lifecycle events.
	Notifier notify.Notifier
	// Targets is the fan-out of upload destinations.
	Targets []store.Target
	// Logger is the structured logger; nil means slog.Default.
	Logger *slog.Logger
	// HTTPClient downloads snapshot archives. A nil client uses a default
	// with the download timeout applied per request via ctx.
	HTTPClient *http.Client
	// Collectors receives pipeline stage observations. A nil value disables
	// metric emission; the methods on *telemetry.Collectors are nil-safe.
	Collectors *telemetry.Collectors
}

// jobRunner is the concrete Runner implementation.
type jobRunner struct {
	deps   Deps
	logger *slog.Logger
	http   *http.Client

	mu      sync.Mutex
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New constructs a Runner from deps.
func New(deps Deps) Runner {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &jobRunner{
		deps:   deps,
		logger: logger,
		http:   client,
		done:   make(chan struct{}),
	}
}

// Start implements Runner. A child context is derived from ctx so Stop can
// signal the blocked Dequeue call without waiting on the parent context
// (which is typically the daemon-wide SIGTERM context that drainDaemon
// replaces with a bounded shutdownCtx before calling Stop).
func (r *jobRunner) Start(ctx context.Context) error {
	defer close(r.done)

	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer cancel()

	for {
		if err := runCtx.Err(); err != nil {
			return nil
		}
		job, err := r.deps.Queue.Dequeue(runCtx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if errors.Is(err, queue.ErrShuttingDown) {
				return nil
			}
			return fmt.Errorf("%w: dequeue: %v", ErrFatal, err)
		}

		r.processJob(runCtx, job)
	}
}

// Stop implements Runner. It cancels the runner's internal context so the
// blocked Dequeue call unblocks, then waits for Start to return. Callers
// that hold a separate queue.Shutdown lifecycle should invoke that first so
// the in-flight job is allowed to finish before Stop forces the loop to exit.
func (r *jobRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	cancel := r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processJob runs the full pipeline for a single job. Fatal and non-fatal
// errors are both demoted to per-job failure events; the daemon never exits
// on a job error.
func (r *jobRunner) processJob(ctx context.Context, job queue.Job) {
	defer func() {
		r.deps.Queue.CompleteCurrent()
		r.deps.Collectors.RecordDequeue(r.deps.Queue.Depth())
	}()

	start := time.Now()
	r.deps.Collectors.RecordJobStart()
	r.deps.Collectors.RecordDequeue(r.deps.Queue.Depth())

	redactedURL := redactedJobURL(job.URL)
	logger := r.logger.With("job_id", job.ID, "url", redactedURL)
	logger.Info("job started")

	if err := r.safeNotify(ctx, notify.Event{
		Kind:   notify.EventStarted,
		JobID:  job.ID,
		JobURL: redactedURL,
	}); err != nil {
		logger.Warn("notify started failed", "error", err)
	}

	workspace, err := r.prepareWorkspace(job.ID)
	if err != nil {
		r.failJob(ctx, logger, job, err)
		r.deps.Collectors.RecordJobEnd(time.Since(start), false)
		return
	}
	defer r.cleanupWorkspace(logger, workspace)

	if err := r.runPipeline(ctx, logger, job, workspace); err != nil {
		r.failJob(ctx, logger, job, err)
		r.deps.Collectors.RecordJobEnd(time.Since(start), false)
		return
	}

	if err := r.safeNotify(ctx, notify.Event{
		Kind:   notify.EventCompleted,
		JobID:  job.ID,
		JobURL: redactedURL,
	}); err != nil {
		logger.Warn("notify completed failed", "error", err)
	}
	logger.Info("job completed")
	r.deps.Collectors.RecordJobEnd(time.Since(start), true)
}

// runPipeline executes steps 2..10 of the daemon job pipeline defined in
// docs/specs/daemon-mode.md. Any returned error is treated as a non-fatal
// per-job failure by the caller.
func (r *jobRunner) runPipeline(ctx context.Context, logger *slog.Logger,
	job queue.Job, ws *workspace) error {
	archivePath, archiveSize, err := r.download(ctx, logger, job.URL, ws.srcDir)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := r.ensureDiskBudget(ws.root, archiveSize); err != nil {
		return err
	}

	if err := repack.Extract(ctx, archivePath, ws.extractedDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	levelDir, err := findLevelDBRoot(ws.extractedDir)
	if err != nil {
		return err
	}

	if err := r.convert(ctx, logger, ws, levelDir); err != nil {
		return fmt.Errorf("convert: %w", err)
	}

	if err := r.verify(ctx, logger, ws, levelDir); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	if err := r.replaceDBTree(levelDir, ws.pebbleDir); err != nil {
		return fmt.Errorf("replace db tree: %w", err)
	}

	mode := store.Compression(r.deps.Cfg.Save.Compression)
	archiveOut := r.outputArchivePath(ws.outDir, job, archivePath, mode)
	if err := repack.Compress(ctx, ws.extractedDir, archiveOut, mode); err != nil {
		return fmt.Errorf("repack: %w", err)
	}

	if err := r.fanOutUploads(ctx, logger, archiveOut); err != nil {
		return err
	}
	return nil
}

// safeNotify swallows the context error into a log-only event so notify
// failures never bubble up as job failures.
func (r *jobRunner) safeNotify(ctx context.Context, event notify.Event) error {
	if r.deps.Notifier == nil {
		return nil
	}
	return r.deps.Notifier.Notify(ctx, event)
}

// failJob emits a Failed notification and logs the error. The error is routed
// exclusively through slog so operators receive the structured payload
// (handler-specific formatting, log correlation) and no raw stderr write can
// re-introduce the URL leak that redactedJobURL guards against.
func (r *jobRunner) failJob(ctx context.Context, logger *slog.Logger,
	job queue.Job, cause error) {
	logger.Error("job failed", "error", cause)
	if err := r.safeNotify(ctx, notify.Event{
		Kind:   notify.EventFailed,
		JobID:  job.ID,
		JobURL: redactedJobURL(job.URL),
		Error:  cause,
	}); err != nil {
		logger.Warn("notify failed failed", "error", err)
	}
}

// workspace groups the scratch directories created for a single job.
//
// bgTasks tracks background goroutines spawned by convert and verify. The
// internal/migration and internal/verify APIs do not yet accept a context, so
// cancellation from Stop abandons the work goroutine while it still holds
// file handles on the PebbleDB output. cleanupWorkspace must wait on bgTasks
// with a bounded timeout before removing the scratch tree to avoid racing
// the leaked goroutine on Windows and EBUSY paths on Linux.
type workspace struct {
	root         string
	srcDir       string
	extractedDir string
	pebbleDir    string
	outDir       string
	migrationTmp string
	bgTasks      sync.WaitGroup
}

// prepareWorkspace creates <tmp>/<job_id>/{src,extracted,pebbledb,out,migration}.
func (r *jobRunner) prepareWorkspace(jobID string) (*workspace, error) {
	root := filepath.Join(r.deps.Cfg.Conversion.TemporaryDirectory, jobID)
	ws := &workspace{
		root:         root,
		srcDir:       filepath.Join(root, "src"),
		extractedDir: filepath.Join(root, "extracted"),
		pebbleDir:    filepath.Join(root, "pebbledb"),
		outDir:       filepath.Join(root, "out"),
		migrationTmp: filepath.Join(root, "migration"),
	}
	for _, d := range []string{ws.srcDir, ws.extractedDir, ws.pebbleDir, ws.outDir, ws.migrationTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("workspace mkdir %s: %w", d, err)
		}
	}
	return ws, nil
}

// cleanupWorkspace removes the job scratch tree. Errors are logged, not
// returned, because cleanup must run on every exit path.
//
// Any background goroutine still holding file handles (convert or verify that
// was abandoned on ctx cancellation) is awaited up to workspaceCleanupGrace
// before removal. If the grace period elapses, cleanup proceeds anyway and the
// lingering handles turn into a bounded partial-cleanup warning rather than an
// indefinite leak. The timer goroutine cleanly exits on either path.
func (r *jobRunner) cleanupWorkspace(logger *slog.Logger, ws *workspace) {
	if ws == nil {
		return
	}
	if err := waitWithTimeout(&ws.bgTasks, workspaceCleanupGrace); err != nil {
		logger.Warn("cleanup workspace waited for background goroutines",
			"path", ws.root, "grace", workspaceCleanupGrace, "error", err)
	}
	if err := os.RemoveAll(ws.root); err != nil {
		logger.Warn("cleanup workspace failed", "path", ws.root, "error", err)
	}
}

// waitWithTimeout blocks until wg.Wait returns or d elapses. Returns a
// non-nil error only when the timeout fires; callers should treat a timeout
// as a best-effort cleanup advisory rather than a fatal condition.
func waitWithTimeout(wg *sync.WaitGroup, d time.Duration) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("background goroutines did not finish within %s", d)
	}
}

// ensureDiskBudget checks that diskSafetyFactor * archiveSize bytes are
// available in the workspace root. Zero archiveSize is treated as unknown
// and skips the check.
func (r *jobRunner) ensureDiskBudget(root string, archiveSize int64) error {
	if archiveSize <= 0 {
		return nil
	}
	avail, err := fsutil.GetAvailableSpace(root)
	if err != nil {
		r.logger.Warn("disk budget check failed", "path", root, "error", err)
		return nil
	}
	required := uint64(archiveSize) * diskSafetyFactor
	if avail < required {
		return fmt.Errorf("insufficient disk space in %s: have %s, need %s",
			root, fsutil.FormatBytes(int64(avail)), fsutil.FormatBytes(int64(required)))
	}
	return nil
}

// download fetches rawURL into dir and returns the on-disk archive path plus
// its size in bytes. Progress is logged every downloadProgressInterval. An
// empty URL basename (e.g. "https://host" or "https://host/") yields an
// invalid-URL error so the job fails cleanly rather than writing to a
// filename derived from the hostname.
func (r *jobRunner) download(ctx context.Context, logger *slog.Logger,
	rawURL, dir string) (string, int64, error) {
	redacted := redactedJobURL(rawURL)
	basename := urlBasename(rawURL)
	if basename == "" {
		return "", 0, fmt.Errorf("invalid snapshot url %s: missing filename in path", redacted)
	}

	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build request for %s: %w", redacted, err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("do request for %s: %w", redacted, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("http status %d for %s", resp.StatusCode, redacted)
	}

	dest := filepath.Join(dir, basename)
	out, err := os.Create(dest)
	if err != nil {
		return "", 0, fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() { _ = out.Close() }()

	written, err := copyWithProgress(dlCtx, out, resp.Body, logger,
		downloadProgressInterval, resp.ContentLength)
	if err != nil {
		return "", 0, err
	}
	if err := out.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync %s: %w", dest, err)
	}
	r.deps.Collectors.AddBytesDownloaded(written)
	logger.Info("download complete", "path", dest, "bytes", written)
	return dest, written, nil
}

// copyWithProgress is io.Copy that emits a progress log entry every tick.
// The ticker check is non-blocking so the read loop drives the copy rate;
// progress events are opportunistic rather than forced into a goroutine.
func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader,
	logger *slog.Logger, tick time.Duration, total int64) (int64, error) {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	var written int64
	nextLog := time.Now().Add(tick)

	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, fmt.Errorf("write: %w", werr)
			}
			written += int64(n)
		}
		if now := time.Now(); now.After(nextLog) {
			logger.Info("download progress", "bytes", written, "total", total)
			nextLog = now.Add(tick)
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, fmt.Errorf("read: %w", rerr)
		}
	}
}

// convert invokes internal/migration to produce a PebbleDB tree at pebbleDir.
// The migration package expects the source to be a directory containing one
// or more *.db subdirectories.
//
// internal/migration.RunLevelToPebble pre-dates context plumbing, so the call
// runs in a child goroutine and we select on ctx.Done() to honour cancellation
// from Stop or a daemon-wide shutdown. When ctx fires first, the goroutine is
// left to complete on its own (registered on ws.bgTasks so cleanupWorkspace
// can await handle release before os.RemoveAll) and the caller gets ctx.Err();
// v0.5.x is expected to plumb context through the migration API so this
// wrapper can collapse back to a direct call.
func (r *jobRunner) convert(ctx context.Context, logger *slog.Logger,
	ws *workspace, levelDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	logger.Info("conversion starting", "src", levelDir, "out", ws.pebbleDir)
	cfg := &migration.RunConfig{
		Workers:        migrationWorkers,
		BatchMemory:    migrationBatchMemoryMB,
		Verbose:        false,
		MetricsEnabled: false,
	}

	done := make(chan error, 1)
	ws.bgTasks.Add(1)
	go func() {
		defer ws.bgTasks.Done()
		done <- migration.RunLevelToPebble(levelDir, ws.pebbleDir, cfg, ws.migrationTmp)
	}()

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		logger.Info("conversion complete")
		return nil
	case <-ctx.Done():
		logger.Warn("conversion cancelled; background goroutine will drain",
			"error", ctx.Err())
		return ctx.Err()
	}
}

// verify runs internal/verify.Run against the source/destination pair.
//
// Mirrors convert: verify.Run does not yet accept a context, so the blocking
// call is wrapped in a goroutine and multiplexed against ctx.Done(). On
// cancellation the wrapper returns ctx.Err() and leaves the background call
// to complete; the goroutine is registered on ws.bgTasks so cleanupWorkspace
// can wait (bounded by workspaceCleanupGrace) for file handles to release
// before removing the scratch tree. See convert's doc comment for the v0.5.x
// roadmap.
func (r *jobRunner) verify(ctx context.Context, logger *slog.Logger,
	ws *workspace, levelDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dataDir := filepath.Join(ws.pebbleDir, "data")
	logger.Info("verification starting", "src", levelDir, "out", dataDir)
	cfg := &verify.Config{
		SamplePercent: verifySamplePercent,
		StopOnError:   true,
		Verbose:       false,
	}

	done := make(chan error, 1)
	ws.bgTasks.Add(1)
	go func() {
		defer ws.bgTasks.Done()
		done <- verify.Run(levelDir, dataDir, cfg)
	}()

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		logger.Info("verification complete")
		return nil
	case <-ctx.Done():
		logger.Warn("verification cancelled; background goroutine will drain",
			"error", ctx.Err())
		return ctx.Err()
	}
}

// replaceDBTree swaps the original LevelDB subtree in the extracted archive
// for the newly-produced PebbleDB subtree. When delete_source_snapshot is
// false, the LevelDB tree is retained alongside the PebbleDB output under a
// sibling directory suffixed "_pebbledb".
func (r *jobRunner) replaceDBTree(levelDir, pebbleDir string) error {
	dataDir := filepath.Join(pebbleDir, "data")
	cleanedLevelDir := filepath.Clean(levelDir)

	if r.deps.Cfg.Conversion.DeleteSourceSnapshot {
		if err := os.RemoveAll(cleanedLevelDir); err != nil {
			return fmt.Errorf("remove source leveldb tree %s: %w", cleanedLevelDir, err)
		}
		if err := os.Rename(dataDir, cleanedLevelDir); err != nil {
			return fmt.Errorf("move pebble tree into place: %w", err)
		}
		return nil
	}
	pebbleSiblingDir := cleanedLevelDir + "_pebbledb"
	if err := os.Rename(dataDir, pebbleSiblingDir); err != nil {
		return fmt.Errorf("move pebble tree alongside leveldb: %w", err)
	}
	return nil
}

// fanOutUploads pushes archiveOut to every configured target in parallel.
// Every configured target must succeed; any non-zero failure count aborts the
// job so partial successes (e.g. local succeeded, S3 failed) never masquerade
// as a completed backup. Individual failures are still WARN-logged per target
// for operator visibility, and the aggregate is surfaced via errors.Join so
// callers observing the return can unwrap each underlying cause.
func (r *jobRunner) fanOutUploads(ctx context.Context, logger *slog.Logger,
	archiveOut string) error {
	if len(r.deps.Targets) == 0 {
		return errors.New("no store targets configured")
	}
	remoteName := filepath.Base(archiveOut)

	var archiveSize int64
	if info, err := os.Stat(archiveOut); err == nil {
		archiveSize = info.Size()
	}

	var wg sync.WaitGroup
	errs := make([]error, len(r.deps.Targets))
	wg.Add(len(r.deps.Targets))
	for i, t := range r.deps.Targets {
		go func(idx int, target store.Target) {
			defer wg.Done()
			tLogger := logger.With("target", target.Name())
			tLogger.Info("upload starting")
			if err := target.Upload(ctx, archiveOut, remoteName); err != nil {
				tLogger.Warn("upload failed", "error", err)
				errs[idx] = err
				return
			}
			r.deps.Collectors.AddBytesUploaded(archiveSize)
			tLogger.Info("upload complete")
		}(i, t)
	}
	wg.Wait()

	total := len(r.deps.Targets)
	var failed int
	joined := make([]error, 0, total)
	for _, err := range errs {
		if err != nil {
			failed++
			joined = append(joined, err)
		}
	}
	if failed == 0 {
		return nil
	}
	if failed == total {
		return fmt.Errorf("upload: all %d targets failed: %w", failed, errors.Join(joined...))
	}
	return fmt.Errorf("upload: %d of %d targets failed: %w",
		failed, total, errors.Join(joined...))
}

// outputArchivePath builds the on-disk path for the repacked archive using
// the spec's naming pattern: <original_name>_pebbledb_<unix>.<ext>.
func (r *jobRunner) outputArchivePath(outDir string, job queue.Job,
	archivePath string, mode store.Compression) string {
	original := archiveStem(filepath.Base(archivePath))
	ext := repack.Extension(mode)
	filename := fmt.Sprintf("%s_pebbledb_%d.%s", original, time.Now().Unix(), ext)
	return filepath.Join(outDir, filename)
}

// archiveStem strips known archive extensions from filename (tar, tar.gz,
// tar.zst, tar.lz4, zip) so the output pattern does not accumulate suffixes.
func archiveStem(filename string) string {
	lower := strings.ToLower(filename)
	for _, suffix := range []string{".tar.gz", ".tar.zst", ".tar.lz4", ".tar", ".zip"} {
		if strings.HasSuffix(lower, suffix) {
			return filename[:len(filename)-len(suffix)]
		}
	}
	return filename
}

// urlBasename returns the final path component of raw. Query strings and
// fragments are ignored. When raw is unparseable, has no path, or the path is
// "/", the returned basename is the empty string so callers can surface a
// validation error instead of persisting a junk filename derived from the
// host. A basename that resolves to ".." or that would contain a path
// separator is likewise rejected so an attacker-controlled URL cannot escape
// the scratch directory via traversal.
func urlBasename(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	if p == "" || p == "/" {
		return ""
	}
	base := path.Base(p)
	if base == "/" || base == "." || base == ".." {
		return ""
	}
	if strings.ContainsRune(base, '/') || strings.ContainsRune(base, os.PathSeparator) {
		return ""
	}
	return base
}

// redactedJobURL returns a log-safe form of raw. The implementation delegates
// to queue.RedactURL so every logger in the daemon emits the same redaction
// shape; keeping the runner-local wrapper avoids churning the call sites that
// already pass a package-internal name.
func redactedJobURL(raw string) string {
	return queue.RedactURL(raw)
}

// findLevelDBRoot locates a directory under root that contains one or more
// *.db subdirectories, matching internal/migration's detection contract.
// Returns the first such directory in a pre-order walk; returns ErrNoLevelDB
// when none is found.
func findLevelDBRoot(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if hasDBChildren(path) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan extracted tree: %w", err)
	}
	if found == "" {
		return "", ErrNoLevelDB
	}
	return found, nil
}

// hasDBChildren reports whether dir contains at least one subdirectory whose
// name ends in .db (the LevelDB convention used by Cosmos SDK nodes).
func hasDBChildren(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			return true
		}
	}
	return false
}
