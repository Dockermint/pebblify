package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	levopt "github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/Dockermint/Pebblify/internal/fsutil"
	"github.com/Dockermint/Pebblify/internal/state"
)

func ScanAndPrepare(src, tmpData string, st *state.ConversionState, verbose bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read SRC dir: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		srcPath := filepath.Join(src, name)

		if e.IsDir() && strings.HasSuffix(name, ".db") {
			size, err := fsutil.DirSize(srcPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to compute size for db %s: %v\n", srcPath, err)
			}

			keys := estimateDBKeys(srcPath, verbose)

			tempPath := filepath.Join(tmpData, name+".tmp")
			st.DBs[name] = &state.DBStatus{
				Name:          name,
				SourcePath:    srcPath,
				TempPath:      tempPath,
				Status:        "pending",
				SizeBytes:     size,
				EstimatedKeys: keys,
				MigratedKeys:  0,
			}
			continue
		}

		dstPath := filepath.Join(tmpData, name)
		if e.IsDir() {
			if err := fsutil.CopyDir(srcPath, dstPath); err != nil {
				return fmt.Errorf("failed to copy directory %s -> %s: %w", srcPath, dstPath, err)
			}
		} else {
			if err := fsutil.CopyFile(srcPath, dstPath); err != nil {
				return fmt.Errorf("failed to copy file %s -> %s: %w", srcPath, dstPath, err)
			}
		}
	}

	return nil
}

func estimateDBKeys(path string, verbose bool) int64 {
	opts := &levopt.Options{
		ErrorIfMissing: true,
		ReadOnly:       true,
	}
	db, err := leveldb.OpenFile(path, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to open leveldb for key estimation %s: %v\n", path, err)
		return 0
	}
	defer db.Close()

	sizes, err := db.SizeOf([]util.Range{{Start: nil, Limit: nil}})
	if err != nil || len(sizes) == 0 || sizes[0] == 0 {
		return estimateKeysByFullScan(db, path, verbose)
	}
	totalSize := sizes[0]

	const (
		sampleSize    = 10_000
		minKeysForEst = 1_000
	)

	it := db.NewIterator(&util.Range{Start: nil, Limit: nil}, nil)
	defer it.Release()

	var sampleKeys int64
	var sampleBytes int64

	for it.Next() && sampleKeys < sampleSize {
		sampleKeys++
		sampleBytes += int64(len(it.Key()) + len(it.Value()) + 10)
	}

	if err := it.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: iterator error during sampling for %s: %v\n", path, err)
		return sampleKeys
	}

	if sampleKeys < minKeysForEst {
		return estimateKeysByFullScan(db, path, verbose)
	}

	if sampleBytes > 0 {
		estimatedTotal := int64(float64(totalSize) / float64(sampleBytes) * float64(sampleKeys))

		if verbose {
			fmt.Printf("  [estimate] %s: sampled %d keys (%.2f MB), total size %.2f MB, estimated %d keys\n",
				filepath.Base(path), sampleKeys, float64(sampleBytes)/1024/1024,
				float64(totalSize)/1024/1024, estimatedTotal)
		}

		return estimatedTotal
	}

	return sampleKeys
}

func estimateKeysByFullScan(db *leveldb.DB, path string, verbose bool) int64 {
	it := db.NewIterator(&util.Range{Start: nil, Limit: nil}, nil)
	defer it.Release()

	var count int64
	for it.Next() {
		count++
	}
	if err := it.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: iterator error during full scan for %s: %v\n", path, err)
	}

	if verbose {
		fmt.Printf("  [estimate] %s: full scan counted %d keys\n", filepath.Base(path), count)
	}

	return count
}
