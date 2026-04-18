// Package repack packs and extracts snapshot archives for the daemon job
// pipeline.
//
// The pipeline is: extract original archive -> replace LevelDB subtree with
// PebbleDB subtree in place -> re-archive the whole tree with the operator's
// chosen codec. Callers use Compress to produce the output archive and
// Extract to open incoming archives regardless of format.
//
// Supported codecs in v0.4.0:
//   - none: plain tar (stdlib archive/tar)
//   - gzip: tar.gz (stdlib compress/gzip)
//   - zstd: tar.zst (github.com/klauspost/compress/zstd)
//   - lz4:  tar.lz4 (github.com/pierrec/lz4/v4, frame format)
package repack

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"

	"github.com/Dockermint/Pebblify/internal/daemon/store"
)

// Magic byte prefixes for format detection in Extract.
var (
	magicGzip    = []byte{0x1f, 0x8b}
	magicZstd    = []byte{0x28, 0xb5, 0x2f, 0xfd}
	magicLZ4     = []byte{0x04, 0x22, 0x4d, 0x18}
	magicZIPPK34 = []byte{0x50, 0x4b, 0x03, 0x04}
	magicZIPPK56 = []byte{0x50, 0x4b, 0x05, 0x06}
	magicZIPPK78 = []byte{0x50, 0x4b, 0x07, 0x08}

	// ustarMagicPosix is the "ustar\x00" signature defined by POSIX.1-1988.
	ustarMagicPosix = []byte{'u', 's', 't', 'a', 'r', 0x00}
	// ustarMagicGNU is the "ustar  \x00" signature emitted by GNU tar when
	// the header carries extended GNU attributes.
	ustarMagicGNU = []byte{'u', 's', 't', 'a', 'r', ' ', ' ', 0x00}
)

// tarHeaderPeek is the number of bytes peeked from the archive so the USTAR
// magic at offset 257 is reachable. 512 is the tar block size.
const tarHeaderPeek = 512

// ustarMagicOffset is the offset of the USTAR signature inside the first tar
// header block.
const ustarMagicOffset = 257

// Format identifies an archive container/compression combination.
type Format int

// Archive format enumeration. Extract derives the format from magic bytes.
const (
	// FormatUnknown is returned when the first 8 bytes of the archive do not
	// match any supported magic prefix.
	FormatUnknown Format = iota
	// FormatTar is uncompressed tar.
	FormatTar
	// FormatTarGzip is gzipped tar (tar.gz).
	FormatTarGzip
	// FormatTarZstd is zstd-compressed tar (tar.zst).
	FormatTarZstd
	// FormatTarLZ4 is lz4-compressed tar (tar.lz4).
	FormatTarLZ4
	// FormatZip is a zip archive.
	FormatZip
)

// String returns a human label for the format.
func (f Format) String() string {
	switch f {
	case FormatTar:
		return "tar"
	case FormatTarGzip:
		return "tar.gz"
	case FormatTarZstd:
		return "tar.zst"
	case FormatTarLZ4:
		return "tar.lz4"
	case FormatZip:
		return "zip"
	default:
		return "unknown"
	}
}

// Sentinel errors returned by the package.
var (
	// ErrUnknownFormat is returned by Extract when the archive magic bytes
	// do not match any supported format.
	ErrUnknownFormat = errors.New("repack: unknown archive format")
	// ErrUnsafePath is returned when an archive entry resolves outside the
	// extraction root (tarslip / zip-slip).
	ErrUnsafePath = errors.New("repack: archive entry escapes destination")
)

// Extension returns the file extension (without leading dot) used for
// archives produced with mode. None yields "tar".
func Extension(mode store.Compression) string {
	switch mode {
	case store.CompNone:
		return "tar"
	case store.CompGzip:
		return "tar.gz"
	case store.CompZstd:
		return "tar.zst"
	case store.CompLZ4:
		return "tar.lz4"
	default:
		return "tar"
	}
}

// Compress packs srcDir into a single archive at destPath using mode.
//
// The archive preserves the directory tree rooted at srcDir; relative paths
// inside the archive are anchored to filepath.Base(srcDir). Symlinks are
// serialized as tar symlink entries. Non-regular, non-directory, non-symlink
// files (sockets, devices) are skipped with no error, matching GNU tar's
// default behaviour.
func Compress(ctx context.Context, srcDir, destPath string, mode store.Compression) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("repack compress: stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repack compress: %s is not a directory", srcDir)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("repack compress: mkdir dest: %w", err)
	}

	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("repack compress: open dest %s: %w", destPath, err)
	}
	defer func() { _ = out.Close() }()

	bw := bufio.NewWriterSize(out, 1<<20)
	defer func() { _ = bw.Flush() }()

	tarSink, closer, err := openCompressedWriter(bw, mode)
	if err != nil {
		return err
	}

	tw := tar.NewWriter(tarSink)

	if err := writeTarTree(ctx, tw, srcDir); err != nil {
		_ = tw.Close()
		_ = closer()
		return err
	}

	if err := tw.Close(); err != nil {
		_ = closer()
		return fmt.Errorf("repack compress: close tar: %w", err)
	}
	if err := closer(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("repack compress: flush: %w", err)
	}
	return out.Sync()
}

// openCompressedWriter wraps w in the compressor selected by mode. The
// returned closer MUST be called before the underlying writer to flush
// remaining framing bytes.
func openCompressedWriter(w io.Writer, mode store.Compression) (io.Writer, func() error, error) {
	switch mode {
	case store.CompNone:
		return w, func() error { return nil }, nil
	case store.CompGzip:
		gz := gzip.NewWriter(w)
		return gz, gz.Close, nil
	case store.CompZstd:
		enc, err := zstd.NewWriter(w)
		if err != nil {
			return nil, nil, fmt.Errorf("repack compress: zstd writer: %w", err)
		}
		return enc, enc.Close, nil
	case store.CompLZ4:
		enc := lz4.NewWriter(w)
		return enc, enc.Close, nil
	default:
		return nil, nil, fmt.Errorf("repack compress: unknown mode %q", mode)
	}
}

// writeTarTree walks srcDir and writes every entry (dir, regular file,
// symlink) to tw. Paths inside the tar are relative to srcDir.
func writeTarTree(ctx context.Context, tw *tar.Writer, srcDir string) error {
	base := filepath.Clean(srcDir)
	return filepath.Walk(base, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(base, p)
		if err != nil {
			return fmt.Errorf("repack compress: rel %s: %w", p, err)
		}
		if rel == "." {
			return nil
		}

		mode := info.Mode()
		switch {
		case mode.IsDir():
			return writeTarDir(tw, rel, info)
		case mode.IsRegular():
			return writeTarRegular(tw, p, rel, info)
		case mode&os.ModeSymlink != 0:
			return writeTarSymlink(tw, p, rel, info)
		default:
			return nil
		}
	})
}

// writeTarDir writes a directory entry to tw.
func writeTarDir(tw *tar.Writer, rel string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("repack compress: header %s: %w", rel, err)
	}
	hdr.Name = rel + "/"
	return tw.WriteHeader(hdr)
}

// writeTarRegular writes a regular-file entry + body to tw.
func writeTarRegular(tw *tar.Writer, abs, rel string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("repack compress: header %s: %w", rel, err)
	}
	hdr.Name = rel
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("repack compress: write header %s: %w", rel, err)
	}

	f, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("repack compress: open %s: %w", abs, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("repack compress: write body %s: %w", rel, err)
	}
	return nil
}

// writeTarSymlink writes a symlink entry to tw.
func writeTarSymlink(tw *tar.Writer, abs, rel string, info os.FileInfo) error {
	target, err := os.Readlink(abs)
	if err != nil {
		return fmt.Errorf("repack compress: readlink %s: %w", abs, err)
	}
	hdr, err := tar.FileInfoHeader(info, target)
	if err != nil {
		return fmt.Errorf("repack compress: header symlink %s: %w", rel, err)
	}
	hdr.Name = rel
	return tw.WriteHeader(hdr)
}

// Extract decompresses archivePath into destDir. The format is detected from
// the first bytes of the file; unknown magic returns ErrUnknownFormat.
// Archive entries that resolve outside destDir return ErrUnsafePath.
func Extract(ctx context.Context, archivePath, destDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("repack extract: mkdir %s: %w", destDir, err)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("repack extract: open %s: %w", archivePath, err)
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReaderSize(f, 1<<20)
	// Peek at least the first tar block so detectFormat can validate the
	// USTAR magic at offset 257 before classifying as raw tar.
	magic, _ := br.Peek(tarHeaderPeek)

	format := detectFormat(magic)
	switch format {
	case FormatZip:
		return extractZip(ctx, archivePath, destDir)
	case FormatTar, FormatTarGzip, FormatTarZstd, FormatTarLZ4:
		return extractTarStream(ctx, br, format, destDir)
	default:
		return fmt.Errorf("%w: magic=%x", ErrUnknownFormat, truncateForLog(magic))
	}
}

// detectFormat classifies archive magic bytes. Compressor and zip signatures
// match the first few bytes. Raw tar is accepted only when the POSIX USTAR
// signature is present at offset 257 ("ustar\x00" or "ustar  \x00"); anything
// else returns FormatUnknown so callers surface ErrUnknownFormat instead of
// feeding garbage into the tar reader.
func detectFormat(magic []byte) Format {
	if hasPrefix(magic, magicGzip) {
		return FormatTarGzip
	}
	if hasPrefix(magic, magicZstd) {
		return FormatTarZstd
	}
	if hasPrefix(magic, magicLZ4) {
		return FormatTarLZ4
	}
	if hasPrefix(magic, magicZIPPK34) || hasPrefix(magic, magicZIPPK56) || hasPrefix(magic, magicZIPPK78) {
		return FormatZip
	}
	if hasUSTARMagic(magic) {
		return FormatTar
	}
	return FormatUnknown
}

// hasUSTARMagic reports whether b carries a POSIX USTAR signature at
// offset 257. Both the strict POSIX form ("ustar\x00") and the GNU form
// ("ustar  \x00") are accepted.
func hasUSTARMagic(b []byte) bool {
	if len(b) >= ustarMagicOffset+len(ustarMagicGNU) {
		if bytesEqual(b[ustarMagicOffset:ustarMagicOffset+len(ustarMagicGNU)], ustarMagicGNU) {
			return true
		}
	}
	if len(b) >= ustarMagicOffset+len(ustarMagicPosix) {
		if bytesEqual(b[ustarMagicOffset:ustarMagicOffset+len(ustarMagicPosix)], ustarMagicPosix) {
			return true
		}
	}
	return false
}

// bytesEqual is a small alternative to bytes.Equal kept inline so the detect
// path has no extra import for a six-byte comparison.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// truncateForLog caps magic-byte dumps so error strings stay short when the
// peek buffer holds a full tar header.
func truncateForLog(b []byte) []byte {
	const logCap = 16
	if len(b) > logCap {
		return b[:logCap]
	}
	return b
}

// hasPrefix reports whether b begins with prefix.
func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

// extractTarStream opens the appropriate decompressor over r and extracts
// every entry into destDir.
func extractTarStream(ctx context.Context, r io.Reader, format Format, destDir string) error {
	var (
		body   io.Reader
		closer func() error
	)
	switch format {
	case FormatTar:
		body = r
		closer = func() error { return nil }
	case FormatTarGzip:
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("repack extract: gzip reader: %w", err)
		}
		body = gz
		closer = gz.Close
	case FormatTarZstd:
		dec, err := zstd.NewReader(r)
		if err != nil {
			return fmt.Errorf("repack extract: zstd reader: %w", err)
		}
		body = dec
		closer = func() error { dec.Close(); return nil }
	case FormatTarLZ4:
		dec := lz4.NewReader(r)
		body = dec
		closer = func() error { return nil }
	default:
		return fmt.Errorf("%w: format=%s", ErrUnknownFormat, format)
	}
	defer func() { _ = closer() }()

	tr := tar.NewReader(body)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("repack extract: tar next: %w", err)
		}
		if err := writeTarEntry(tr, hdr, destDir); err != nil {
			return err
		}
	}
}

// writeTarEntry materializes a single tar entry under destDir, guarding
// against path-traversal attacks.
func writeTarEntry(tr *tar.Reader, hdr *tar.Header, destDir string) error {
	target, err := safeJoin(destDir, hdr.Name)
	if err != nil {
		return err
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777|0o700)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("repack extract: mkdir parent %s: %w", target, err)
		}
		out, err := os.OpenFile(target,
			os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
		if err != nil {
			return fmt.Errorf("repack extract: create %s: %w", target, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return fmt.Errorf("repack extract: copy %s: %w", target, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("repack extract: close %s: %w", target, err)
		}
		return nil
	case tar.TypeSymlink:
		return fmt.Errorf("%w: symlink entry %s (target %s); symlinks not allowed",
			ErrUnsafePath, hdr.Name, hdr.Linkname)
	default:
		return nil
	}
}

// extractZip decodes a zip archive into destDir.
func extractZip(ctx context.Context, archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("repack extract: zip open: %w", err)
	}
	defer func() { _ = zr.Close() }()

	for _, entry := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeZipEntry(entry, destDir); err != nil {
			return err
		}
	}
	return nil
}

// writeZipEntry materializes a single zip entry under destDir. Symlink
// entries (identified by fs.ModeSymlink in the zip mode field) are rejected
// with ErrUnsafePath: the Compress path never emits zip symlinks, and
// accepting untrusted symlinks would reopen the traversal vector closed in
// the tar path.
func writeZipEntry(entry *zip.File, destDir string) error {
	if entry.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink entry %s; symlinks not allowed",
			ErrUnsafePath, entry.Name)
	}

	target, err := safeJoin(destDir, entry.Name)
	if err != nil {
		return err
	}

	if entry.FileInfo().IsDir() {
		return os.MkdirAll(target, entry.Mode()|0o700)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("repack extract: mkdir parent %s: %w", target, err)
	}
	src, err := entry.Open()
	if err != nil {
		return fmt.Errorf("repack extract: open entry %s: %w", entry.Name, err)
	}
	defer func() { _ = src.Close() }()

	out, err := os.OpenFile(target,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, entry.Mode()|0o600)
	if err != nil {
		return fmt.Errorf("repack extract: create %s: %w", target, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("repack extract: copy %s: %w", target, err)
	}
	return nil
}

// safeJoin resolves name against root, rejecting any path that escapes root.
func safeJoin(root, name string) (string, error) {
	cleaned := filepath.Clean("/" + name)
	joined := filepath.Join(root, cleaned)
	if err := containsPath(root, joined); err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, name)
	}
	return joined, nil
}

// containsPath reports whether candidate resolves inside root. It returns a
// non-nil error when candidate equals root's parent or lies outside root.
// The check is lexical (no filesystem resolution); callers that need to
// follow existing symlinks should resolve the path first.
func containsPath(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return fmt.Errorf("rel %s: %w", candidate, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s escapes root %s", candidate, root)
	}
	return nil
}
