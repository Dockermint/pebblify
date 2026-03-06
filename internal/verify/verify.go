package verify

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/syndtr/goleveldb/leveldb"
	levopt "github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/Dockermint/Pebblify/internal/fsutil"
)

type Config struct {
	SamplePercent float64
	StopOnError   bool
	Verbose       bool
}

type Result struct {
	DBName         string
	TotalKeys      int64
	VerifiedKeys   int64
	MatchingKeys   int64
	MismatchedKeys int64
	MissingInDest  int64
	ExtraInDest    int64
	Errors         []string
	Duration       time.Duration
	Success        bool
}

func truncateBytes(b []byte, maxLen int) []byte {
	if len(b) <= maxLen {
		return b
	}
	return b[:maxLen]
}

func VerifyDB(srcPath, dstPath string, config *Config) (*Result, error) {
	result := &Result{
		DBName:  filepath.Base(srcPath),
		Success: true,
	}
	startTime := time.Now()

	srcDB, err := leveldb.OpenFile(srcPath, &levopt.Options{
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open source LevelDB %s: %w", srcPath, err)
	}
	defer func() { _ = srcDB.Close() }()

	dstDB, err := pebble.Open(dstPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open destination PebbleDB %s: %w", dstPath, err)
	}
	defer func() { _ = dstDB.Close() }()

	srcIt := srcDB.NewIterator(&util.Range{Start: nil, Limit: nil}, nil)
	defer srcIt.Release()

	sampleInterval := int64(1)
	if config.SamplePercent > 0 && config.SamplePercent < 100 {
		sampleInterval = int64(100 / config.SamplePercent)
	}

	var keyIndex int64
	for srcIt.Next() {
		result.TotalKeys++
		keyIndex++

		if sampleInterval > 1 && keyIndex%sampleInterval != 0 {
			continue
		}

		result.VerifiedKeys++
		srcKey := srcIt.Key()
		srcValue := srcIt.Value()

		dstValue, closer, err := dstDB.Get(srcKey)
		if err != nil {
			if err == pebble.ErrNotFound {
				result.MissingInDest++
				result.Success = false
				errMsg := fmt.Sprintf("key missing in dest: %x (first 32 bytes)", truncateBytes(srcKey, 32))
				result.Errors = append(result.Errors, errMsg)

				if config.Verbose {
					fmt.Printf("  [MISS] %s\n", errMsg)
				}
				if config.StopOnError {
					break
				}
				continue
			}
			return nil, fmt.Errorf("error reading from dest: %w", err)
		}

		if !bytes.Equal(srcValue, dstValue) {
			result.MismatchedKeys++
			result.Success = false
			errMsg := fmt.Sprintf("value mismatch for key %x: src=%d bytes, dst=%d bytes",
				truncateBytes(srcKey, 32), len(srcValue), len(dstValue))
			result.Errors = append(result.Errors, errMsg)

			if config.Verbose {
				fmt.Printf("  [MISMATCH] %s\n", errMsg)
			}
			if config.StopOnError {
				_ = closer.Close()
				break
			}
		} else {
			result.MatchingKeys++
		}

		_ = closer.Close()

		if config.Verbose && result.VerifiedKeys%100_000 == 0 {
			fmt.Printf("  [verify] %s: verified %d keys...\n", result.DBName, result.VerifiedKeys)
		}
	}

	if err := srcIt.Error(); err != nil {
		return nil, fmt.Errorf("source iterator error: %w", err)
	}

	if config.SamplePercent == 0 || config.SamplePercent >= 100 {
		dstIt, err := dstDB.NewIter(&pebble.IterOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create dest iterator: %w", err)
		}
		defer func() { _ = dstIt.Close() }()

		var dstKeyCount int64
		for dstIt.First(); dstIt.Valid(); dstIt.Next() {
			dstKeyCount++
		}

		if dstKeyCount > result.TotalKeys {
			result.ExtraInDest = dstKeyCount - result.TotalKeys
			result.Success = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("destination has %d extra keys", result.ExtraInDest))
		}
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

func Run(srcDir, dstDir string, config *Config) error {
	fmt.Printf("Starting verification...\n")
	fmt.Printf("  Source:      %s\n", srcDir)
	fmt.Printf("  Destination: %s\n", dstDir)
	fmt.Printf("  Sample:      %.1f%%\n\n", config.SamplePercent)

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	var dbPaths []struct{ src, dst string }
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			srcPath := filepath.Join(srcDir, e.Name())
			dstPath := filepath.Join(dstDir, e.Name())
			if fsutil.PathExists(dstPath) {
				dbPaths = append(dbPaths, struct{ src, dst string }{srcPath, dstPath})
			} else {
				fmt.Printf("  Warning: %s exists in source but not in destination\n", e.Name())
			}
		}
	}

	if len(dbPaths) == 0 {
		return fmt.Errorf("no .db directories found to verify")
	}

	fmt.Printf("Found %d databases to verify\n\n", len(dbPaths))

	allSuccess := true
	startTime := time.Now()

	for _, paths := range dbPaths {
		fmt.Printf("Verifying %s...\n", filepath.Base(paths.src))

		result, err := VerifyDB(paths.src, paths.dst, config)
		if err != nil {
			return fmt.Errorf("verification failed for %s: %w", paths.src, err)
		}

		if result.Success {
			fmt.Printf("  ✓ OK: %d/%d keys verified in %s\n",
				result.VerifiedKeys, result.TotalKeys, result.Duration.Truncate(time.Millisecond))
		} else {
			allSuccess = false
			fmt.Printf("  ✗ FAILED: %d matching, %d mismatched, %d missing\n",
				result.MatchingKeys, result.MismatchedKeys, result.MissingInDest)
			maxErrors := 5
			if len(result.Errors) < maxErrors {
				maxErrors = len(result.Errors)
			}
			for _, e := range result.Errors[:maxErrors] {
				fmt.Printf("    - %s\n", e)
			}
			if len(result.Errors) > 5 {
				fmt.Printf("    ... and %d more errors\n", len(result.Errors)-5)
			}
		}
	}

	totalDuration := time.Since(startTime)

	fmt.Println("\n" + strings.Repeat("=", 50))
	if allSuccess {
		fmt.Println("VERIFICATION PASSED - All databases match")
	} else {
		fmt.Println("VERIFICATION FAILED - Some databases have mismatches")
	}
	fmt.Printf("Total verification time: %s\n", totalDuration.Truncate(time.Second))
	fmt.Println(strings.Repeat("=", 50))

	if !allSuccess {
		return fmt.Errorf("verification failed")
	}
	return nil
}
