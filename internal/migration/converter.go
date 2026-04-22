package migration

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/syndtr/goleveldb/leveldb"
	levopt "github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/Dockermint/Pebblify/internal/batcher"
	"github.com/Dockermint/Pebblify/internal/fsutil"
	"github.com/Dockermint/Pebblify/internal/metrics"
	"github.com/Dockermint/Pebblify/internal/progress"
	"github.com/Dockermint/Pebblify/internal/prom"
	"github.com/Dockermint/Pebblify/internal/state"
)

// RunConfig bundles the tunables for a LevelDB to PebbleDB conversion
// run. Zero values for Workers or BatchMemory trigger the pipeline's
// defaults.
type RunConfig struct {
	Workers        int
	BatchMemory    int
	Verbose        bool
	MetricsEnabled bool
}

// RunLevelToPebble performs a fresh conversion from the LevelDB tree at
// src into a new PebbleDB tree under out, using tmpRoot as the working
// directory for checkpoints and staged output. It returns an error if the
// source is missing, the output already contains a data directory, or any
// sub-database conversion fails.
func RunLevelToPebble(src, out string, cfg *RunConfig, tmpRoot string) error {
	statePath := filepath.Join(tmpRoot, state.StateFileName)
	tmpData := filepath.Join(tmpRoot, "data")
	baseTmpDir := filepath.Dir(tmpRoot)

	if !fsutil.PathExists(src) {
		return fmt.Errorf("SRC path does not exist: %s", src)
	}

	srcSize, err := fsutil.DirSize(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to compute size of SRC (%s): %v\n", src, err)
	}

	if srcSize > 0 {
		fsutil.CheckDiskSpace(baseTmpDir, srcSize, cfg.Verbose)
	}

	if err := os.MkdirAll(tmpData, 0o755); err != nil {
		return fmt.Errorf("failed to create temp data dir %s: %w", tmpData, err)
	}

	if !fsutil.PathExists(out) {
		if err := os.MkdirAll(out, 0o755); err != nil {
			cleanTmp(tmpRoot)
			return fmt.Errorf("failed to create OUT directory %s: %w", out, err)
		}
	} else if fsutil.PathExists(filepath.Join(out, "data")) {
		cleanTmp(tmpRoot)
		return fmt.Errorf("OUT directory %s already contains a data directory", out)
	}

	st := &state.ConversionState{
		Version:     state.StateVersion,
		Src:         src,
		Out:         out,
		StartedAt:   time.Now().UTC(),
		LastUpdated: time.Now().UTC(),
		DBs:         make(map[string]*state.DBStatus),
	}

	fmt.Println("Scanning source directory and estimating keys...")
	if err := ScanAndPrepare(src, tmpData, st, cfg.Verbose); err != nil {
		cleanTmp(tmpRoot)
		return fmt.Errorf("failed to scan SRC: %w", err)
	}

	var totalKeys int64
	for _, db := range st.DBs {
		totalKeys += db.EstimatedKeys
	}
	st.TotalKeysEstimated = totalKeys

	if err := state.Update(statePath, st, nil); err != nil {
		cleanTmp(tmpRoot)
		return fmt.Errorf("failed to write initial state file: %w", err)
	}

	dbList := state.CollectPendingDBs(st)
	if len(dbList) == 0 {
		fmt.Println("No .db directories found to convert.")
		return nil
	}

	workers := fsutil.NormalizeWorkers(cfg.Workers, len(dbList))

	fmt.Printf("\nStarting LevelDB → PebbleDB migration\n")
	fmt.Printf("  SRC: %s\n", src)
	fmt.Printf("  OUT: %s\n", out)
	fmt.Printf("  TMP: %s\n", tmpRoot)
	fmt.Printf("  workers: %d\n", workers)
	fmt.Printf("  batch memory: %d MB\n", cfg.BatchMemory)
	fmt.Printf("  verbose: %v\n\n", cfg.Verbose)

	if srcSize > 0 {
		fmt.Printf("Source data size: %s (%d bytes)\n\n", fsutil.FormatBytes(srcSize), srcSize)
	}

	fmt.Println("Discovered DBs to convert:")
	for _, db := range dbList {
		fmt.Printf("  - %s (size: %s, est. keys: %d)\n",
			db.Name, fsutil.FormatBytes(db.SizeBytes), db.EstimatedKeys)
	}
	fmt.Println()

	m := metrics.New()

	doneCh := make(chan struct{})
	go progress.Monitor(st, totalKeys, st.StartedAt, m, doneCh)

	batchConfig := &batcher.Config{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: cfg.BatchMemory,
	}

	startRun := time.Now()
	if err := convertAllDBs(statePath, st, workers, batchConfig, m, cfg.Verbose, cfg.MetricsEnabled); err != nil {
		close(doneCh)
		return err
	}
	close(doneCh)

	finalData := filepath.Join(out, "data")
	if fsutil.PathExists(finalData) {
		cleanTmp(tmpRoot)
		return fmt.Errorf("final data dir already exists: %s", finalData)
	}
	if err := fsutil.MoveDir(tmpData, finalData); err != nil {
		cleanTmp(tmpRoot)
		return fmt.Errorf("failed to move %s to %s: %w", tmpData, finalData, err)
	}

	outSize, err := fsutil.DirSize(finalData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to compute size of OUT (%s): %v\n", finalData, err)
	}

	if err := os.RemoveAll(tmpRoot); err != nil {
		return fmt.Errorf("failed to cleanup temp dir %s: %w", tmpRoot, err)
	}

	m.PrintSummary()

	fmt.Println("\nConversion completed successfully.")
	fmt.Printf("New Pebble-backed data directory: %s\n", finalData)

	runDuration := time.Since(startRun)
	totalDuration := time.Since(st.StartedAt)

	fmt.Println()
	fmt.Printf("This run duration:   %s\n", runDuration.Truncate(time.Second))
	fmt.Printf("Total elapsed time:  %s (since first start)\n", totalDuration.Truncate(time.Second))

	if srcSize > 0 && outSize > 0 {
		ratio := float64(outSize) / float64(srcSize) * 100
		fmt.Println()
		fmt.Println("Size summary:")
		fmt.Printf("  Source (LevelDB) data:  %s (%d bytes)\n", fsutil.FormatBytes(srcSize), srcSize)
		fmt.Printf("  Target (PebbleDB) data: %s (%d bytes)\n", fsutil.FormatBytes(outSize), outSize)
		fmt.Printf("  Size ratio:             %.1f %%\n", ratio)
	}

	return nil
}

// RunRecover resumes an interrupted conversion by reading the state file
// under tmpRoot and re-running the pipeline on every database that is not
// yet marked done. Each resumed database restarts from its last on-disk
// checkpoint key.
func RunRecover(workers, batchMemory int, tmpRoot string, verbose bool, metricsEnabled bool) error {
	statePath := filepath.Join(tmpRoot, state.StateFileName)

	st, err := state.Read(statePath)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	fmt.Println("Recovery state summary:")
	fmt.Printf("  Started at:    %s\n", st.StartedAt.Format(time.RFC3339))
	fmt.Printf("  Last updated:  %s\n", st.LastUpdated.Format(time.RFC3339))
	fmt.Printf("  Source:        %s\n", st.Src)
	fmt.Printf("  Output:        %s\n\n", st.Out)

	var doneCount, inProgressCount, pendingCount, failedCount int

	fmt.Println("Database statuses:")
	for _, db := range st.DBs {
		switch db.Status {
		case "done":
			doneCount++
			fmt.Printf("  ✓ %s: done (%d keys)\n", db.Name, db.MigratedKeys)
		case "in_progress":
			inProgressCount++
			p := float64(0)
			if db.EstimatedKeys > 0 {
				p = float64(db.MigratedKeys) / float64(db.EstimatedKeys) * 100
			}
			checkpointInfo := ""
			if db.GetLastCheckpointKey() != nil {
				checkpointInfo = fmt.Sprintf(", checkpoint at %s", db.CheckpointTime.Format("15:04:05"))
			}
			fmt.Printf("  ⟳ %s: in progress (%.1f%%, %d/%d keys%s)\n",
				db.Name, p, db.MigratedKeys, db.EstimatedKeys, checkpointInfo)
		case "pending":
			pendingCount++
			fmt.Printf("  ○ %s: pending (%d keys estimated)\n", db.Name, db.EstimatedKeys)
		case "failed":
			failedCount++
			fmt.Printf("  ✗ %s: failed - %s\n", db.Name, db.Error)
		}
	}

	fmt.Printf("\nSummary: %d done, %d in progress, %d pending, %d failed\n",
		doneCount, inProgressCount, pendingCount, failedCount)

	if st.TotalKeysEstimated > 0 {
		overallProgress := float64(st.TotalKeysMigrated) / float64(st.TotalKeysEstimated) * 100
		fmt.Printf("Overall progress: %.1f%% (%d / %d keys)\n\n",
			overallProgress, st.TotalKeysMigrated, st.TotalKeysEstimated)
	}

	dbList := state.CollectPendingDBs(st)
	if len(dbList) == 0 {
		fmt.Println("All databases are already converted. Moving to finalization...")
		return finalizeConversion(st, tmpRoot)
	}

	fmt.Printf("Resuming conversion for %d database(s)...\n\n", len(dbList))

	workers = fsutil.NormalizeWorkers(workers, len(dbList))
	m := metrics.New()

	doneCh := make(chan struct{})
	go progress.Monitor(st, st.TotalKeysEstimated, st.StartedAt, m, doneCh)

	batchConfig := &batcher.Config{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: batchMemory,
	}

	if err := convertAllDBs(statePath, st, workers, batchConfig, m, verbose, metricsEnabled); err != nil {
		close(doneCh)
		return err
	}
	close(doneCh)

	m.PrintSummary()

	return finalizeConversion(st, tmpRoot)
}

func finalizeConversion(st *state.ConversionState, tmpRoot string) error {
	tmpData := filepath.Join(tmpRoot, "data")
	finalData := filepath.Join(st.Out, "data")

	if !fsutil.PathExists(finalData) {
		fmt.Printf("Moving converted data to %s...\n", finalData)
		if err := fsutil.MoveDir(tmpData, finalData); err != nil {
			cleanTmp(tmpRoot)
			return fmt.Errorf("failed to move data: %w", err)
		}
	}

	fmt.Printf("Cleaning up temp directory %s...\n", tmpRoot)
	if err := os.RemoveAll(tmpRoot); err != nil {
		return fmt.Errorf("failed to cleanup: %w", err)
	}

	fmt.Println("\nConversion completed successfully!")
	fmt.Printf("PebbleDB data directory: %s\n", finalData)

	srcSize, _ := fsutil.DirSize(st.Src)
	outSize, _ := fsutil.DirSize(finalData)
	if srcSize > 0 && outSize > 0 {
		ratio := float64(outSize) / float64(srcSize) * 100
		fmt.Println()
		fmt.Println("Size summary:")
		fmt.Printf("  Source (LevelDB) data:  %s\n", fsutil.FormatBytes(srcSize))
		fmt.Printf("  Target (PebbleDB) data: %s\n", fsutil.FormatBytes(outSize))
		fmt.Printf("  Size ratio:             %.1f %%\n", ratio)
	}

	return nil
}

func convertAllDBs(statePath string, st *state.ConversionState, workers int, batchConfig *batcher.Config, m *metrics.Metrics, verbose bool, metricsEnabled bool) error {
	dbList := state.CollectPendingDBs(st)
	if len(dbList) == 0 {
		return nil
	}

	jobs := make(chan *state.DBStatus)
	errCh := make(chan error, len(dbList))
	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for dbst := range jobs {
				if err := convertSingleDB(statePath, st, dbst, batchConfig, m, verbose, metricsEnabled); err != nil {
					errCh <- err
				}
			}
		})
	}

	for _, dbst := range dbList {
		if dbst.Status == "done" {
			continue
		}
		jobs <- dbst
	}
	close(jobs)

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func convertSingleDB(statePath string, st *state.ConversionState, dbst *state.DBStatus, batchConfig *batcher.Config, m *metrics.Metrics, verbose bool, metricsEnabled bool) error {
	fmt.Printf("\nConverting DB %s", dbst.Name)

	isResume := dbst.Status == "in_progress" && dbst.GetLastCheckpointKey() != nil
	if isResume {
		fmt.Printf(" (resuming from checkpoint, %d keys already migrated)\n", dbst.MigratedKeys)
	} else {
		fmt.Println()
		if fsutil.PathExists(dbst.TempPath) {
			if err := os.RemoveAll(dbst.TempPath); err != nil {
				return fmt.Errorf("failed to remove existing temp db dir %s: %w", dbst.TempPath, err)
			}
		}
		dbst.MigratedKeys = 0
		dbst.BytesRead = 0
		dbst.BytesWritten = 0
	}

	if err := state.Update(statePath, st, func() {
		dbst.Status = "in_progress"
		dbst.Error = ""
	}); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	if metricsEnabled {
		updateDBGauges(st)
	}

	srcDB, err := leveldb.OpenFile(dbst.SourcePath, &levopt.Options{
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return markDBFailed(statePath, st, dbst, err)
	}
	defer func() { _ = srcDB.Close() }()

	dstDB, err := pebble.Open(dbst.TempPath, &pebble.Options{
		DisableAutomaticCompactions: true,
		L0CompactionFileThreshold:   math.MaxInt,
		L0CompactionThreshold:       math.MaxInt,
		L0StopWritesThreshold:       math.MaxInt,
		MaxConcurrentCompactions:    func() int { return 1 },
		MaxOpenFiles:                100,
		Levels: []pebble.LevelOptions{
			{
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				TargetFileSize: 128 << 20,
			},
		},
	})
	if err != nil {
		return markDBFailed(statePath, st, dbst, err)
	}
	defer func() { _ = dstDB.Close() }()

	var iterRange *util.Range
	if isResume {
		checkpointKey := dbst.GetLastCheckpointKey()
		startKey := make([]byte, len(checkpointKey)+1)
		copy(startKey, checkpointKey)
		startKey[len(checkpointKey)] = 0
		iterRange = &util.Range{Start: startKey, Limit: nil}
	} else {
		iterRange = &util.Range{Start: nil, Limit: nil}
	}

	it := srcDB.NewIterator(iterRange, nil)
	defer it.Release()

	b := batcher.New(dstDB, batchConfig)
	defer func() { _ = b.Close() }()

	const checkpointInterval = 50_000
	count := dbst.MigratedKeys
	var lastKey []byte
	bytesRead := dbst.BytesRead
	bytesWritten := dbst.BytesWritten

	var intervalKeys int64
	var intervalBytes int64
	lastMetricsUpdate := time.Now()

	for it.Next() {
		key := it.Key()
		val := it.Value()

		lastKey = make([]byte, len(key))
		copy(lastKey, key)

		entrySize := int64(len(key) + len(val))
		bytesRead += entrySize
		intervalBytes += entrySize

		if err := b.Add(key, val); err != nil {
			return markDBFailed(statePath, st, dbst, err)
		}

		count++
		intervalKeys++
		bytesWritten += entrySize

		if count%int64(checkpointInterval) == 0 {
			if err := b.Commit(); err != nil {
				return markDBFailed(statePath, st, dbst, err)
			}

			if metricsEnabled {
				prom.BatchCommits.Inc()
				prom.Checkpoints.Inc()
			}

			if err := state.Update(statePath, st, func() {
				dbst.MigratedKeys = count
				dbst.SetLastCheckpointKey(lastKey)
				dbst.CheckpointTime = time.Now()
				dbst.BytesRead = bytesRead
				dbst.BytesWritten = bytesWritten
				st.TotalKeysMigrated = state.CalculateTotalMigrated(st)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to write checkpoint: %v\n", err)
			}

			if time.Since(lastMetricsUpdate) >= time.Second {
				m.RecordKeys(dbst.Name, intervalKeys, intervalBytes, intervalBytes)

				if metricsEnabled {
					prom.KeysProcessed.Add(float64(intervalKeys))
					prom.BytesRead.Add(float64(intervalBytes))
					prom.BytesWritten.Add(float64(intervalBytes))
					kps, bps := m.GetCurrentThroughput()
					prom.KeysPerSecond.Set(kps)
					prom.BytesPerSecond.Set(bps * 1024 * 1024)
				}

				intervalKeys = 0
				intervalBytes = 0
				lastMetricsUpdate = time.Now()
			}

			if verbose && count%100_000 == 0 {
				fmt.Printf("  %s: migrated %d keys (checkpoint saved)...\n", dbst.Name, count)
			}
		}
	}

	if intervalKeys > 0 {
		m.RecordKeys(dbst.Name, intervalKeys, intervalBytes, intervalBytes)

		if metricsEnabled {
			prom.KeysProcessed.Add(float64(intervalKeys))
			prom.BytesRead.Add(float64(intervalBytes))
			prom.BytesWritten.Add(float64(intervalBytes))
		}
	}

	if err := b.Commit(); err != nil {
		return markDBFailed(statePath, st, dbst, err)
	}

	if metricsEnabled {
		prom.BatchCommits.Inc()
	}

	if err := it.Error(); err != nil {
		return markDBFailed(statePath, st, dbst, err)
	}

	if err := dstDB.Flush(); err != nil {
		return markDBFailed(statePath, st, dbst, err)
	}

	finalPath := strings.TrimSuffix(dbst.TempPath, ".tmp")
	if finalPath != dbst.TempPath {
		if err := os.Rename(dbst.TempPath, finalPath); err != nil {
			return markDBFailed(statePath, st, dbst, err)
		}
		dbst.TempPath = finalPath
	}

	_, _, avgKey, avgVal := b.Stats()
	m.FinalizeDB(dbst.Name, avgKey, avgVal)

	if err := state.Update(statePath, st, func() {
		dbst.Status = "done"
		dbst.Error = ""
		dbst.MigratedKeys = count
		dbst.SetLastCheckpointKey(nil)
		dbst.BytesRead = bytesRead
		dbst.BytesWritten = bytesWritten
		st.TotalKeysMigrated = state.CalculateTotalMigrated(st)
	}); err != nil {
		return fmt.Errorf("failed to finalize state: %w", err)
	}

	if metricsEnabled {
		updateDBGauges(st)
	}

	fmt.Printf("\nDB %s converted successfully (%d keys)\n", dbst.Name, count)
	return nil
}

func updateDBGauges(st *state.ConversionState) {
	counts := map[string]float64{
		"pending":     0,
		"in_progress": 0,
		"done":        0,
		"failed":      0,
	}
	for _, db := range st.DBs {
		counts[db.Status]++
	}
	for status, count := range counts {
		prom.Databases.WithLabelValues(status).Set(count)
	}
}

func cleanTmp(tmpRoot string) {
	_ = os.RemoveAll(tmpRoot)
}

func markDBFailed(statePath string, st *state.ConversionState, dbst *state.DBStatus, originalErr error) error {
	_ = state.Update(statePath, st, func() {
		dbst.Status = "failed"
		dbst.Error = originalErr.Error()
	})
	return fmt.Errorf("failed processing %s: %w", dbst.Name, originalErr)
}
