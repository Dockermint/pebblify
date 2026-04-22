// Package migration implements the LevelDB to PebbleDB conversion pipeline.
//
// The package exposes two entrypoints: RunLevelToPebble drives a fresh
// conversion of a Tendermint/CometBFT data directory, while RunRecover
// resumes an interrupted run from the last on-disk checkpoint. Both
// orchestrate the scanner, the adaptive batcher, the state store, and the
// progress monitor, and share a worker pool that processes each discovered
// .db directory concurrently.
package migration
