// Package state persists conversion progress to disk so an interrupted
// run can resume from the last checkpoint.
//
// The package owns the on-disk state.json document, the single-writer
// mutex that guards it, and the pebblify.lock file that prevents two
// pebblify processes from sharing a temp root. It also holds the typed
// representation of per-database progress (DBStatus) and the top-level
// ConversionState aggregate that the migration pipeline mutates.
package state
