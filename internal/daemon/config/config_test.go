package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// validAPITOML returns a minimal valid [api] block (always-on in daemon mode).
func validAPITOML() string {
	return `[api]
host = "127.0.0.1"
port = 2324
authentification_mode = "unsecure"
`
}

// validMinimalTOML returns the minimal valid config that passes all validators.
func validMinimalTOML(t *testing.T) string {
	t.Helper()
	return `
[general]
config_version = 0

` + validAPITOML() + `
[notify]
enable = false

[telemetry]
enable = false

[health]
enable = false

[convertion]
temporary_directory = "/tmp"
delete_source_snapshot = false

[save]
compression = "lz4"

[save.local]
enable = true
local_save_directory = "/tmp/snapshots"

[save.scp]
enable = false

[save.s3]
enable = false
`
}

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return p
}

// TestLoad_ValidMinimalConfig loads the minimal valid config and checks basic fields.
func TestLoad_ValidMinimalConfig(t *testing.T) {
	t.Parallel()
	p := writeTOML(t, validMinimalTOML(t))
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if got.Config.General.ConfigVersion != 0 {
		t.Errorf("ConfigVersion = %d, want 0", got.Config.General.ConfigVersion)
	}
	if got.Config.Save.Compression != CompressionLZ4 {
		t.Errorf("Compression = %q, want %q", got.Config.Save.Compression, CompressionLZ4)
	}
	if got.Config.Queue.BufferSize != DefaultQueueBufferSize {
		t.Errorf("BufferSize = %d, want %d", got.Config.Queue.BufferSize, DefaultQueueBufferSize)
	}
}

// TestLoad_FileNotFound returns an error when the config file is missing.
func TestLoad_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("Load() expected error for missing file, got nil")
	}
}

// TestLoad_InvalidTOML returns an error for malformed TOML.
func TestLoad_InvalidTOML(t *testing.T) {
	t.Parallel()
	p := writeTOML(t, "this is not [valid toml{{{{")
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load() expected error for invalid TOML, got nil")
	}
}

// TestLoad_UnsupportedConfigVersion ensures versions > 0 are rejected.
func TestLoad_UnsupportedConfigVersion(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 1
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrUnsupportedConfigVersion) {
		t.Errorf("Load() error = %v, want %v", err, ErrUnsupportedConfigVersion)
	}
}

// TestLoad_InvalidPort covers each listener port constraint.
func TestLoad_InvalidPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		toml string
	}{
		{
			name: "api port zero",
			toml: `
[general]
config_version = 0
[api]
host = "127.0.0.1"
port = 0
authentification_mode = "unsecure"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`,
		},
		{
			name: "api port above 65535",
			toml: `
[general]
config_version = 0
[api]
host = "127.0.0.1"
port = 70000
authentification_mode = "unsecure"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`,
		},
		{
			name: "telemetry port negative",
			toml: `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = true
host = "127.0.0.1"
port = -1
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`,
		},
		{
			name: "health port zero",
			toml: `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = true
host = "127.0.0.1"
port = 0
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`,
		},
		{
			name: "scp port zero when enabled",
			toml: `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = true
host = "myhost"
port = 0
username = "user"
authentification_mode = "none"
[save.s3]
enable = false
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := writeTOML(t, tt.toml)
			_, err := Load(p)
			if !errors.Is(err, ErrInvalidPort) {
				t.Errorf("Load() error = %v, want %v", err, ErrInvalidPort)
			}
		})
	}
}

// TestLoad_InvalidAPIAuthMode returns ErrInvalidAPIAuthMode for unknown mode.
func TestLoad_InvalidAPIAuthMode(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 0
[api]
host = "127.0.0.1"
port = 2324
authentification_mode = "oauth2"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidAPIAuthMode) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidAPIAuthMode)
	}
}

// TestLoad_BasicAuthMissingToken returns ErrMissingSecret when token env is absent.
func TestLoad_BasicAuthMissingToken(t *testing.T) {
	t.Setenv(EnvBasicAuthToken, "")
	toml := `
[general]
config_version = 0
[api]
host = "127.0.0.1"
port = 2324
authentification_mode = "basic_auth"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("Load() error = %v, want %v", err, ErrMissingSecret)
	}
}

// TestLoad_BasicAuthWithToken succeeds when token env is set.
func TestLoad_BasicAuthWithToken(t *testing.T) {
	t.Setenv(EnvBasicAuthToken, "supersecret")
	toml := `
[general]
config_version = 0
[api]
host = "127.0.0.1"
port = 2324
authentification_mode = "basic_auth"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if got.Secrets.BasicAuthToken != "supersecret" {
		t.Errorf("BasicAuthToken = %q, want %q", got.Secrets.BasicAuthToken, "supersecret")
	}
}

// TestLoad_InvalidNotifyMode rejects unknown notify modes.
func TestLoad_InvalidNotifyMode(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = true
mode = "slack"
channel_id = "123"
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidNotifyMode) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidNotifyMode)
	}
}

// TestLoad_NotifyTelegramMissingToken returns ErrMissingSecret when bot token absent.
func TestLoad_NotifyTelegramMissingToken(t *testing.T) {
	t.Setenv(EnvTelegramBotToken, "")
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = true
mode = "telegram"
channel_id = "123"
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("Load() error = %v, want %v", err, ErrMissingSecret)
	}
}

// TestLoad_NotifyTelegramMissingChannelID returns ErrInvalidField when channel_id is empty.
func TestLoad_NotifyTelegramMissingChannelID(t *testing.T) {
	t.Setenv(EnvTelegramBotToken, "mytoken")
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = true
mode = "telegram"
channel_id = ""
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidField) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidField)
	}
}

// TestLoad_InvalidCompression rejects unknown compression codecs.
func TestLoad_InvalidCompression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		compression string
	}{
		{"empty", ""},
		{"unknown codec", "brotli"},
		{"typo lz5", "lz5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "` + tt.compression + `"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
			p := writeTOML(t, toml)
			_, err := Load(p)
			if !errors.Is(err, ErrInvalidCompression) {
				t.Errorf("Load() compression=%q error = %v, want %v", tt.compression, err, ErrInvalidCompression)
			}
		})
	}
}

// TestLoad_ValidCompressionModes checks all four accepted codecs.
func TestLoad_ValidCompressionModes(t *testing.T) {
	t.Parallel()
	for _, codec := range []string{CompressionNone, CompressionLZ4, CompressionZstd, CompressionGzip} {
		t.Run(codec, func(t *testing.T) {
			t.Parallel()
			toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "` + codec + `"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
			p := writeTOML(t, toml)
			got, err := Load(p)
			if err != nil {
				t.Fatalf("Load() codec=%q unexpected error: %v", codec, err)
			}
			if got.Config.Save.Compression != codec {
				t.Errorf("Compression = %q, want %q", got.Config.Save.Compression, codec)
			}
		})
	}
}

// TestLoad_SCPMissingHost returns ErrInvalidField when scp host is empty.
func TestLoad_SCPMissingHost(t *testing.T) {
	t.Setenv(EnvSCPKeyPath, "/tmp/id_rsa")
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = true
host = ""
port = 22
username = "user"
authentification_mode = "key"
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidField) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidField)
	}
}

// TestLoad_SCPInvalidAuthMode returns ErrInvalidSCPAuthMode for unknown mode.
func TestLoad_SCPInvalidAuthMode(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = true
host = "myhost"
port = 22
username = "user"
authentification_mode = "kerberos"
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidSCPAuthMode) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidSCPAuthMode)
	}
}

// TestLoad_SCPValidAuthModes checks all three SCP auth modes.
func TestLoad_SCPValidAuthModes(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		envKey  string
		envVal  string
	}{
		{name: "key", mode: SCPAuthKey, envKey: EnvSCPKeyPath, envVal: "/tmp/id_rsa"},
		{name: "password", mode: SCPAuthPassword, envKey: EnvSCPPassword, envVal: "pass"},
		{name: "none", mode: SCPAuthNone, envKey: "", envVal: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envVal)
			}
			toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = true
host = "myhost"
port = 22
username = "user"
authentification_mode = "` + tt.mode + `"
[save.s3]
enable = false
`
			p := writeTOML(t, toml)
			_, err := Load(p)
			if err != nil {
				t.Errorf("Load() scp mode=%q unexpected error: %v", tt.mode, err)
			}
		})
	}
}

// TestLoad_S3MissingBucket returns ErrInvalidField when bucket_name is empty.
func TestLoad_S3MissingBucket(t *testing.T) {
	t.Setenv(EnvS3SecretKey, "secret")
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = true
bucket_name = ""
s3_access_key = "AKID"
save_path = "prefix"
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidField) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidField)
	}
}

// TestLoad_S3MissingSecretKey returns ErrMissingSecret when env key absent.
func TestLoad_S3MissingSecretKey(t *testing.T) {
	t.Setenv(EnvS3SecretKey, "")
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = false
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = true
bucket_name = "mybucket"
s3_access_key = "AKID"
save_path = "prefix"
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("Load() error = %v, want %v", err, ErrMissingSecret)
	}
}

// TestLoad_LocalMissingDirectory returns ErrInvalidField when local dir is empty.
func TestLoad_LocalMissingDirectory(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = ""
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidField) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidField)
	}
}

// TestLoad_HomeTildeExpansion verifies ~ is expanded in local_save_directory.
func TestLoad_HomeTildeExpansion(t *testing.T) {
	t.Parallel()
	toml := `
[general]
config_version = 0
` + validAPITOML() + `
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "~/snapshots"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := home + "/snapshots"
	if got.Config.Save.Local.LocalSaveDirectory != want {
		t.Errorf("LocalSaveDirectory = %q, want %q", got.Config.Save.Local.LocalSaveDirectory, want)
	}
}

// TestLoad_QueueBufferDefault applies the default when absent from TOML.
func TestLoad_QueueBufferDefault(t *testing.T) {
	t.Parallel()
	p := writeTOML(t, validMinimalTOML(t))
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if got.Config.Queue.BufferSize != DefaultQueueBufferSize {
		t.Errorf("Queue.BufferSize = %d, want %d", got.Config.Queue.BufferSize, DefaultQueueBufferSize)
	}
}

// TestLoad_QueueBufferCustom accepts explicit positive values.
func TestLoad_QueueBufferCustom(t *testing.T) {
	t.Parallel()
	toml := validMinimalTOML(t) + "\n[queue]\nbuffer_size = 128\n"
	p := writeTOML(t, toml)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if got.Config.Queue.BufferSize != 128 {
		t.Errorf("Queue.BufferSize = %d, want 128", got.Config.Queue.BufferSize)
	}
}

// TestLoad_APIHostEmptyWhenEnabled returns ErrInvalidField.
func TestLoad_APIHostEmptyWhenEnabled(t *testing.T) {
	t.Setenv(EnvBasicAuthToken, "tok")
	toml := `
[general]
config_version = 0
[api]
host = ""
port = 2324
authentification_mode = "basic_auth"
[notify]
enable = false
[telemetry]
enable = false
[health]
enable = false
[convertion]
temporary_directory = "/tmp"
[save]
compression = "lz4"
[save.local]
enable = true
local_save_directory = "/tmp/s"
[save.scp]
enable = false
[save.s3]
enable = false
`
	p := writeTOML(t, toml)
	_, err := Load(p)
	if !errors.Is(err, ErrInvalidField) {
		t.Errorf("Load() error = %v, want %v", err, ErrInvalidField)
	}
}

// TestResolveConfigPath_EnvOverride confirms env var takes precedence over default.
func TestResolveConfigPath_EnvOverride(t *testing.T) {
	t.Setenv(EnvConfigPath, "/env/path/config.toml")
	got := resolveConfigPath("")
	if got != "/env/path/config.toml" {
		t.Errorf("resolveConfigPath() = %q, want /env/path/config.toml", got)
	}
}

// TestResolveConfigPath_ExplicitWins verifies explicit path beats env.
func TestResolveConfigPath_ExplicitWins(t *testing.T) {
	t.Setenv(EnvConfigPath, "/env/path/config.toml")
	got := resolveConfigPath("/explicit/config.toml")
	if got != "/explicit/config.toml" {
		t.Errorf("resolveConfigPath() = %q, want /explicit/config.toml", got)
	}
}

// TestResolveConfigPath_DefaultFallback falls back to the compiled constant.
func TestResolveConfigPath_DefaultFallback(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	got := resolveConfigPath("")
	if got != DefaultConfigPath {
		t.Errorf("resolveConfigPath() = %q, want %q", got, DefaultConfigPath)
	}
}

// TestExpandHome_Tilde covers the "~" shorthand for the home dir.
func TestExpandHome_Tilde(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("home dir unavailable")
	}
	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/foo/bar", home + "/foo/bar"},
		{"/absolute/path", "/absolute/path"},
		{"relative", "relative"},
		{"", ""},
		{"~user", "~user"}, // not a home-relative path, returned unchanged
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := expandHome(tt.input)
			if err != nil {
				t.Errorf("expandHome(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
