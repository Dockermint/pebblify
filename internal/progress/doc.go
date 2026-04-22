// Package progress renders the live progress bar shown during a
// conversion.
//
// Monitor ticks once per second, reads the shared ConversionState under the
// state package's lock, and writes a single-line, carriage-return-updated
// bar to stderr. The line includes elapsed time, per-status database
// counts, a rolling throughput figure sourced from the metrics package,
// and an ETA computed from the average throughput since the run started.
package progress
