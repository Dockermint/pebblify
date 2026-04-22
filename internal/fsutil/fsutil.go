package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// PathExists reports whether p resolves to an existing filesystem entry.
// A stat error of any kind is reported as non-existence.
func PathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// IsDirEmpty reports whether the directory at p contains no entries.
// It returns an error if p cannot be opened or read.
func IsDirEmpty(p string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// CopyFile copies the regular file at src to dst, creating parent
// directories as needed and fsyncing the destination before returning.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

// CopyDir recursively copies the directory tree rooted at src into dst.
// Files are copied through CopyFile and sub-directories are created with
// 0o755 permissions.
func CopyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())

		if e.IsDir() {
			if err := CopyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// MoveDir renames src to dst, falling back to a recursive copy followed
// by removal when the two paths span different filesystems (EXDEV).
func MoveDir(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		var linkErr *os.LinkError
		if !errors.As(err, &linkErr) || linkErr.Err != syscall.EXDEV {
			return err
		}

		if err := CopyDir(src, dst); err != nil {
			return err
		}
		if err := os.RemoveAll(src); err != nil {
			return err
		}
	}
	return nil
}

// DirSize returns the total size in bytes of every regular file under
// root. Symlinks and non-regular entries are ignored.
func DirSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// FormatBytes renders n as a human-readable size using binary (KiB, MiB,
// GiB, TiB) units with two decimals of precision.
func FormatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)

	f := float64(n)

	switch {
	case n >= tb:
		return fmt.Sprintf("%.2f TiB", f/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.2f GiB", f/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.2f MiB", f/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.2f KiB", f/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// GetAvailableSpace returns the number of bytes available to unprivileged
// users on the filesystem backing path. When path does not exist the
// closest existing ancestor is probed instead.
func GetAvailableSpace(path string) (uint64, error) {
	var stat syscall.Statfs_t
	checkPath := path
	for !PathExists(checkPath) {
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			break
		}
		checkPath = parent
	}
	if err := syscall.Statfs(checkPath, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// CheckDiskSpace warns on stderr when tmpDir has less free space than
// 1.5x srcSize, the rule-of-thumb headroom required during migration.
// When verbose is true and the check passes, the figures are printed on
// stdout for operator visibility.
func CheckDiskSpace(tmpDir string, srcSize int64, verbose bool) {
	available, err := GetAvailableSpace(tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not check available disk space: %v\n", err)
		return
	}

	requiredSpace := uint64(float64(srcSize) * 1.5)

	if available < requiredSpace {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Low disk space!\n")
		fmt.Fprintf(os.Stderr, "   Available in %s: %s\n", tmpDir, FormatBytes(int64(available)))
		fmt.Fprintf(os.Stderr, "   Estimated required: %s (1.5x source size)\n", FormatBytes(int64(requiredSpace)))
		fmt.Fprintf(os.Stderr, "   Consider using --tmp-dir to specify a directory with more space.\n\n")
	} else if verbose {
		fmt.Printf("Disk space check: %s available, ~%s required\n\n",
			FormatBytes(int64(available)), FormatBytes(int64(requiredSpace)))
	}
}

// NormalizeWorkers clamps workers to a sensible range for numJobs. A zero
// or negative value defaults to runtime.NumCPU; the result is capped at
// numJobs and floored at one.
func NormalizeWorkers(workers int, numJobs int) int {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > numJobs {
		workers = numJobs
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}
