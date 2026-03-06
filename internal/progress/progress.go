package progress

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dockermint/Pebblify/internal/metrics"
	"github.com/Dockermint/Pebblify/internal/state"
)

func Monitor(st *state.ConversionState, totalKeys int64, startedAt time.Time, m *metrics.Metrics, done <-chan struct{}) {
	if totalKeys == 0 {
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			renderBar(st, totalKeys, startedAt, m, true)
			fmt.Fprintln(os.Stderr)
			return
		case <-ticker.C:
			renderBar(st, totalKeys, startedAt, m, false)
		}
	}
}

func renderBar(st *state.ConversionState, totalKeys int64, startedAt time.Time, m *metrics.Metrics, final bool) {
	state.Lock()
	defer state.Unlock()

	var keysDone int64
	var doneCount, inProgCount, pendingCount, failedCount int
	now := time.Now()

	for _, db := range st.DBs {
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
	if m != nil {
		keysPerSec, mbPerSec := m.GetCurrentThroughput()
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
