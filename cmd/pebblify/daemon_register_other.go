//go:build !linux

// daemon_register_other.go: non-Linux build registers a stub that rejects the
// daemon subcommand. The daemon depends on systemd integration and Linux-only
// service paths, so on macOS and Windows it must run through the container
// image; this file exists solely to keep main.go portable across GOOS targets.

package main

import (
	"fmt"
	"os"
	"runtime"
)

// runDaemon rejects invocation on non-Linux platforms with a clear message.
// The daemon relies on systemd socket activation and sd_notify semantics that
// are intentionally Linux-only; on macOS users should run the daemon inside
// Docker or Podman.
func runDaemon(_ []string) {
	fmt.Fprintf(os.Stderr, "pebblify daemon is Linux-only (current: %s/%s)\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(os.Stderr, "On macOS, run the daemon via Docker or Podman.")
	os.Exit(1)
}
