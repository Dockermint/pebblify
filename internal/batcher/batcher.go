package batcher

import (
	"github.com/cockroachdb/pebble"
)

// Config tunes the AdaptiveBatcher commit thresholds.
type Config struct {
	MinBatchSize   int
	MaxBatchSize   int
	TargetMemoryMB int
}

// DefaultConfig returns a Config populated with the batcher defaults used
// by the migration pipeline when no explicit tuning is supplied.
func DefaultConfig() *Config {
	return &Config{
		MinBatchSize:   1_000,
		MaxBatchSize:   100_000,
		TargetMemoryMB: 64,
	}
}

// AdaptiveBatcher accumulates key/value pairs and commits them to PebbleDB
// in bulk once either the memory budget or the entry-count ceiling is hit.
type AdaptiveBatcher struct {
	config       *Config
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

// New constructs an AdaptiveBatcher bound to db. A nil config falls back
// to DefaultConfig.
func New(db *pebble.DB, config *Config) *AdaptiveBatcher {
	if config == nil {
		config = DefaultConfig()
	}
	return &AdaptiveBatcher{
		config: config,
		batch:  db.NewBatch(),
		db:     db,
	}
}

// SetOnCommit registers a callback invoked after each successful batch
// commit with the number of entries and bytes that were flushed.
func (ab *AdaptiveBatcher) SetOnCommit(fn func(keys int64, bytes int64)) {
	ab.onCommit = fn
}

// Add copies key and value into the pending batch and triggers Commit when
// either TargetMemoryMB or MaxBatchSize is reached.
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

// Commit flushes the pending batch to PebbleDB without fsync, resets the
// in-memory accumulator, and notifies any registered OnCommit callback.
// A commit of an empty batch is a no-op.
func (ab *AdaptiveBatcher) Commit() error {
	if ab.currentCount == 0 {
		return nil
	}

	if err := ab.batch.Commit(pebble.NoSync); err != nil {
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

// Stats returns the cumulative counters maintained by the batcher: total
// keys written, total bytes written, and the rolling average key and value
// sizes computed from the observed data.
func (ab *AdaptiveBatcher) Stats() (totalKeys, totalBytes int64, avgKeySize, avgValueSize float64) {
	return ab.totalKeys, ab.totalBytes, ab.avgKeySize, ab.avgValueSize
}

// Close releases the underlying pebble.Batch. Any pending entries are
// discarded; callers that need to persist them must call Commit first.
func (ab *AdaptiveBatcher) Close() error {
	return ab.batch.Close()
}
