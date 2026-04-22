// Package verify performs post-conversion data integrity checks between
// the source LevelDB and the target PebbleDB.
//
// VerifyDB walks the source database, samples keys at the configured
// percentage, and compares each sampled value byte-for-byte against the
// target. Run iterates over every .db subdirectory and aggregates results
// into a human-readable summary. When the sample is 100 percent the target
// is additionally scanned for extra keys that are absent from the source.
package verify
