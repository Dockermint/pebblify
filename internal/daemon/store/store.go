// Package store delivers repacked PebbleDB archives to one or more remote
// or local destinations.
//
// Each Target implementation uploads a local file to the sink it represents
// (filesystem, SCP/SSH, S3). The daemon orchestrator fans out a single archive
// to every enabled Target. Partial upload failures are isolated per-target;
// a failure in one Target does not abort the others.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/store/local"
	"github.com/Dockermint/Pebblify/internal/daemon/store/s3"
	"github.com/Dockermint/Pebblify/internal/daemon/store/scp"
)

// Compression identifies the output archive codec. Mirrors the config enum
// for callers that prefer a typed value over raw strings.
type Compression string

// Compression codec enumeration.
const (
	// CompNone disables compression; output is a plain tar archive.
	CompNone Compression = "none"
	// CompLZ4 selects lz4 compression.
	CompLZ4 Compression = "lz4"
	// CompZstd selects zstd compression.
	CompZstd Compression = "zstd"
	// CompGzip selects gzip compression.
	CompGzip Compression = "gzip"
)

// Target is the contract every storage backend satisfies.
type Target interface {
	// Upload copies the file at localPath to the target under remoteName.
	// remoteName is the bare filename; targets that require a directory
	// or prefix (e.g. S3 save_path) join it internally.
	Upload(ctx context.Context, localPath, remoteName string) error
	// Name returns a short identifier used for logging and Prometheus labels.
	Name() string
}

// ErrNoTargets is returned by New when the daemon configuration does not
// enable any save target; at least one Target must be active for the daemon
// to produce useful output.
var ErrNoTargets = errors.New("no save targets enabled")

// New constructs every Target enabled in cfg. Targets are returned in a stable
// order (local, scp, s3) so log output remains deterministic. A configuration
// with zero enabled targets returns ErrNoTargets.
//
// ctx bounds construction-time I/O (e.g. s3 region lookup via the AWS SDK
// default config chain). logger is forwarded to target constructors that
// emit structured logs from their own code paths; a nil logger falls back to
// slog.Default so callers wiring a minimal daemon do not need to synthesize
// one.
//
// Each sub-constructor performs its own validation; errors propagate verbatim
// wrapped with a target-identifying prefix so operators can pinpoint the
// misconfigured section.
func New(ctx context.Context, cfg config.SaveSection, secrets config.Secrets,
	logger *slog.Logger) ([]Target, error) {
	if logger == nil {
		logger = slog.Default()
	}
	targets := make([]Target, 0, 3)

	if cfg.Local.Enable {
		t, err := local.New(cfg.Local)
		if err != nil {
			return nil, fmt.Errorf("store local: %w", err)
		}
		targets = append(targets, t)
	}

	if cfg.SCP.Enable {
		t, err := scp.New(cfg.SCP, secrets)
		if err != nil {
			return nil, fmt.Errorf("store scp: %w", err)
		}
		targets = append(targets, t)
	}

	if cfg.S3.Enable {
		t, err := s3.New(ctx, cfg.S3, secrets, logger)
		if err != nil {
			return nil, fmt.Errorf("store s3: %w", err)
		}
		targets = append(targets, t)
	}

	if len(targets) == 0 {
		return nil, ErrNoTargets
	}
	return targets, nil
}
