package repack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dockermint/Pebblify/internal/daemon/store"
)

// ---- helpers ----

// buildDirTree creates a directory tree with the given file paths and contents
// under root. Paths are relative.
func buildDirTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
}

// collectFiles returns a map of relative path -> content for every file under dir.
func collectFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		result[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return result
}

// ---- Extension ----

// TestExtension_AllModes verifies every compression mode maps to the right suffix.
func TestExtension_AllModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode store.Compression
		want string
	}{
		{store.CompNone, "tar"},
		{store.CompGzip, "tar.gz"},
		{store.CompZstd, "tar.zst"},
		{store.CompLZ4, "tar.lz4"},
		{store.Compression("unknown"), "tar"}, // fallback
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			t.Parallel()
			got := Extension(tt.mode)
			if got != tt.want {
				t.Errorf("Extension(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

// ---- Compress + Extract round-trips ----

// assertFilesEqual compares two maps of relative-path -> content for exact
// key and value equality, failing the test with descriptive messages on any
// mismatch, missing file, or unexpected extra file.
func assertFilesEqual(t *testing.T, got, want map[string]string) {
	t.Helper()
	for path, wantContent := range want {
		gotContent, ok := got[path]
		if !ok {
			t.Errorf("missing file %q in extracted output", path)
			continue
		}
		if gotContent != wantContent {
			t.Errorf("file %q: content = %q, want %q", path, gotContent, wantContent)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			t.Errorf("unexpected extra file %q in extracted output", path)
		}
	}
}

// TestCompress_Extract_Roundtrip_None verifies plain tar round-trip with exact content.
func TestCompress_Extract_Roundtrip_None(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"a/file.txt": "hello",
		"b/other":    "world",
	}
	src := t.TempDir()
	buildDirTree(t, src, want)

	archive := filepath.Join(t.TempDir(), "out.tar")
	if err := Compress(context.Background(), src, archive, store.CompNone); err != nil {
		t.Fatalf("Compress(none) error: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract(none) error: %v", err)
	}
	assertFilesEqual(t, collectFiles(t, dst), want)
}

// TestCompress_Extract_Roundtrip_Gzip verifies tar.gz round-trip with exact content.
func TestCompress_Extract_Roundtrip_Gzip(t *testing.T) {
	t.Parallel()
	want := map[string]string{"data.bin": "binary content"}
	src := t.TempDir()
	buildDirTree(t, src, want)

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := Compress(context.Background(), src, archive, store.CompGzip); err != nil {
		t.Fatalf("Compress(gzip) error: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract(gzip) error: %v", err)
	}
	assertFilesEqual(t, collectFiles(t, dst), want)
}

// TestCompress_Extract_Roundtrip_Zstd verifies tar.zst round-trip with exact content.
func TestCompress_Extract_Roundtrip_Zstd(t *testing.T) {
	t.Parallel()
	want := map[string]string{"snap.db": "leveldb-payload"}
	src := t.TempDir()
	buildDirTree(t, src, want)

	archive := filepath.Join(t.TempDir(), "out.tar.zst")
	if err := Compress(context.Background(), src, archive, store.CompZstd); err != nil {
		t.Fatalf("Compress(zstd) error: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract(zstd) error: %v", err)
	}
	assertFilesEqual(t, collectFiles(t, dst), want)
}

// TestCompress_Extract_Roundtrip_LZ4 verifies tar.lz4 round-trip with exact content.
func TestCompress_Extract_Roundtrip_LZ4(t *testing.T) {
	t.Parallel()
	want := map[string]string{"snap.db": "leveldb-payload"}
	src := t.TempDir()
	buildDirTree(t, src, want)

	archive := filepath.Join(t.TempDir(), "out.tar.lz4")
	if err := Compress(context.Background(), src, archive, store.CompLZ4); err != nil {
		t.Fatalf("Compress(lz4) error: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract(lz4) error: %v", err)
	}
	assertFilesEqual(t, collectFiles(t, dst), want)
}

// TestCompress_DirectoryStructurePreserved confirms nested dirs survive the round-trip
// with exact path and content equality.
func TestCompress_DirectoryStructurePreserved(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"dir/nested/deep.txt": "deep",
		"root.txt":            "root",
	}
	src := t.TempDir()
	buildDirTree(t, src, want)

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := Compress(context.Background(), src, archive, store.CompGzip); err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	assertFilesEqual(t, collectFiles(t, dst), want)
}

// ---- Compress error paths ----

// TestCompress_NotADirectory returns error when srcDir is a file.
func TestCompress_NotADirectory(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	_ = f.Close()

	err = Compress(context.Background(), f.Name(), filepath.Join(t.TempDir(), "out.tar"), store.CompNone)
	if err == nil {
		t.Fatal("Compress(file) expected error, got nil")
	}
}

// TestCompress_NonExistentSrc returns error.
func TestCompress_NonExistentSrc(t *testing.T) {
	t.Parallel()
	err := Compress(context.Background(), "/nonexistent/src", filepath.Join(t.TempDir(), "out.tar"), store.CompNone)
	if err == nil {
		t.Fatal("Compress(missing src) expected error, got nil")
	}
}

// TestCompress_CancelledContext returns ctx error.
func TestCompress_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := t.TempDir()
	err := Compress(ctx, src, filepath.Join(t.TempDir(), "out.tar"), store.CompNone)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Compress cancelled error = %v, want context.Canceled", err)
	}
}

// ---- Extract error paths ----

// TestExtract_UnknownFormat returns ErrUnknownFormat for an empty file.
func TestExtract_UnknownFormat(t *testing.T) {
	t.Parallel()
	// An empty file has 0 magic bytes -> detectFormat returns FormatUnknown.
	f, err := os.CreateTemp(t.TempDir(), "empty")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	_ = f.Close()

	err = Extract(context.Background(), f.Name(), t.TempDir())
	if !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("Extract(empty file) error = %v, want %v", err, ErrUnknownFormat)
	}
}

// TestExtract_NonExistentFile returns error.
func TestExtract_NonExistentFile(t *testing.T) {
	t.Parallel()
	err := Extract(context.Background(), "/nonexistent/archive.tar", t.TempDir())
	if err == nil {
		t.Fatal("Extract(missing file) expected error, got nil")
	}
}

// TestExtract_CancelledContext returns ctx error before opening file.
func TestExtract_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Extract(ctx, "/any/archive.tar", t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Extract cancelled error = %v, want context.Canceled", err)
	}
}

// ---- Path traversal guard ----

// buildMaliciousTar creates an in-memory tar archive with a path-traversal entry.
func buildMaliciousTar(t *testing.T, entryName string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("malicious content")
	hdr := &tar.Header{
		Name:     entryName,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write body: %v", err)
	}
	_ = tw.Close()

	out := filepath.Join(t.TempDir(), "malicious.tar")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar: %v", err)
	}
	return out
}

// TestExtract_PathTraversalGuard_DotDot verifies that a "../escape" tar entry
// either returns ErrUnsafePath or is safely contained; in either case no file
// escapes outside dst.
func TestExtract_PathTraversalGuard_DotDot(t *testing.T) {
	t.Parallel()
	archive := buildMaliciousTar(t, "../outside.txt")
	dst := t.TempDir()

	err := Extract(context.Background(), archive, dst)
	// safeJoin maps "../outside.txt" -> "<dst>/outside.txt" (inside root).
	// When the entry is safely re-rooted rather than rejected, err may be nil.
	// The invariant is: the file must NEVER appear outside dst.
	if err != nil && !errors.Is(err, ErrUnsafePath) {
		t.Errorf("Extract() error = %v, want nil or ErrUnsafePath", err)
	}

	parent := filepath.Dir(dst)
	escapedPath := filepath.Join(parent, "outside.txt")
	if _, statErr := os.Stat(escapedPath); statErr == nil {
		t.Errorf("path traversal: file escaped to %s", escapedPath)
	}

	// Walk dst — all written files must be inside dst.
	_ = filepath.Walk(dst, func(p string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(dst, p)
		if relErr != nil {
			t.Errorf("walk rel error: %v", relErr)
			return nil
		}
		if rel == ".." || len(rel) > 2 && rel[:3] == "../" {
			t.Errorf("file escaped dst: %s", p)
		}
		return nil
	})
}

// TestExtract_PathTraversalGuard_AbsoluteEntry verifies that an absolute-path
// tar entry is re-rooted inside dst and never reaches a privileged path on the
// real filesystem.
func TestExtract_PathTraversalGuard_AbsoluteEntry(t *testing.T) {
	t.Parallel()
	// safeJoin strips the leading "/" so "/tmp/evil" maps to "<dst>/tmp/evil".
	// We use a unique name under /tmp rather than /etc to avoid needing root.
	victimPath := filepath.Join(t.TempDir(), "pebblify_traversal_victim.txt")
	// Ensure it does not exist before the test.
	_ = os.Remove(victimPath)

	archive := buildMaliciousTar(t, victimPath)
	dst := t.TempDir()
	err := Extract(context.Background(), archive, dst)
	if err != nil && !errors.Is(err, ErrUnsafePath) {
		t.Errorf("Extract() error = %v, want nil or ErrUnsafePath", err)
	}

	// The victim path must NOT have been written at the absolute location.
	if _, statErr := os.Stat(victimPath); statErr == nil {
		_ = os.Remove(victimPath)
		t.Errorf("absolute-path entry wrote to %s outside destination", victimPath)
	}

	// Walk dst — every written file must be inside dst.
	_ = filepath.Walk(dst, func(p string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(dst, p)
		if relErr != nil {
			t.Errorf("walk rel error: %v", relErr)
			return nil
		}
		if rel == ".." || len(rel) > 2 && rel[:3] == "../" {
			t.Errorf("file escaped dst: %s", p)
		}
		return nil
	})
}

// ---- safeJoin ----

// TestSafeJoin_Table covers all cases of the path guard helper.
func TestSafeJoin_Table(t *testing.T) {
	t.Parallel()
	root := "/tmp/root"
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"safe nested", "a/b/c.txt", false},
		{"safe root file", "file.txt", false},
		// safeJoin prepends "/" before Clean, so "../escape.txt" becomes
		// filepath.Clean("/../escape.txt") = "/escape.txt", which joins to
		// root+"/escape.txt" — safely inside root. No error expected.
		{"dotdot escape", "../escape.txt", false},
		// "a/../../escape.txt" -> filepath.Clean("/a/../../escape.txt") = "/escape.txt"
		// -> root+"/escape.txt" — safely inside root. No error expected.
		{"deep dotdot", "a/../../escape.txt", false},
		{"absolute mapped inside root", "/etc/passwd", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := safeJoin(root, tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("safeJoin(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got == "" {
					t.Errorf("safeJoin(%q) returned empty string", tt.input)
					return
				}
				// Every successful result must be rooted at root — the path
				// traversal guard must never allow the caller to escape.
				rel, relErr := filepath.Rel(root, got)
				if relErr != nil {
					t.Errorf("safeJoin(%q): cannot compute rel path: %v", tt.input, relErr)
					return
				}
				if rel == ".." || len(rel) > 2 && rel[:3] == "../" {
					t.Errorf("safeJoin(%q) = %q escapes root %q (rel=%q)", tt.input, got, root, rel)
				}
			}
		})
	}
}

// ---- detectFormat ----

// TestDetectFormat_Table verifies magic byte classification.
func TestDetectFormat_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		magic []byte
		want  Format
	}{
		{"gzip", []byte{0x1f, 0x8b, 0, 0, 0, 0, 0, 0}, FormatTarGzip},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0, 0, 0}, FormatTarZstd},
		{"lz4", []byte{0x04, 0x22, 0x4d, 0x18, 0, 0, 0, 0}, FormatTarLZ4},
		{"zip pk34", []byte{0x50, 0x4b, 0x03, 0x04, 0, 0, 0, 0}, FormatZip},
		{"zip pk56", []byte{0x50, 0x4b, 0x05, 0x06, 0, 0, 0, 0}, FormatZip},
		{"zip pk78", []byte{0x50, 0x4b, 0x07, 0x08, 0, 0, 0, 0}, FormatZip},
		{"empty", []byte{}, FormatUnknown},
		{"tar_too_short_for_ustar_offset", make([]byte, 256), FormatUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectFormat(tt.magic)
			if got != tt.want {
				t.Errorf("detectFormat(%x) = %v, want %v", tt.magic, got, tt.want)
			}
		})
	}
}

// TestDetectFormat_TarWithUSTARMagic verifies that a real tar header block
// (512 bytes with USTAR magic at offset 257) is classified as FormatTar.
func TestDetectFormat_TarWithUSTARMagic(t *testing.T) {
	t.Parallel()
	// Build a minimal valid tar archive in memory via archive/tar, then peek the
	// first 512 bytes as detectFormat would.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "file.txt",
		Mode: 0o644,
		Size: 4,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The first 512 bytes of a tar archive are the header block; USTAR magic
	// is written by archive/tar at offset 257.
	magic := buf.Bytes()
	if len(magic) < 512 {
		t.Fatalf("tar buffer too short: %d bytes", len(magic))
	}
	magic = magic[:512]

	got := detectFormat(magic)
	if got != FormatTar {
		t.Errorf("detectFormat(real tar header) = %v, want %v", got, FormatTar)
	}
}

// ---- Format.String ----

// TestFormat_String covers all enum values.
func TestFormat_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		f    Format
		want string
	}{
		{FormatTar, "tar"},
		{FormatTarGzip, "tar.gz"},
		{FormatTarZstd, "tar.zst"},
		{FormatTarLZ4, "tar.lz4"},
		{FormatZip, "zip"},
		{FormatUnknown, "unknown"},
		{Format(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.f.String(); got != tt.want {
				t.Errorf("Format(%d).String() = %q, want %q", tt.f, got, tt.want)
			}
		})
	}
}

// TestExtract_ZipRoundTrip verifies zip extraction through the zip path.
func TestExtract_ZipRoundTrip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("data.txt")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := io.WriteString(f, "zip content"); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	archive := filepath.Join(t.TempDir(), "out.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	dst := t.TempDir()
	if err := Extract(context.Background(), archive, dst); err != nil {
		t.Fatalf("Extract(zip) error: %v", err)
	}
	got := collectFiles(t, dst)
	if _, ok := got["data.txt"]; !ok {
		t.Errorf("Extract(zip) missing data.txt; files: %v", got)
	}
}

// TestExtract_GzipCorrupt returns error for truncated gzip body.
func TestExtract_GzipCorrupt(t *testing.T) {
	t.Parallel()
	// Write a file that starts with gzip magic but is corrupt.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("corrupt"))
	// Do NOT close gw — truncated gzip stream.
	corrupt := buf.Bytes()

	archive := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err := os.WriteFile(archive, corrupt, 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	err := Extract(context.Background(), archive, t.TempDir())
	if err == nil {
		t.Error("Extract(corrupt gzip) expected error, got nil")
	}
}

// ---- Symlink security (CodeQL fix coverage) ----

// buildSymlinkTar writes an in-memory tar with a single TypeSymlink entry and
// saves it to t.TempDir(). entryName is the archive path of the link;
// linkname is the Linkname field (the symlink target).
func buildSymlinkTar(t *testing.T, entryName, linkname string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     entryName,
		Typeflag: tar.TypeSymlink,
		Linkname: linkname,
		Mode:     0o777,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("buildSymlinkTar: write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("buildSymlinkTar: close: %v", err)
	}
	out := filepath.Join(t.TempDir(), "symlink.tar")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("buildSymlinkTar: write file: %v", err)
	}
	return out
}

// TestExtract_SymlinkRejected is a table-driven test for symlink tar entries
// that must be rejected with ErrUnsafePath. Each case also asserts that no
// file named by the last element of the entry path was created in destDir.
func TestExtract_SymlinkRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		entryName string
		linkname  string
	}{
		{
			name:      "absolute target rejected",
			entryName: "evil",
			linkname:  "/etc/passwd",
		},
		{
			name:      "relative escape rejected",
			entryName: "evil",
			linkname:  "../../../etc/passwd",
		},
		{
			name:      "deep relative escape rejected",
			entryName: "a/b/evil",
			linkname:  "../../../../outside",
		},
		{
			name:      "empty target rejected",
			entryName: "evil",
			linkname:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			archive := buildSymlinkTar(t, tt.entryName, tt.linkname)
			dst := t.TempDir()

			err := Extract(context.Background(), archive, dst)

			if !errors.Is(err, ErrUnsafePath) {
				t.Errorf("Extract() error = %v, want errors.Is(err, ErrUnsafePath)", err)
			}
			// The symlink must not have been created anywhere inside dst.
			base := filepath.Base(tt.entryName)
			created := filepath.Join(dst, base)
			if _, statErr := os.Lstat(created); statErr == nil {
				t.Errorf("Extract() created symlink %s despite ErrUnsafePath", created)
			}
		})
	}
}

// TestExtract_ZipSymlinkRejected verifies that a zip entry with the
// fs.ModeSymlink bit set in its Mode field is rejected with ErrUnsafePath.
func TestExtract_ZipSymlinkRejected(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// zip.FileHeader.SetMode propagates the Unix mode bits including the
	// symlink type bits into the ExternalAttrs field, which zip.File.Mode()
	// reads back.
	fh := &zip.FileHeader{
		Name:   "evil-link",
		Method: zip.Store,
	}
	fh.SetMode(fs.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatalf("zip CreateHeader: %v", err)
	}
	// Body is the symlink target — the guard must fire before reading it.
	if _, err := io.WriteString(w, "/etc/passwd"); err != nil {
		t.Fatalf("zip write body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	archive := filepath.Join(t.TempDir(), "symlink.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	dst := t.TempDir()
	extractErr := Extract(context.Background(), archive, dst)
	if !errors.Is(extractErr, ErrUnsafePath) {
		t.Errorf("Extract(zip symlink) error = %v, want errors.Is(err, ErrUnsafePath)", extractErr)
	}

	// The symlink must not have been materialised inside dst.
	if _, statErr := os.Lstat(filepath.Join(dst, "evil-link")); statErr == nil {
		t.Errorf("Extract(zip symlink) created evil-link in dst despite ErrUnsafePath")
	}
}
