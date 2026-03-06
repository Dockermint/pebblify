package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Dockermint/Pebblify/internal/fsutil"
	"github.com/Dockermint/Pebblify/internal/health"
	"github.com/Dockermint/Pebblify/internal/migration"
	"github.com/Dockermint/Pebblify/internal/state"
	"github.com/Dockermint/Pebblify/internal/verify"
)

var (
	Version  = "dev"
	Revision = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	if cmd == "-V" || cmd == "--version" || cmd == "version" {
		printVersion()
		os.Exit(0)
	}

	switch cmd {
	case "level-to-pebble":
		levelToPebbleCmd(os.Args[2:])
	case "recover":
		recoverCmd(os.Args[2:])
	case "verify":
		verifyCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func printVersion() {
	fmt.Printf("pebblify %s\n", Version)
	fmt.Printf("  revision:  %s\n", Revision)
	fmt.Printf("  platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  go:        %s\n", runtime.Version())
}

func usage() {
	fmt.Fprintf(os.Stderr, `pebblify %s – LevelDB → PebbleDB migration tool

Usage:
  pebblify <command> [options]

Commands:
  level-to-pebble   Convert a Tendermint/CometBFT data/ directory from LevelDB to PebbleDB
  recover           Resume a previously interrupted conversion
  verify            Verify that converted data matches the source
  version           Show version information

Options for level-to-pebble:
  -f, --force       Overwrite existing temporary state
  -w, --workers N   Max concurrent DB conversions (0 = auto, based on CPU)
  -v, --verbose     Enable verbose output
  --batch-memory M  Target memory per batch in MB (default: 64)
  --tmp-dir DIR     Directory where .pebblify-tmp/ will be created
                    (default: system temp, e.g. /tmp)
                    Use this if /tmp is too small (e.g. tmpfs in RAM)

Options for recover:
  -w, --workers N   Max concurrent DB conversions (0 = auto)
  -v, --verbose     Enable verbose output
  --tmp-dir DIR     Directory containing .pebblify-tmp/ (must match conversion)

Options for verify:
  -s, --sample P    Percentage of keys to verify (default: 100 = all)
  --stop-on-error   Stop at first mismatch
  -v, --verbose     Show each key being verified

Health probes (opt-in):
  --health          Enable the HTTP health probe server
  --health-port P   Port for the health server (default: 8086)

Global flags:
  -h, --help        Show this help
  -V, --version     Show version and exit

Examples:
  # Convert using /var/tmp instead of /tmp (creates /var/tmp/.pebblify-tmp/)
  pebblify level-to-pebble --tmp-dir /var/tmp ~/.gaia/data ./output

  # Resume an interrupted conversion (same --tmp-dir as before)
  pebblify recover --tmp-dir /var/tmp

  # Verify the converted data
  pebblify verify ~/.gaia/data ./output/data

  # Convert with health probes enabled
  pebblify level-to-pebble --health --health-port 8086 ~/.gaia/data ./output

`, Version)
}

type healthFlags struct {
	enabled bool
	port    int
}

func addHealthFlags(fs *flag.FlagSet) *healthFlags {
	hf := &healthFlags{}
	fs.BoolVar(&hf.enabled, "health", false, "enable HTTP health probe server")
	fs.IntVar(&hf.port, "health-port", 8086, "port for the health server")
	return hf
}

func startHealthServer(hf *healthFlags) (*health.Server, *health.ProbeState) {
	probeState := health.NewProbeState(30 * time.Second)

	if !hf.enabled {
		return nil, probeState
	}

	srv := health.NewServer(hf.port, probeState)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "health server error: %v\n", err)
		}
	}()

	return srv, probeState
}

func stopHealthServer(srv *health.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func levelToPebbleCmd(args []string) {
	fs := flag.NewFlagSet("level-to-pebble", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing temporary state")
	fs.BoolVar(force, "f", false, "alias for --force")
	workers := fs.Int("workers", 0, "max concurrent DB conversions (0 = auto)")
	fs.IntVar(workers, "w", 0, "alias for --workers")
	batchMemory := fs.Int("batch-memory", 64, "target memory per batch in MB")
	tmpDir := fs.String("tmp-dir", "", "directory where .pebblify-tmp will be created (default: system temp)")
	verbose := fs.Bool("verbose", false, "enable verbose output")
	fs.BoolVar(verbose, "v", false, "alias for --verbose")
	hf := addHealthFlags(fs)

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintf(os.Stderr, "invalid arguments for level-to-pebble\n\n")
		fmt.Fprintf(os.Stderr, "Usage: pebblify level-to-pebble [options] SRC OUT\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	srv, probeState := startHealthServer(hf)
	defer stopHealthServer(srv)

	src := rest[0]
	out := rest[1]

	baseTmpDir := os.TempDir()
	if *tmpDir != "" {
		baseTmpDir = *tmpDir
	}
	tmpRoot := filepath.Join(baseTmpDir, state.TmpDirName)

	if fsutil.PathExists(tmpRoot) {
		if !*force {
			fmt.Fprintf(os.Stderr, "error: %s already exists – run 'pebblify recover --tmp-dir %s' or use --force\n", tmpRoot, baseTmpDir)
			os.Exit(1)
		}

		if err := os.RemoveAll(tmpRoot); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to remove existing temp dir %s: %v\n", tmpRoot, err)
			os.Exit(1)
		}
	}

	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create temp dir %s: %v\n", tmpRoot, err)
		os.Exit(1)
	}

	unlock, err := state.AcquireLock(tmpRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error acquiring lock: %v\n", err)
		os.Exit(1)
	}
	defer unlock()

	probeState.SetStarted()
	probeState.SetReady()

	ticker := health.NewPingTicker(probeState, 5*time.Second)
	defer ticker.Stop()

	cfg := &migration.RunConfig{
		Workers:     *workers,
		BatchMemory: *batchMemory,
		Verbose:     *verbose,
	}

	if err := migration.RunLevelToPebble(src, out, cfg, tmpRoot); err != nil {
		probeState.SetNotReady()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func recoverCmd(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	workers := fs.Int("workers", 0, "max concurrent DB conversions (0 = auto)")
	fs.IntVar(workers, "w", 0, "alias for --workers")
	batchMemory := fs.Int("batch-memory", 64, "target memory per batch in MB")
	tmpDir := fs.String("tmp-dir", "", "directory containing .pebblify-tmp (must match conversion)")
	verbose := fs.Bool("verbose", false, "enable verbose output")
	fs.BoolVar(verbose, "v", false, "alias for --verbose")
	hf := addHealthFlags(fs)

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "recover does not take any positional arguments\n\n")
		fmt.Fprintf(os.Stderr, "Usage: pebblify recover [options]\n")
		os.Exit(1)
	}

	srv, probeState := startHealthServer(hf)
	defer stopHealthServer(srv)

	baseTmpDir := os.TempDir()
	if *tmpDir != "" {
		baseTmpDir = *tmpDir
	}
	tmpRoot := filepath.Join(baseTmpDir, state.TmpDirName)

	if !fsutil.PathExists(tmpRoot) {
		fmt.Fprintf(os.Stderr, "error: no existing temp directory found at %s – nothing to recover\n", tmpRoot)
		if *tmpDir == "" {
			fmt.Fprintf(os.Stderr, "hint: if you used --tmp-dir during conversion, specify it here too\n")
		}
		os.Exit(1)
	}

	unlock, err := state.AcquireLock(tmpRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error acquiring lock: %v\n", err)
		os.Exit(1)
	}
	defer unlock()

	probeState.SetStarted()
	probeState.SetReady()

	ticker := health.NewPingTicker(probeState, 5*time.Second)
	defer ticker.Stop()

	if err := migration.RunRecover(*workers, *batchMemory, tmpRoot, *verbose); err != nil {
		probeState.SetNotReady()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	samplePercent := fs.Float64("sample", 100, "percentage of keys to verify (0-100, 100=all)")
	fs.Float64Var(samplePercent, "s", 100, "alias for --sample")
	stopOnError := fs.Bool("stop-on-error", false, "stop at first mismatch")
	verbose := fs.Bool("verbose", false, "show each key being verified")
	fs.BoolVar(verbose, "v", false, "alias for --verbose")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: pebblify verify [options] SRC_DATA DST_DATA\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  SRC_DATA    Source data directory (LevelDB)\n")
		fmt.Fprintf(os.Stderr, "  DST_DATA    Destination data directory (PebbleDB)\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	config := &verify.Config{
		SamplePercent: *samplePercent,
		StopOnError:   *stopOnError,
		Verbose:       *verbose,
	}

	if err := verify.Run(rest[0], rest[1], config); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
