// Package local implements a filesystem-backed store.Target.
//
// Uploads copy the source file into the configured destination directory
// using a write-to-temp-then-rename sequence so partial writes never become
// visible under the final path. The original file at localPath is always
// preserved; upstream cleanup removes the job workspace after all targets
// finish so individual targets must not take ownership of the source.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// Name is the Target identifier reported by LocalTarget.Name.
const Name = "local"

// LocalTarget stores archives on the local filesystem under a configured
// directory. Zero value is not usable; construct via New.
type LocalTarget struct {
	dir string
}

// New validates cfg and returns a ready-to-use LocalTarget.
//
// The destination directory is not created at construction time; creation
// is deferred to the first Upload call so a mis-typed path fails on the
// job that tried to use it, not at daemon startup.
func New(cfg config.LocalSaveSection) (*LocalTarget, error) {
	if cfg.LocalSaveDirectory == "" {
		return nil, errors.New("local: local_save_directory must not be empty")
	}
	return &LocalTarget{dir: cfg.LocalSaveDirectory}, nil
}

// Name implements store.Target.
func (t *LocalTarget) Name() string { return Name }

// Upload implements store.Target. It copies localPath to <dir>/<remoteName>
// via a temp file in the destination directory, fsyncs the bytes, then
// atomically renames into place. The source file is never removed; the
// caller's workspace cleanup is the sole owner of localPath.
//
// remoteName must be a bare filename (no separators, no traversal, non-empty);
// anything else is rejected so an attacker-controlled name cannot escape t.dir.
func (t *LocalTarget) Upload(ctx context.Context, localPath, remoteName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if localPath == "" || remoteName == "" {
		return errors.New("local upload: localPath and remoteName must be non-empty")
	}
	if err := validateRemoteName(remoteName); err != nil {
		return fmt.Errorf("local upload: %w", err)
	}

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return fmt.Errorf("local upload: mkdir %s: %w", t.dir, err)
	}

	dst := filepath.Join(t.dir, remoteName)
	if err := copyFileAtomic(ctx, localPath, dst); err != nil {
		return fmt.Errorf("local upload: copy %s -> %s: %w", localPath, dst, err)
	}
	return nil
}

// validateRemoteName rejects names that are empty, absolute, contain path
// separators, or resolve to "." / ".." after filepath.Base. The check runs
// under the same semantics as filepath.Base so attacker-supplied input cannot
// traverse out of t.dir regardless of the host OS separator.
func validateRemoteName(remoteName string) error {
	if remoteName == "" {
		return errors.New("remoteName must not be empty")
	}
	if filepath.IsAbs(remoteName) {
		return fmt.Errorf("remoteName %q must not be absolute", remoteName)
	}
	if filepath.Base(remoteName) != remoteName {
		return fmt.Errorf("remoteName %q must be a bare filename", remoteName)
	}
	if remoteName == "." || remoteName == ".." {
		return fmt.Errorf("remoteName %q is not a valid filename", remoteName)
	}
	return nil
}

// copyFileAtomic writes src to a sibling temp file of dst, fsyncs, closes,
// then atomically renames the temp onto dst. On any error the temp file is
// removed so no partial output is ever visible. The destination directory is
// also fsynced after the rename so crash-recovery guarantees are preserved on
// platforms that require it (ext4 with data=ordered, xfs).
func copyFileAtomic(ctx context.Context, src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := in.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("close source %s: %w", src, cerr)
		}
	}()

	destDir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(destDir, ".pebblify-upload-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && retErr == nil {
				// Best-effort removal; surface only when a primary error did
				// not already eclipse it so operators still see stale-temp
				// warnings during an otherwise-successful shutdown.
				retErr = fmt.Errorf("remove temp %s: %w", tmpPath, rmErr)
			}
		}
	}()

	if _, cerr := copyWithContext(ctx, tmp, in); cerr != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return fmt.Errorf("copy failed: %w (close temp: %v)", cerr, closeErr)
		}
		return cerr
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return fmt.Errorf("sync temp %s: %w (close temp: %v)", tmpPath, syncErr, closeErr)
		}
		return fmt.Errorf("sync temp %s: %w", tmpPath, syncErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("close temp %s: %w", tmpPath, closeErr)
	}
	if renameErr := os.Rename(tmpPath, dst); renameErr != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, dst, renameErr)
	}
	cleanup = false

	if fsyncErr := fsyncDir(destDir); fsyncErr != nil {
		return fmt.Errorf("fsync dir %s: %w", destDir, fsyncErr)
	}
	return nil
}

// fsyncDir opens destDir and issues Sync so the directory entry update from
// the preceding Rename is flushed to disk. On platforms that do not support
// directory fsync (Windows), Open returns an error that we treat as a no-op.
func fsyncDir(destDir string) error {
	d, err := os.Open(destDir)
	if err != nil {
		// Directories cannot be opened for fsync on every platform; treat
		// that path as best-effort rather than a hard failure.
		if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrInvalid) {
			return nil
		}
		return err
	}
	if syncErr := d.Sync(); syncErr != nil {
		_ = d.Close()
		return syncErr
	}
	return d.Close()
}

// copyWithContext is io.Copy that checks ctx.Done() every 1 MiB chunk.
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total, werr
			}
			if w != n {
				return total, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}
