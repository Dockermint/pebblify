// Package local implements a filesystem-backed store.Target.
//
// Uploads are performed via a filesystem rename when source and destination
// share a device; otherwise the file is copied and the source removed. The
// destination directory is created on demand.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

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

// Upload implements store.Target. It delivers localPath to <dir>/<remoteName>,
// preferring an atomic rename and falling back to copy-then-delete when the
// source and destination live on different devices.
func (t *LocalTarget) Upload(ctx context.Context, localPath, remoteName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if localPath == "" || remoteName == "" {
		return errors.New("local upload: localPath and remoteName must be non-empty")
	}

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return fmt.Errorf("local upload: mkdir %s: %w", t.dir, err)
	}

	dst := filepath.Join(t.dir, remoteName)

	if err := os.Rename(localPath, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return fmt.Errorf("local upload: rename %s -> %s: %w", localPath, dst, err)
	}

	if err := copyFile(ctx, localPath, dst); err != nil {
		return fmt.Errorf("local upload: copy %s -> %s: %w", localPath, dst, err)
	}
	if err := os.Remove(localPath); err != nil {
		return fmt.Errorf("local upload: remove source %s: %w", localPath, err)
	}
	return nil
}

// isCrossDevice reports whether err is a cross-device link error, the only
// case in which Upload falls back to copy-then-delete.
func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}
	return errors.Is(linkErr.Err, syscall.EXDEV)
}

// copyFile copies src to dst honoring ctx cancellation between chunks. The
// destination is created with mode 0o644 and fsynced before returning.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := copyWithContext(ctx, out, in); err != nil {
		return err
	}
	return out.Sync()
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
