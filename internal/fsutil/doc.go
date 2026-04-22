// Package fsutil provides filesystem helpers used across the migration
// pipeline.
//
// The helpers cover path existence checks, recursive directory copy and
// move (with cross-device fallback), directory size accounting, human
// readable byte formatting, free-space inspection, and worker-count
// normalization. All helpers operate on the local filesystem and never
// touch configuration or secrets.
package fsutil
