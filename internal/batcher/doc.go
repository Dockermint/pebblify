// Package batcher provides an adaptive write batcher for PebbleDB.
//
// The batcher accumulates key/value pairs and commits them in bulk once
// either the configured memory budget or the maximum per-batch entry count
// is reached. Callers can observe commits via an optional callback, which
// the migration package uses to drive progress reporting and Prometheus
// counters.
package batcher
