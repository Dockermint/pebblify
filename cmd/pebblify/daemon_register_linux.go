//go:build linux

package main

// runDaemon is the Linux entrypoint wired into main's command switch. It
// forwards to daemonCmd, which owns flag parsing, config loading, and the
// full sub-server lifecycle.
func runDaemon(args []string) { daemonCmd(args) }
