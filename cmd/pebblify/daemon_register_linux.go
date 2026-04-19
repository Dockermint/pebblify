//go:build linux

// daemon_register_linux.go: Linux build registers the real daemon entrypoint.
// The sibling !linux file provides a stub that rejects the invocation with a
// clear message. Splitting the registration this way keeps main.go free of
// GOOS conditionals while preserving the Linux-only service semantics.

package main

// runDaemon is the Linux entrypoint wired into main's command switch. It
// forwards to daemonCmd, which owns flag parsing, config loading, and the
// full sub-server lifecycle.
func runDaemon(args []string) { daemonCmd(args) }
