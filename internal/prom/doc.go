// Package prom exposes the Prometheus metrics and the optional HTTP
// exporter used by the pebblify CLI.
//
// The package declares process-wide counters and gauges covering keys
// processed, bytes read and written, per-status database counts, current
// throughput, batch commits, and checkpoints. All metrics are registered
// with the default Prometheus registry in init and are served on /metrics
// by the Server type when the operator opts in with the --metrics flag.
package prom
