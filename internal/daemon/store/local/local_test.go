package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: "/some/dir"})
	if got := tgt.Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}

// TestLocalTarget_Upload_CancelledContext returns ctx error immediately.
func TestLocalTarget_Upload_CancelledContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dir})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := tgt.Upload(ctx, "/any/path", "output.tar.lz4")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Upload cancelled ctx error = %v, want context.Canceled", err)
	}
}

// TestLocalTarget_Upload_EmptyLocalPath returns error.
func TestLocalTarget_Upload_EmptyLocalPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	err := tgt.Upload(context.Background(), "", "out.tar")
	if err == nil {
		t.Fatal("Upload(empty localPath) expected error, got nil")
	}
}

// TestLocalTarget_Upload_EmptyRemoteName returns error.
func TestLocalTarget_Upload_EmptyRemoteName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	src := filepath.Join(t.TempDir(), "src.tar")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	err := tgt.Upload(context.Background(), src, "")
	if err == nil {
		t.Fatal("Upload(empty remoteName) expected error, got nil")
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

	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dstDir})
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

	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dstDir})
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
	tgt, _ := New(config.LocalSaveSection{LocalSaveDirectory: dir})
	err := tgt.Upload(context.Background(), "/nonexistent/file.tar", "out.tar")
	if err == nil {
		t.Fatal("Upload(missing src) expected error, got nil")
	}
}

// TestCopyWithContext_CancelMidCopy respects context cancellation mid-read.
func TestCopyWithContext_CancelMidCopy(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	pr, pw, _ := os.Pipe()
	defer func() { _ = pr.Close() }()

	// Write 2 MiB so copy needs more than one 1 MiB chunk.
	go func() {
		defer func() { _ = pw.Close() }()
		chunk := make([]byte, 2<<20)
		_, _ = pw.Write(chunk)
	}()

	dst, err := os.CreateTemp(t.TempDir(), "dst")
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	defer func() { _ = dst.Close() }()

	cancel() // cancel before copy

	_, err = copyWithContext(ctx, dst, pr)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("copyWithContext cancelled error = %v, want context.Canceled", err)
	}
}
