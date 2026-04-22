// Command pebblify is the CLI entrypoint for the LevelDB to PebbleDB
// migration tool.
//
// The binary dispatches on its first positional argument to one of the
// level-to-pebble, recover, verify, completion, or daemon subcommands. Every
// subcommand owns its flag set, exits non-zero on error, and surfaces
// diagnostics on stderr. See the top-level usage text for flag details.
package main
