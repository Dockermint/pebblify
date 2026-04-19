package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
	"github.com/Dockermint/Pebblify/internal/daemon/store"
)

// TestNew_NoTargetsEnabled returns ErrNoTargets when all backends are disabled.
func TestNew_NoTargetsEnabled(t *testing.T) {
	t.Parallel()
	cfg := config.SaveSection{
		Compression: "lz4",
		Local:       config.LocalSaveSection{Enable: false},
		SCP:         config.SCPSaveSection{Enable: false},
		S3:          config.S3SaveSection{Enable: false},
	}
	_, err := store.New(context.Background(), cfg, config.Secrets{}, nil)
	if !errors.Is(err, store.ErrNoTargets) {
		t.Errorf("New() error = %v, want %v", err, store.ErrNoTargets)
	}
}

// TestNew_LocalOnlyProducesOneTarget returns exactly one local target.
func TestNew_LocalOnlyProducesOneTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.SaveSection{
		Compression: "lz4",
		Local:       config.LocalSaveSection{Enable: true, LocalSaveDirectory: dir},
		SCP:         config.SCPSaveSection{Enable: false},
		S3:          config.S3SaveSection{Enable: false},
	}
	targets, err := store.New(context.Background(), cfg, config.Secrets{}, nil)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	if targets[0].Name() != "local" {
		t.Errorf("targets[0].Name() = %q, want %q", targets[0].Name(), "local")
	}
}

// TestNew_LocalEmptyDirReturnsError confirms local.New rejects empty dir.
func TestNew_LocalEmptyDirReturnsError(t *testing.T) {
	t.Parallel()
	cfg := config.SaveSection{
		Compression: "lz4",
		Local:       config.LocalSaveSection{Enable: true, LocalSaveDirectory: ""},
		SCP:         config.SCPSaveSection{Enable: false},
		S3:          config.S3SaveSection{Enable: false},
	}
	_, err := store.New(context.Background(), cfg, config.Secrets{}, nil)
	if err == nil {
		t.Fatal("New() expected error for empty local dir, got nil")
	}
}

// TestNew_S3MissingBucketReturnsError propagates s3.New validation.
func TestNew_S3MissingBucketReturnsError(t *testing.T) {
	t.Parallel()
	cfg := config.SaveSection{
		Compression: "lz4",
		Local:       config.LocalSaveSection{Enable: false},
		SCP:         config.SCPSaveSection{Enable: false},
		S3: config.S3SaveSection{
			Enable:      true,
			BucketName:  "",
			S3AccessKey: "AKID",
		},
	}
	secrets := config.Secrets{S3SecretKey: "secret"}
	_, err := store.New(context.Background(), cfg, secrets, nil)
	if err == nil {
		t.Fatal("New() expected error for empty bucket, got nil")
	}
}

// TestCompressionConstants verifies string values match spec.
func TestCompressionConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got  store.Compression
		want string
	}{
		{store.CompNone, "none"},
		{store.CompLZ4, "lz4"},
		{store.CompZstd, "zstd"},
		{store.CompGzip, "gzip"},
	}
	for _, tt := range tests {
		t.Run(string(tt.got), func(t *testing.T) {
			t.Parallel()
			if string(tt.got) != tt.want {
				t.Errorf("Compression %q != %q", tt.got, tt.want)
			}
		})
	}
}

// ---- Target interface stub for interface compliance ----

// fakeTarget satisfies store.Target and records Upload calls.
type fakeTarget struct {
	name   string
	err    error
	called bool
}

func (f *fakeTarget) Upload(_ context.Context, _, _ string) error {
	f.called = true
	return f.err
}
func (f *fakeTarget) Name() string { return f.name }

// TestTarget_InterfaceCompliance verifies fakeTarget implements store.Target.
func TestTarget_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ store.Target = &fakeTarget{}
}
