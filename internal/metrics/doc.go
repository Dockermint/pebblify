// Package metrics aggregates runtime conversion metrics.
//
// A single Metrics value tracks global totals (keys, bytes read, bytes
// written) together with a rolling 30-second throughput window and a
// per-database breakdown. It is safe for concurrent use and is the source
// of truth behind both the live progress bar and the final human-readable
// summary printed at the end of a run.
package metrics
