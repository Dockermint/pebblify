package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dockermint/Pebblify/internal/fsutil"
)

// DBMetricsData is the per-database counters and derived statistics
// collected during a conversion run.
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

// Metrics aggregates global and per-database conversion counters together
// with a rolling 30-second throughput window. It is safe for concurrent
// use.
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

// New returns a zeroed Metrics ready for use. The StartTime and LastUpdate
// fields are primed to the current wall clock time.
func New() *Metrics {
	return &Metrics{
		DBMetrics:     make(map[string]*DBMetricsData),
		StartTime:     time.Now(),
		LastUpdate:    time.Now(),
		recentSamples: make([]throughputSample, 0, 100),
	}
}

// RecordKeys increments the global and per-database counters for dbName
// and appends a new sample into the rolling throughput window. Samples
// older than 30 seconds are evicted on every call.
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

// FinalizeDB closes out the per-database counters for dbName, setting the
// end time, duration, and average key and value sizes, and deriving the
// throughput figures reported in the final summary.
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

// GetCurrentThroughput returns the current keys-per-second and
// megabytes-per-second figures computed from the rolling sample window.
// It returns zeroes when fewer than two samples are available.
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

// PrintSummary writes the human-readable metrics summary (global totals,
// throughput, and per-database breakdown) to stdout.
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
	fmt.Printf("  Total data read:     %s\n", fsutil.FormatBytes(m.TotalBytesRead))
	fmt.Printf("  Total data written:  %s\n", fsutil.FormatBytes(m.TotalBytesWritten))

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
