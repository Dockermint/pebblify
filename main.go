package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/syndtr/goleveldb/leveldb"
	levopt "github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	tmpDirName    = ".pebblify-tmp"
	stateFileName = "state.json"
	lockFileName  = "pebblify.lock"
	stateVersion  = 0
)

var (
	Version  = "dev"
	Revision = "unknown"
)

var verbose bool

type DBStatus struct {
	Name                 string    `json:"name"`
	SourcePath           string    `json:"source_path"`
	TempPath             string    `json:"temp_path"`
	Status               string    `json:"status"`
	Error                string    `json:"error,omitempty"`
	SizeBytes            int64     `json:"size_bytes"`
	EstimatedKeys        int64     `json:"estimated_keys"`
	MigratedKeys         int64     `json:"migrated_keys"`
	LastCheckpointKeyB64 string    `json:"last_checkpoint_key_b64,omitempty"`
	CheckpointTime       time.Time `json:"checkpoint_time"`
	BytesRead            int64     `json:"bytes_read"`
	BytesWritten         int64     `json:"bytes_written"`
	KeysPerSecond        float64   `json:"keys_per_second,omitempty"`
}

func (d *DBStatus) GetLastCheckpointKey() []byte {
	if d.LastCheckpointKeyB64 == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(d.LastCheckpointKeyB64)
	if err != nil {
		return nil
	}
	return data
}

func (d *DBStatus) SetLastCheckpointKey(key []byte) {
	if key == nil {
		d.LastCheckpointKeyB64 = ""
	} else {
		d.LastCheckpointKeyB64 = base64.StdEncoding.EncodeToString(key)
	}
}

type ConversionState struct {
	Version            int                  `json:"version"`
	Src                string               `json:"src"`
	Out                string               `json:"out"`
	StartedAt          time.Time            `json:"started_at"`
	LastUpdated        time.Time            `json:"last_updated"`
	DBs                map[string]*DBStatus `json:"dbs"`
	TotalKeysEstimated int64                `json:"total_keys_estimated"`
	TotalKeysMigrated  int64                `json:"total_keys_migrated"`
}

var stateMu sync.Mutex

type Metrics struct {
	mu                 sync.RWMutex
	TotalKeysProcessed int64
	TotalBytesRead     int64
	TotalBytesWritten  int64
	DBMetrics          map[string]*DBMetricsData
	StartTime          time.Time
	LastUpdate         time.Time
	recentSamples      []throughputSample
}

type DBMetricsData struct {
	Name          string
	KeysProcessed int64
	BytesRead     int64
	BytesWritten  int64
	StartTime     time.Time
	EndTime       time.Time
	Duration      time.Duration
	AvgKeySize    float64
	AvgValueSize  float64
	KeysPerSecond float64
	MBPerSecond   float64
}

type throughputSample struct {
	timestamp time.Time
	keys      int64
	bytes     int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		DBMetrics:     make(map[string]*DBMetricsData),
		StartTime:     time.Now(),
		LastUpdate:    time.Now(),
		recentSamples: make([]throughputSample, 0, 100),
	}
}

func (m *Metrics) RecordKeys(dbName string, keys int64, bytesRead, bytesWritten int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalKeysProcessed += keys
	m.TotalBytesRead += bytesRead
	m.TotalBytesWritten += bytesWritten
	m.LastUpdate = time.Now()

	m.recentSamples = append(m.recentSamples, throughputSample{
		timestamp: time.Now(),
		keys:      keys,
		bytes:     bytesRead,
	})

	cutoff := time.Now().Add(-30 * time.Second)
	newSamples := make([]throughputSample, 0, len(m.recentSamples))
	for _, s := range m.recentSamples {
		if s.timestamp.After(cutoff) {
			newSamples = append(newSamples, s)
		}
	}
	m.recentSamples = newSamples

	dbm, exists := m.DBMetrics[dbName]
	if !exists {
		dbm = &DBMetricsData{Name: dbName, StartTime: time.Now()}
		m.DBMetrics[dbName] = dbm
	}
	dbm.KeysProcessed += keys
	dbm.BytesRead += bytesRead
	dbm.BytesWritten += bytesWritten
}

func (m *Metrics) FinalizeDB(dbName string, avgKeySize, avgValueSize float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if dbm, exists := m.DBMetrics[dbName]; exists {
		dbm.EndTime = time.Now()
		dbm.Duration = dbm.EndTime.Sub(dbm.StartTime)
		dbm.AvgKeySize = avgKeySize
		dbm.AvgValueSize = avgValueSize

		if dbm.Duration.Seconds() > 0 {
			dbm.KeysPerSecond = float64(dbm.KeysProcessed) / dbm.Duration.Seconds()
			dbm.MBPerSecond = float64(dbm.BytesRead) / 1024 / 1024 / dbm.Duration.Seconds()
		}
	}
}

func (m *Metrics) GetCurrentThroughput() (keysPerSec float64, mbPerSec float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.recentSamples) < 2 {
		return 0, 0
	}

	var totalKeys, totalBytes int64
	firstTime := m.recentSamples[0].timestamp
	lastTime := m.recentSamples[len(m.recentSamples)-1].timestamp

	for _, s := range m.recentSamples {
		totalKeys += s.keys
		totalBytes += s.bytes
	}

	duration := lastTime.Sub(firstTime).Seconds()
	if duration > 0 {
		keysPerSec = float64(totalKeys) / duration
		mbPerSec = float64(totalBytes) / 1024 / 1024 / duration
	}

	return keysPerSec, mbPerSec
}

func (m *Metrics) PrintSummary() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	totalDuration := time.Since(m.StartTime)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("CONVERSION METRICS SUMMARY")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\nGlobal Statistics:\n")
	fmt.Printf("  Total duration:      %s\n", totalDuration.Truncate(time.Second))
	fmt.Printf("  Total keys:          %d\n", m.TotalKeysProcessed)
	fmt.Printf("  Total data read:     %s\n", formatBytes(m.TotalBytesRead))
	fmt.Printf("  Total data written:  %s\n", formatBytes(m.TotalBytesWritten))

	if totalDuration.Seconds() > 0 {
		fmt.Printf("  Avg throughput:      %.0f keys/sec, %.2f MB/sec\n",
			float64(m.TotalKeysProcessed)/totalDuration.Seconds(),
			float64(m.TotalBytesRead)/1024/1024/totalDuration.Seconds())
	}

	if m.TotalBytesRead > 0 {
		compressionRatio := float64(m.TotalBytesWritten) / float64(m.TotalBytesRead) * 100
		fmt.Printf("  Write/Read ratio:    %.1f%%\n", compressionRatio)
	}

	if len(m.DBMetrics) > 0 {
		fmt.Printf("\nPer-Database Statistics:\n")
		for _, dbm := range m.DBMetrics {
			fmt.Printf("\n  %s:\n", dbm.Name)
			fmt.Printf("    Keys:        %d\n", dbm.KeysProcessed)
			fmt.Printf("    Duration:    %s\n", dbm.Duration.Truncate(time.Second))
			if dbm.KeysPerSecond > 0 {
				fmt.Printf("    Throughput:  %.0f keys/sec, %.2f MB/sec\n", dbm.KeysPerSecond, dbm.MBPerSecond)
			}
			if dbm.AvgKeySize > 0 {
				fmt.Printf("    Avg sizes:   key=%.0f B, value=%.0f B\n", dbm.AvgKeySize, dbm.AvgValueSize)
			}
		}
	}

	fmt.Println(strings.Repeat("=", 60))
}

type BatchConfig struct {
	MinBatchSize   int
	MaxBatchSize   int
	TargetMemoryMB int
}

func DefaultBatchConfig() *BatchConfig {
	return &BatchConfig{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: 64,
	}
}

type AdaptiveBatcher struct {
	config       *BatchConfig
	batch        *pebble.Batch
	db           *pebble.DB
	currentCount int64
	currentBytes int64
	totalKeys    int64
	totalBytes   int64
	avgKeySize   float64
	avgValueSize float64
	onCommit     func(keys int64, bytes int64)
}

func NewAdaptiveBatcher(db *pebble.DB, config *BatchConfig) *AdaptiveBatcher {
	if config == nil {
		config = DefaultBatchConfig()
	}
	return &AdaptiveBatcher{
		config: config,
		batch:  db.NewBatch(),
		db:     db,
	}
}

func (ab *AdaptiveBatcher) SetOnCommit(fn func(keys int64, bytes int64)) {
	ab.onCommit = fn
}

func (ab *AdaptiveBatcher) Add(key, value []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	v := make([]byte, len(value))
	copy(v, value)

	if err := ab.batch.Set(k, v, nil); err != nil {
		return err
	}

	entrySize := int64(len(k) + len(v))
	ab.currentCount++
	ab.currentBytes += entrySize
	ab.totalKeys++
	ab.totalBytes += entrySize

	targetBytes := int64(ab.config.TargetMemoryMB) * 1024 * 1024
	shouldCommit := ab.currentBytes >= targetBytes || ab.currentCount >= int64(ab.config.MaxBatchSize)

	if shouldCommit {
		if err := ab.Commit(); err != nil {
			return err
		}
	}

	return nil
}

func (ab *AdaptiveBatcher) Commit() error {
	if ab.currentCount == 0 {
		return nil
	}

	if err := ab.batch.Commit(pebble.Sync); err != nil {
		return err
	}

	if ab.onCommit != nil {
		ab.onCommit(ab.currentCount, ab.currentBytes)
	}

	ab.batch.Reset()
	ab.currentCount = 0
	ab.currentBytes = 0

	if ab.totalKeys > 0 {
		avgEntrySize := float64(ab.totalBytes) / float64(ab.totalKeys)
		ab.avgKeySize = avgEntrySize * 0.1
		ab.avgValueSize = avgEntrySize * 0.9
	}

	return nil
}

func (ab *AdaptiveBatcher) Stats() (totalKeys, totalBytes int64, avgKeySize, avgValueSize float64) {
	return ab.totalKeys, ab.totalBytes, ab.avgKeySize, ab.avgValueSize
}

func (ab *AdaptiveBatcher) Close() error {
	return ab.batch.Close()
}

type VerifyConfig struct {
	SamplePercent float64
	StopOnError   bool
	Verbose       bool
}

type VerifyResult struct {
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

func verifyDB(srcPath, dstPath string, config *VerifyConfig) (*VerifyResult, error) {
	result := &VerifyResult{
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
	defer srcDB.Close()

	dstDB, err := pebble.Open(dstPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open destination PebbleDB %s: %w", dstPath, err)
	}
	defer dstDB.Close()

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
				closer.Close()
				break
			}
		} else {
			result.MatchingKeys++
		}

		closer.Close()

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
		defer dstIt.Close()

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

func truncateBytes(b []byte, maxLen int) []byte {
	if len(b) <= maxLen {
		return b
	}
	return b[:maxLen]
}

func acquireLock(tmpRoot string) (func(), error) {
	lockPath := filepath.Join(tmpRoot, lockFileName)

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another pebblify instance seems to be running (lock file: %s). If not, delete it manually", lockPath)
		}
		return nil, err
	}
	defer f.Close()

	_, _ = fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))

	unlock := func() {
		_ = os.Remove(lockPath)
	}

	return unlock, nil
}

func updateState(statePath string, state *ConversionState, update func()) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	if update != nil {
		update()
	}
	state.LastUpdated = time.Now()
	return writeStateAtomic(statePath, state)
}

func writeStateAtomic(path string, state *ConversionState) error {
	tmpPath := path + ".tmp"

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func printVersion() {
	fmt.Printf("pebblify %s\n", Version)
	fmt.Printf("  revision:  %s\n", Revision)
	fmt.Printf("  platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  go:        %s\n", runtime.Version())
}

func readState(path string) (*ConversionState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var state ConversionState
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return nil, err
	}

	return &state, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	if cmd == "-V" || cmd == "--version" || cmd == "version" {
		printVersion()
		os.Exit(0)
	}

	switch cmd {
	case "level-to-pebble":
		levelToPebbleCmd(os.Args[2:])
	case "recover":
		recoverCmd(os.Args[2:])
	case "verify":
		verifyCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `pebblify %s – LevelDB → PebbleDB migration tool

Usage:
  pebblify <command> [options]

Commands:
  level-to-pebble   Convert a Tendermint/CometBFT data/ directory from LevelDB to PebbleDB
  recover           Resume a previously interrupted conversion
  verify            Verify that converted data matches the source
  version           Show version information

Options for level-to-pebble:
  -f, --force       Overwrite existing temporary state
  -w, --workers N   Max concurrent DB conversions (0 = auto, based on CPU)
  -v, --verbose     Enable verbose output
  --batch-memory M  Target memory per batch in MB (default: 64)
  --tmp-dir DIR     Directory where .pebblify-tmp/ will be created
                    (default: system temp, e.g. /tmp)
                    Use this if /tmp is too small (e.g. tmpfs in RAM)

Options for recover:
  -w, --workers N   Max concurrent DB conversions (0 = auto)
  -v, --verbose     Enable verbose output
  --tmp-dir DIR     Directory containing .pebblify-tmp/ (must match conversion)

Options for verify:
  -s, --sample P    Percentage of keys to verify (default: 100 = all)
  --stop-on-error   Stop at first mismatch
  -v, --verbose     Show each key being verified

Global flags:
  -h, --help        Show this help
  -V, --version     Show version and exit

Examples:
  # Convert using /var/tmp instead of /tmp (creates /var/tmp/.pebblify-tmp/)
  pebblify level-to-pebble --tmp-dir /var/tmp ~/.gaia/data ./output

  # Resume an interrupted conversion (same --tmp-dir as before)
  pebblify recover --tmp-dir /var/tmp

  # Verify the converted data
  pebblify verify ~/.gaia/data ./output/data

`, Version)
}

func levelToPebbleCmd(args []string) {
	fs := flag.NewFlagSet("level-to-pebble", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing temporary state")
	fs.BoolVar(force, "f", false, "alias for --force")
	workers := fs.Int("workers", 0, "max concurrent DB conversions (0 = auto)")
	fs.IntVar(workers, "w", 0, "alias for --workers")
	batchMemory := fs.Int("batch-memory", 64, "target memory per batch in MB")
	tmpDir := fs.String("tmp-dir", "", "directory where .pebblify-tmp will be created (default: system temp)")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose output")
	fs.BoolVar(&verbose, "v", false, "alias for --verbose")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintf(os.Stderr, "invalid arguments for level-to-pebble\n\n")
		fmt.Fprintf(os.Stderr, "Usage: pebblify level-to-pebble [options] SRC OUT\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	src := rest[0]
	out := rest[1]

	baseTmpDir := os.TempDir()
	if *tmpDir != "" {
		baseTmpDir = *tmpDir
	}
	tmpRoot := filepath.Join(baseTmpDir, tmpDirName)

	if pathExists(tmpRoot) {
		if !*force {
			fmt.Fprintf(os.Stderr, "error: %s already exists – run 'pebblify recover --tmp-dir %s' or use --force\n", tmpRoot, baseTmpDir)
			os.Exit(1)
		}

		if err := os.RemoveAll(tmpRoot); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to remove existing temp dir %s: %v\n", tmpRoot, err)
			os.Exit(1)
		}
	}

	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create temp dir %s: %v\n", tmpRoot, err)
		os.Exit(1)
	}

	unlock, err := acquireLock(tmpRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error acquiring lock: %v\n", err)
		os.Exit(1)
	}
	defer unlock()

	if err := runLevelToPebble(src, out, *workers, *batchMemory, tmpRoot); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runLevelToPebble(src, out string, workers, batchMemory int, tmpRoot string) error {
	statePath := filepath.Join(tmpRoot, stateFileName)
	tmpData := filepath.Join(tmpRoot, "data")

	baseTmpDir := filepath.Dir(tmpRoot)

	if !pathExists(src) {
		return fmt.Errorf("SRC path does not exist: %s", src)
	}

	srcSize, err := dirSize(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to compute size of SRC (%s): %v\n", src, err)
	}

	if srcSize > 0 {
		checkDiskSpace(baseTmpDir, srcSize)
	}

	if err := os.MkdirAll(tmpData, 0o755); err != nil {
		return fmt.Errorf("failed to create temp data dir %s: %w", tmpData, err)
	}

	if pathExists(out) {
		empty, err := isDirEmpty(out)
		if err != nil {
			return fmt.Errorf("cannot inspect OUT directory: %w", err)
		}
		if !empty {
			return fmt.Errorf("OUT directory %s already exists and is not empty", out)
		}
	} else {
		if err := os.MkdirAll(out, 0o755); err != nil {
			return fmt.Errorf("failed to create OUT directory %s: %w", out, err)
		}
	}

	state := &ConversionState{
		Version:     stateVersion,
		Src:         src,
		Out:         out,
		StartedAt:   time.Now().UTC(),
		LastUpdated: time.Now().UTC(),
		DBs:         make(map[string]*DBStatus),
	}

	fmt.Println("Scanning source directory and estimating keys...")
	if err := scanAndPrepare(src, tmpData, state); err != nil {
		return fmt.Errorf("failed to scan SRC: %w", err)
	}

	var totalKeys int64
	for _, db := range state.DBs {
		totalKeys += db.EstimatedKeys
	}
	state.TotalKeysEstimated = totalKeys

	if err := updateState(statePath, state, nil); err != nil {
		return fmt.Errorf("failed to write initial state file: %w", err)
	}

	dbList := collectPendingDBs(state)
	if len(dbList) == 0 {
		fmt.Println("No .db directories found to convert.")
		return nil
	}

	workers = normalizeWorkers(workers, len(dbList))

	fmt.Printf("\nStarting LevelDB → PebbleDB migration\n")
	fmt.Printf("  SRC: %s\n", src)
	fmt.Printf("  OUT: %s\n", out)
	fmt.Printf("  TMP: %s\n", tmpRoot)
	fmt.Printf("  workers: %d\n", workers)
	fmt.Printf("  batch memory: %d MB\n", batchMemory)
	fmt.Printf("  verbose: %v\n\n", verbose)

	if srcSize > 0 {
		fmt.Printf("Source data size: %s (%d bytes)\n\n", formatBytes(srcSize), srcSize)
	}

	fmt.Println("Discovered DBs to convert:")
	for _, db := range dbList {
		fmt.Printf("  - %s (size: %s, est. keys: %d)\n",
			db.Name, formatBytes(db.SizeBytes), db.EstimatedKeys)
	}
	fmt.Println()

	metrics := NewMetrics()

	doneCh := make(chan struct{})
	go monitorProgress(state, totalKeys, state.StartedAt, metrics, doneCh)

	batchConfig := &BatchConfig{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: batchMemory,
	}

	startRun := time.Now()
	if err := convertAllDBs(statePath, state, workers, batchConfig, metrics); err != nil {
		close(doneCh)
		return err
	}
	close(doneCh)

	finalData := filepath.Join(out, "data")
	if pathExists(finalData) {
		return fmt.Errorf("final data dir already exists: %s", finalData)
	}
	if err := moveDir(tmpData, finalData); err != nil {
		return fmt.Errorf("failed to move %s to %s: %w", tmpData, finalData, err)
	}

	outSize, err := dirSize(finalData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to compute size of OUT (%s): %v\n", finalData, err)
	}

	if err := os.RemoveAll(tmpRoot); err != nil {
		return fmt.Errorf("failed to cleanup temp dir %s: %w", tmpRoot, err)
	}

	metrics.PrintSummary()

	fmt.Println("\nConversion completed successfully.")
	fmt.Printf("New Pebble-backed data directory: %s\n", finalData)

	runDuration := time.Since(startRun)
	totalDuration := time.Since(state.StartedAt)

	fmt.Println()
	fmt.Printf("This run duration:   %s\n", runDuration.Truncate(time.Second))
	fmt.Printf("Total elapsed time:  %s (since first start)\n", totalDuration.Truncate(time.Second))

	if srcSize > 0 && outSize > 0 {
		ratio := float64(outSize) / float64(srcSize) * 100
		fmt.Println()
		fmt.Println("Size summary:")
		fmt.Printf("  Source (LevelDB) data:  %s (%d bytes)\n", formatBytes(srcSize), srcSize)
		fmt.Printf("  Target (PebbleDB) data: %s (%d bytes)\n", formatBytes(outSize), outSize)
		fmt.Printf("  Size ratio:             %.1f %%\n", ratio)
	}

	return nil
}

func recoverCmd(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	workers := fs.Int("workers", 0, "max concurrent DB conversions (0 = auto)")
	fs.IntVar(workers, "w", 0, "alias for --workers")
	batchMemory := fs.Int("batch-memory", 64, "target memory per batch in MB")
	tmpDir := fs.String("tmp-dir", "", "directory containing .pebblify-tmp (must match conversion)")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose output")
	fs.BoolVar(&verbose, "v", false, "alias for --verbose")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "recover does not take any positional arguments\n\n")
		fmt.Fprintf(os.Stderr, "Usage: pebblify recover [options]\n")
		os.Exit(1)
	}

	baseTmpDir := os.TempDir()
	if *tmpDir != "" {
		baseTmpDir = *tmpDir
	}
	tmpRoot := filepath.Join(baseTmpDir, tmpDirName)

	if !pathExists(tmpRoot) {
		fmt.Fprintf(os.Stderr, "error: no existing temp directory found at %s – nothing to recover\n", tmpRoot)
		if *tmpDir == "" {
			fmt.Fprintf(os.Stderr, "hint: if you used --tmp-dir during conversion, specify it here too\n")
		}
		os.Exit(1)
	}

	unlock, err := acquireLock(tmpRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error acquiring lock: %v\n", err)
		os.Exit(1)
	}
	defer unlock()

	if err := runRecover(*workers, *batchMemory, tmpRoot); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runRecover(workers, batchMemory int, tmpRoot string) error {
	statePath := filepath.Join(tmpRoot, stateFileName)

	state, err := readState(statePath)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	fmt.Println("Recovery state summary:")
	fmt.Printf("  Started at:    %s\n", state.StartedAt.Format(time.RFC3339))
	fmt.Printf("  Last updated:  %s\n", state.LastUpdated.Format(time.RFC3339))
	fmt.Printf("  Source:        %s\n", state.Src)
	fmt.Printf("  Output:        %s\n\n", state.Out)

	var doneCount, inProgressCount, pendingCount, failedCount int

	fmt.Println("Database statuses:")
	for _, db := range state.DBs {
		switch db.Status {
		case "done":
			doneCount++
			fmt.Printf("  ✓ %s: done (%d keys)\n", db.Name, db.MigratedKeys)
		case "in_progress":
			inProgressCount++
			progress := float64(0)
			if db.EstimatedKeys > 0 {
				progress = float64(db.MigratedKeys) / float64(db.EstimatedKeys) * 100
			}
			checkpointInfo := ""
			if db.GetLastCheckpointKey() != nil {
				checkpointInfo = fmt.Sprintf(", checkpoint at %s", db.CheckpointTime.Format("15:04:05"))
			}
			fmt.Printf("  ⟳ %s: in progress (%.1f%%, %d/%d keys%s)\n",
				db.Name, progress, db.MigratedKeys, db.EstimatedKeys, checkpointInfo)
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

	if state.TotalKeysEstimated > 0 {
		overallProgress := float64(state.TotalKeysMigrated) / float64(state.TotalKeysEstimated) * 100
		fmt.Printf("Overall progress: %.1f%% (%d / %d keys)\n\n",
			overallProgress, state.TotalKeysMigrated, state.TotalKeysEstimated)
	}

	dbList := collectPendingDBs(state)
	if len(dbList) == 0 {
		fmt.Println("All databases are already converted. Moving to finalization...")
		return finalizeConversion(state, tmpRoot)
	}

	fmt.Printf("Resuming conversion for %d database(s)...\n\n", len(dbList))

	workers = normalizeWorkers(workers, len(dbList))
	metrics := NewMetrics()

	doneCh := make(chan struct{})
	go monitorProgress(state, state.TotalKeysEstimated, state.StartedAt, metrics, doneCh)

	batchConfig := &BatchConfig{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: batchMemory,
	}

	if err := convertAllDBs(statePath, state, workers, batchConfig, metrics); err != nil {
		close(doneCh)
		return err
	}
	close(doneCh)

	metrics.PrintSummary()

	return finalizeConversion(state, tmpRoot)
}

func finalizeConversion(state *ConversionState, tmpRoot string) error {
	tmpData := filepath.Join(tmpRoot, "data")
	finalData := filepath.Join(state.Out, "data")

	if !pathExists(finalData) {
		fmt.Printf("Moving converted data to %s...\n", finalData)
		if err := moveDir(tmpData, finalData); err != nil {
			return fmt.Errorf("failed to move data: %w", err)
		}
	}

	fmt.Printf("Cleaning up temp directory %s...\n", tmpRoot)
	if err := os.RemoveAll(tmpRoot); err != nil {
		return fmt.Errorf("failed to cleanup: %w", err)
	}

	fmt.Println("\nConversion completed successfully!")
	fmt.Printf("PebbleDB data directory: %s\n", finalData)

	srcSize, _ := dirSize(state.Src)
	outSize, _ := dirSize(finalData)
	if srcSize > 0 && outSize > 0 {
		ratio := float64(outSize) / float64(srcSize) * 100
		fmt.Println()
		fmt.Println("Size summary:")
		fmt.Printf("  Source (LevelDB) data:  %s\n", formatBytes(srcSize))
		fmt.Printf("  Target (PebbleDB) data: %s\n", formatBytes(outSize))
		fmt.Printf("  Size ratio:             %.1f %%\n", ratio)
	}

	return nil
}

func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	samplePercent := fs.Float64("sample", 100, "percentage of keys to verify (0-100, 100=all)")
	fs.Float64Var(samplePercent, "s", 100, "alias for --sample")
	stopOnError := fs.Bool("stop-on-error", false, "stop at first mismatch")
	fs.BoolVar(&verbose, "verbose", false, "show each key being verified")
	fs.BoolVar(&verbose, "v", false, "alias for --verbose")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: pebblify verify [options] SRC_DATA DST_DATA\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  SRC_DATA    Source data directory (LevelDB)\n")
		fmt.Fprintf(os.Stderr, "  DST_DATA    Destination data directory (PebbleDB)\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	config := &VerifyConfig{
		SamplePercent: *samplePercent,
		StopOnError:   *stopOnError,
		Verbose:       verbose,
	}

	if err := runVerify(rest[0], rest[1], config); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runVerify(srcDir, dstDir string, config *VerifyConfig) error {
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
			if pathExists(dstPath) {
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

		result, err := verifyDB(paths.src, paths.dst, config)
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

func scanAndPrepare(src, tmpData string, state *ConversionState) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read SRC dir: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		srcPath := filepath.Join(src, name)

		if e.IsDir() && strings.HasSuffix(name, ".db") {
			size, err := dirSize(srcPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to compute size for db %s: %v\n", srcPath, err)
			}

			keys := estimateDBKeys(srcPath)

			tempPath := filepath.Join(tmpData, name+".tmp")
			state.DBs[name] = &DBStatus{
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
			if err := copyDir(srcPath, dstPath); err != nil {
				return fmt.Errorf("failed to copy directory %s -> %s: %w", srcPath, dstPath, err)
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return fmt.Errorf("failed to copy file %s -> %s: %w", srcPath, dstPath, err)
			}
		}
	}

	return nil
}

func estimateDBKeys(path string) int64 {
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
		return estimateKeysByFullScan(db, path)
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
		return estimateKeysByFullScan(db, path)
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

func estimateKeysByFullScan(db *leveldb.DB, path string) int64 {
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

func collectPendingDBs(state *ConversionState) []*DBStatus {
	var res []*DBStatus
	for _, db := range state.DBs {
		if db.Status != "done" {
			res = append(res, db)
		}
	}
	return res
}

func normalizeWorkers(workers int, numJobs int) int {
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

func convertAllDBs(statePath string, state *ConversionState, workers int, batchConfig *BatchConfig, metrics *Metrics) error {
	dbList := collectPendingDBs(state)
	if len(dbList) == 0 {
		return nil
	}

	jobs := make(chan *DBStatus)
	errCh := make(chan error, len(dbList))
	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for dbst := range jobs {
				if err := convertSingleDB(statePath, state, dbst, batchConfig, metrics); err != nil {
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

func convertSingleDB(statePath string, state *ConversionState, dbst *DBStatus, batchConfig *BatchConfig, metrics *Metrics) error {
	fmt.Printf("\nConverting DB %s", dbst.Name)

	isResume := dbst.Status == "in_progress" && dbst.GetLastCheckpointKey() != nil
	if isResume {
		fmt.Printf(" (resuming from checkpoint, %d keys already migrated)\n", dbst.MigratedKeys)
	} else {
		fmt.Println()
		if pathExists(dbst.TempPath) {
			if err := os.RemoveAll(dbst.TempPath); err != nil {
				return fmt.Errorf("failed to remove existing temp db dir %s: %w", dbst.TempPath, err)
			}
		}
		dbst.MigratedKeys = 0
		dbst.BytesRead = 0
		dbst.BytesWritten = 0
	}

	if err := updateState(statePath, state, func() {
		dbst.Status = "in_progress"
		dbst.Error = ""
	}); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	srcDB, err := leveldb.OpenFile(dbst.SourcePath, &levopt.Options{
		ErrorIfMissing: true,
		ReadOnly:       true,
	})
	if err != nil {
		return markDBFailed(statePath, state, dbst, err)
	}
	defer srcDB.Close()

	dstDB, err := pebble.Open(dbst.TempPath, &pebble.Options{})
	if err != nil {
		return markDBFailed(statePath, state, dbst, err)
	}
	defer dstDB.Close()

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

	batcher := NewAdaptiveBatcher(dstDB, batchConfig)
	defer batcher.Close()

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

		if err := batcher.Add(key, val); err != nil {
			return markDBFailed(statePath, state, dbst, err)
		}

		count++
		intervalKeys++
		bytesWritten += entrySize

		if count%int64(checkpointInterval) == 0 {
			if err := batcher.Commit(); err != nil {
				return markDBFailed(statePath, state, dbst, err)
			}

			if err := updateState(statePath, state, func() {
				dbst.MigratedKeys = count
				dbst.SetLastCheckpointKey(lastKey)
				dbst.CheckpointTime = time.Now()
				dbst.BytesRead = bytesRead
				dbst.BytesWritten = bytesWritten
				state.TotalKeysMigrated = calculateTotalMigrated(state)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to write checkpoint: %v\n", err)
			}

			if time.Since(lastMetricsUpdate) >= time.Second {
				metrics.RecordKeys(dbst.Name, intervalKeys, intervalBytes, intervalBytes)
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
		metrics.RecordKeys(dbst.Name, intervalKeys, intervalBytes, intervalBytes)
	}

	if err := batcher.Commit(); err != nil {
		return markDBFailed(statePath, state, dbst, err)
	}

	if err := it.Error(); err != nil {
		return markDBFailed(statePath, state, dbst, err)
	}

	if err := dstDB.Flush(); err != nil {
		return markDBFailed(statePath, state, dbst, err)
	}

	finalPath := strings.TrimSuffix(dbst.TempPath, ".tmp")
	if finalPath != dbst.TempPath {
		if err := os.Rename(dbst.TempPath, finalPath); err != nil {
			return markDBFailed(statePath, state, dbst, err)
		}
		dbst.TempPath = finalPath
	}

	_, _, avgKey, avgVal := batcher.Stats()
	metrics.FinalizeDB(dbst.Name, avgKey, avgVal)

	if err := updateState(statePath, state, func() {
		dbst.Status = "done"
		dbst.Error = ""
		dbst.MigratedKeys = count
		dbst.SetLastCheckpointKey(nil)
		dbst.BytesRead = bytesRead
		dbst.BytesWritten = bytesWritten
		state.TotalKeysMigrated = calculateTotalMigrated(state)
	}); err != nil {
		return fmt.Errorf("failed to finalize state: %w", err)
	}

	fmt.Printf("\nDB %s converted successfully (%d keys)\n", dbst.Name, count)
	return nil
}

func markDBFailed(statePath string, state *ConversionState, dbst *DBStatus, originalErr error) error {
	_ = updateState(statePath, state, func() {
		dbst.Status = "failed"
		dbst.Error = originalErr.Error()
	})
	return fmt.Errorf("failed processing %s: %w", dbst.Name, originalErr)
}

func calculateTotalMigrated(state *ConversionState) int64 {
	var total int64
	for _, db := range state.DBs {
		total += db.MigratedKeys
	}
	return total
}

func monitorProgress(state *ConversionState, totalKeys int64, startedAt time.Time, metrics *Metrics, done <-chan struct{}) {
	if totalKeys == 0 {
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			renderProgressBar(state, totalKeys, startedAt, metrics, true)
			fmt.Fprintln(os.Stderr)
			return
		case <-ticker.C:
			renderProgressBar(state, totalKeys, startedAt, metrics, false)
		}
	}
}

func renderProgressBar(state *ConversionState, totalKeys int64, startedAt time.Time, metrics *Metrics, final bool) {
	stateMu.Lock()
	defer stateMu.Unlock()

	var keysDone int64
	var doneCount, inProgCount, pendingCount, failedCount int
	now := time.Now()

	for _, db := range state.DBs {
		migrated := db.MigratedKeys
		if migrated == 0 && db.Status == "done" && db.EstimatedKeys > 0 {
			migrated = db.EstimatedKeys
		}

		switch db.Status {
		case "done":
			doneCount++
		case "in_progress":
			inProgCount++
		case "pending":
			pendingCount++
		case "failed":
			failedCount++
		}

		keysDone += migrated
	}

	elapsed := now.Sub(startedAt)
	percent := 0.0
	progressRatio := 0.0
	if totalKeys > 0 {
		progressRatio = float64(keysDone) / float64(totalKeys)
		if progressRatio > 1 {
			progressRatio = 1
		}
		percent = progressRatio * 100
	}

	barWidth := 30
	filled := max(min(int(percent/100*float64(barWidth)), barWidth), 0)

	bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)

	throughputStr := ""
	if metrics != nil {
		keysPerSec, mbPerSec := metrics.GetCurrentThroughput()
		if keysPerSec > 0 {
			throughputStr = fmt.Sprintf(" | %.0fk/s %.1fMB/s", keysPerSec/1000, mbPerSec)
		}
	}

	etaStr := ""
	if keysDone > 0 && totalKeys > 0 && elapsed > 5*time.Second && progressRatio >= 0.05 {
		throughput := float64(keysDone) / elapsed.Seconds()
		keysRemaining := float64(totalKeys - keysDone)
		if throughput > 0 && keysRemaining > 0 {
			etaSec := keysRemaining / throughput
			eta := time.Duration(etaSec) * time.Second
			etaStr = fmt.Sprintf(" | ETA %s", eta.Truncate(time.Second))
		}
	}

	line := fmt.Sprintf(
		"\r[%s] %5.1f%% | done:%d in:%d pend:%d fail:%d | %s%s%s",
		bar,
		percent,
		doneCount,
		inProgCount,
		pendingCount,
		failedCount,
		elapsed.Truncate(time.Second),
		throughputStr,
		etaStr,
	)

	fmt.Fprint(os.Stderr, line)

	if final {
		fmt.Fprint(os.Stderr, " | DONE")
	}
}

func getAvailableSpace(path string) (uint64, error) {
	var stat syscall.Statfs_t
	checkPath := path
	for !pathExists(checkPath) {
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

func checkDiskSpace(tmpDir string, srcSize int64) {
	available, err := getAvailableSpace(tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not check available disk space: %v\n", err)
		return
	}

	requiredSpace := uint64(float64(srcSize) * 1.5)

	if available < requiredSpace {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Low disk space!\n")
		fmt.Fprintf(os.Stderr, "   Available in %s: %s\n", tmpDir, formatBytes(int64(available)))
		fmt.Fprintf(os.Stderr, "   Estimated required: %s (1.5x source size)\n", formatBytes(int64(requiredSpace)))
		fmt.Fprintf(os.Stderr, "   Consider using --tmp-dir to specify a directory with more space.\n\n")
	} else if verbose {
		fmt.Printf("Disk space check: %s available, ~%s required\n\n",
			formatBytes(int64(available)), formatBytes(int64(requiredSpace)))
	}
}

func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		var linkErr *os.LinkError
		if !errors.As(err, &linkErr) || linkErr.Err != syscall.EXDEV {
			return err
		}

		if err := copyDir(src, dst); err != nil {
			return err
		}
		if err := os.RemoveAll(src); err != nil {
			return err
		}
	}
	return nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDirEmpty(p string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

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

func copyDir(src, dst string) error {
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
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func dirSize(root string) (int64, error) {
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

func formatBytes(n int64) string {
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
