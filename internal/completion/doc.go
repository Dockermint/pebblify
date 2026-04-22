// Package completion generates and installs shell completion scripts for
// the pebblify CLI.
//
// The package emits static bash and zsh completion code and provides helpers
// to write those scripts to the user's shell-specific completion directory.
// It does not execute shell code itself; installation is a simple file
// write under the user's home directory.
package completion
