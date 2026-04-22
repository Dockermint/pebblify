package state

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// On-disk layout constants shared by the migration pipeline.
const (
	// TmpDirName is the name of the temporary directory pebblify creates
	// under the configured base directory to stage converted output.
	TmpDirName = ".pebblify-tmp"
	// StateFileName is the filename used for the JSON-encoded conversion
	// state inside TmpDirName.
	StateFileName = "state.json"
	// LockFileName is the filename of the exclusive lock file pebblify
	// creates inside the temp root to prevent concurrent runs.
	LockFileName = "pebblify.lock"
	// StateVersion is the current schema version embedded in every state
	// document written by pebblify.
	StateVersion = 0
)

// DBStatus captures the conversion progress of a single .db sub-directory
// and is persisted as part of the ConversionState JSON document.
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

// GetLastCheckpointKey decodes and returns the last checkpoint key for d,
// or nil if no checkpoint has been recorded yet.
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

// SetLastCheckpointKey base64-encodes key into the persisted field. A nil
// key clears the stored checkpoint.
func (d *DBStatus) SetLastCheckpointKey(key []byte) {
	if key == nil {
		d.LastCheckpointKeyB64 = ""
	} else {
		d.LastCheckpointKeyB64 = base64.StdEncoding.EncodeToString(key)
	}
}

// ConversionState is the top-level persisted document that tracks an
// in-flight or recovered conversion run.
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

var mu sync.Mutex

// Update applies update under the package-level mutex, stamps the
// LastUpdated field, and atomically writes the document to statePath. A
// nil update function is accepted and simply persists the current state.
func Update(statePath string, state *ConversionState, update func()) error {
	mu.Lock()
	defer mu.Unlock()

	if update != nil {
		update()
	}
	state.LastUpdated = time.Now()
	return writeAtomic(statePath, state)
}

// Lock acquires the package-level state mutex so external callers (such
// as the progress renderer) can read ConversionState consistently.
func Lock() {
	mu.Lock()
}

// Unlock releases the package-level state mutex acquired by Lock.
func Unlock() {
	mu.Unlock()
}

func writeAtomic(path string, state *ConversionState) error {
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
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

// Read decodes the JSON-encoded ConversionState stored at path.
func Read(path string) (*ConversionState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var s ConversionState
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, err
	}

	return &s, nil
}

// AcquireLock creates the on-disk lock file inside tmpRoot and returns a
// release closure. It fails if the lock file already exists, signalling a
// concurrent pebblify run or a stale file left by a crashed process.
func AcquireLock(tmpRoot string) (func(), error) {
	lockPath := filepath.Join(tmpRoot, LockFileName)

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another pebblify instance seems to be running (lock file: %s). If not, delete it manually", lockPath)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	_, _ = fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))

	unlock := func() {
		_ = os.Remove(lockPath)
	}

	return unlock, nil
}

// CollectPendingDBs returns every DBStatus in s whose status is not
// "done". The result preserves the order yielded by map iteration.
func CollectPendingDBs(s *ConversionState) []*DBStatus {
	var res []*DBStatus
	for _, db := range s.DBs {
		if db.Status != "done" {
			res = append(res, db)
		}
	}
	return res
}

// CalculateTotalMigrated returns the sum of MigratedKeys across every
// DBStatus in s.
func CalculateTotalMigrated(s *ConversionState) int64 {
	var total int64
	for _, db := range s.DBs {
		total += db.MigratedKeys
	}
	return total
}
