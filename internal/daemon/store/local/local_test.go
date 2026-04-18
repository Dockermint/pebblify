package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// TestNew_EmptyDirectoryReturnsError validates the empty-dir guard.
func TestNew_EmptyDirectoryReturnsError(t *testing.T) {
	t.Parallel()
	_, err := New(config.LocalSaveSection{LocalSaveDirectory: ""})
	if err == nil {
		t.Fatal("New(empty dir) expected error, got nil")
	}
}

// TestNew_NonEmptyDirectorySucceeds constructs a valid LocalTarget.
func TestNew_NonEmptyDirectorySucceeds(t *testing.T) {
	t.Parallel()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: "/some/dir"})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if tgt == nil {
		t.Fatal("New() returned nil target")
	}
}

// TestLocalTarget_Name returns the const Name identifier.
func TestLocalTarget_Name(t *testing.T) {
	t.Parallel()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: "/some/dir"})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if got := tgt.Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}

// TestLocalTarget_Upload_CancelledContext returns ctx error immediately.
func TestLocalTarget_Upload_CancelledContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = tgt.Upload(ctx, "/any/path", "output.tar.lz4")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Upload cancelled ctx error = %v, want context.Canceled", err)
	}
}

// TestLocalTarget_Upload_EmptyLocalPath returns error.
func TestLocalTarget_Upload_EmptyLocalPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	err = tgt.Upload(context.Background(), "", "out.tar")
	if err == nil {
		t.Fatal("Upload(empty localPath) expected error, got nil")
	}
}

// TestLocalTarget_Upload_EmptyRemoteName returns error for an empty name and for
// unsafe names that would write outside LocalSaveDirectory.
func TestLocalTarget_Upload_EmptyRemoteName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	src := filepath.Join(t.TempDir(), "src.tar")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	unsafeNames := []struct {
		name       string
		remoteName string
	}{
		{"empty name", ""},
		{"traversal dotdot", "../outside.txt"},
		{"nested traversal", "subdir/../../evil.txt"},
		{"absolute path", "/etc/passwd"},
	}
	for _, tt := range unsafeNames {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if uploadErr := tgt.Upload(context.Background(), src, tt.remoteName); uploadErr == nil {
				t.Fatalf("Upload(remoteName=%q) expected error, got nil", tt.remoteName)
			}
		})
	}
}

// TestLocalTarget_Upload_SameDevice moves file without copying (rename path).
func TestLocalTarget_Upload_SameDevice(t *testing.T) {
	t.Parallel()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Write content to source file.
	srcFile := filepath.Join(srcDir, "snap.tar.lz4")
	content := []byte("packed data")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dstDir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if err := tgt.Upload(context.Background(), srcFile, "snap.tar.lz4"); err != nil {
		t.Fatalf("Upload() error: %v", err)
	}

	// File should exist in dst.
	dst := filepath.Join(dstDir, "snap.tar.lz4")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}
}

// TestLocalTarget_Upload_CreatesDestDir creates the destination directory on demand.
func TestLocalTarget_Upload_CreatesDestDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dstDir := filepath.Join(base, "deep", "nested", "dir")
	// Do NOT pre-create dstDir; Upload must do it.

	srcFile := filepath.Join(t.TempDir(), "snap.tar")
	if err := os.WriteFile(srcFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dstDir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if err := tgt.Upload(context.Background(), srcFile, "snap.tar"); err != nil {
		t.Fatalf("Upload() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dstDir, "snap.tar")); err != nil {
		t.Errorf("dst file missing: %v", err)
	}
}

// TestLocalTarget_Upload_MissingSourceFile returns error for non-existent src.
func TestLocalTarget_Upload_MissingSourceFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	err = tgt.Upload(context.Background(), "/nonexistent/file.tar", "out.tar")
	if err == nil {
		t.Fatal("Upload(missing src) expected error, got nil")
	}
}

// TestLocalTarget_Upload_SourcePreserved verifies that after a successful Upload
// the original source file is still present (copy semantics — source is never
// removed by Upload).
func TestLocalTarget_Upload_SourcePreserved(t *testing.T) {
	t.Parallel()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "snap.tar.lz4")
	content := []byte("snapshot payload")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	tgt, err := New(config.LocalSaveSection{LocalSaveDirectory: dstDir})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if err := tgt.Upload(context.Background(), srcFile, "snap.tar.lz4"); err != nil {
		t.Fatalf("Upload() error: %v", err)
	}

	// Destination must have the file.
	if _, statErr := os.Stat(filepath.Join(dstDir, "snap.tar.lz4")); statErr != nil {
		t.Errorf("dst file missing after Upload: %v", statErr)
	}

	// Source must still exist — Upload uses copy semantics, not move.
	if _, statErr := os.Stat(srcFile); statErr != nil {
		t.Errorf("source file was removed by Upload (copy semantics violated): %v", statErr)
	}

	// Source content must be intact.
	got, readErr := os.ReadFile(srcFile)
	if readErr != nil {
		t.Fatalf("read src after Upload: %v", readErr)
	}
	if string(got) != string(content) {
		t.Errorf("source content = %q after Upload, want %q", got, content)
	}
}

// blockingReader is a mock io.Reader that returns one byte immediately on the
// first Read (proving the copy loop is active), then blocks on subsequent reads
// until ctx is cancelled, at which point it returns an error so copyWithContext
// can observe the cancellation on its next ctx.Err() check at the loop top.
type blockingReader struct {
	ctx    context.Context
	primed bool
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if !r.primed {
		// Return one byte so copyWithContext enters the write path and
		// completes one full iteration before we force a blocking read.
		r.primed = true
		p[0] = 0x00
		return 1, nil
	}
	// Block until the context is cancelled, then return its error so the
	// caller (copyWithContext loop) propagates it on the next Err() check.
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// TestCopyWithContext_CancelMidCopy respects context cancellation mid-copy.
// A mock reader returns one byte immediately (proving the copy loop is active),
// then blocks until the context is cancelled. Cancelling while the read blocks
// exercises the real cancellation code path in copyWithContext.
func TestCopyWithContext_CancelMidCopy(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &blockingReader{ctx: ctx}

	dst, createErr := os.CreateTemp(t.TempDir(), "dst")
	if createErr != nil {
		t.Fatalf("create dst: %v", createErr)
	}
	defer func() {
		if closeErr := dst.Close(); closeErr != nil {
			t.Errorf("dst.Close: %v", closeErr)
		}
	}()

	// copyWithContext runs in a goroutine; result sent back via channel.
	type result struct{ err error }
	resultCh := make(chan result, 1)
	go func() {
		_, err := copyWithContext(ctx, dst, src)
		resultCh <- result{err: err}
	}()

	// Give the goroutine enough time to enter the loop and block on the second
	// Read, then cancel the context to unblock it.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case res := <-resultCh:
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("copyWithContext cancelled error = %v, want context.Canceled", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyWithContext did not return after context cancellation")
	}
}
