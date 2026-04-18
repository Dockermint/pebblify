// Package config loads and validates the Pebblify daemon configuration.
//
// The loader reads a TOML file (default ./config.toml, overridable via the
// PEBBLIFY_CONFIG_PATH environment variable) into a typed Config struct,
// then overlays secrets read from environment variables into a separate
// Secrets struct. Secrets never touch the TOML schema.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// SupportedConfigVersion is the maximum config_version this loader understands.
const SupportedConfigVersion = 0

// Default queue buffer size when the [queue] section or its size field is absent.
const DefaultQueueBufferSize = 64

// Compression codec identifiers accepted by the save.compression field.
const (
	CompressionNone = "none"
	CompressionLZ4  = "lz4"
	CompressionZstd = "zstd"
	CompressionGzip = "gzip"
	CompressionZip  = "zip"
)

// Authentication mode identifiers accepted by the api.authentification_mode field.
const (
	APIAuthBasic    = "basic_auth"
	APIAuthUnsecure = "unsecure"
)

// SCP authentication mode identifiers accepted by the save.scp.authentification_mode field.
const (
	SCPAuthKey      = "key"
	SCPAuthPassword = "password"
	SCPAuthNone     = "none"
)

// NotifyModeTelegram is the only notify backend accepted in v0.4.0.
const NotifyModeTelegram = "telegram"

// Environment variable names for secrets and overrides.
const (
	EnvConfigPath       = "PEBBLIFY_CONFIG_PATH"
	EnvLogLevel         = "PEBBLIFY_LOG_LEVEL"
	EnvBasicAuthToken   = "PEBBLIFY_BASIC_AUTH_TOKEN"
	EnvTelegramBotToken = "PEBBLIFY_TELEGRAM_BOT_TOKEN"
	EnvSCPKeyPath       = "PEBBLIFY_SCP_KEY_PATH"
	EnvSCPPassword      = "PEBBLIFY_SCP_PASSWORD"
	EnvS3SecretKey      = "PEBBLIFY_S3_SECRET_KEY"
)

// DefaultConfigPath is used when PEBBLIFY_CONFIG_PATH is unset and no path is
// provided to Load.
const DefaultConfigPath = "./config.toml"

// Sentinel errors returned by Load so callers can match on specific validation
// failures via errors.Is.
var (
	// ErrMissingConfigVersion indicates the [general] config_version field is
	// absent from the TOML file. Operators must set the field explicitly so
	// the loader can migrate or reject old schemas.
	ErrMissingConfigVersion = errors.New("missing general.config_version")
	// ErrUnsupportedConfigVersion indicates the config_version field exceeds
	// SupportedConfigVersion.
	ErrUnsupportedConfigVersion = errors.New("unsupported config_version")
	// ErrInvalidPort indicates a listener port value is outside 1..65535.
	ErrInvalidPort = errors.New("invalid port")
	// ErrInvalidAPIAuthMode indicates api.authentification_mode is unknown.
	ErrInvalidAPIAuthMode = errors.New("invalid api authentification_mode")
	// ErrInvalidNotifyMode indicates notify.mode is unknown.
	ErrInvalidNotifyMode = errors.New("invalid notify mode")
	// ErrInvalidCompression indicates save.compression is unknown.
	ErrInvalidCompression = errors.New("invalid save compression")
	// ErrInvalidSCPAuthMode indicates save.scp.authentification_mode is unknown.
	ErrInvalidSCPAuthMode = errors.New("invalid scp authentification_mode")
	// ErrMissingSecret indicates a required secret env var is unset.
	ErrMissingSecret = errors.New("missing required secret")
	// ErrInvalidField indicates a non-enum field failed validation.
	ErrInvalidField = errors.New("invalid field")
)

// Config is the top-level daemon configuration as loaded from the TOML file.
// It contains only non-secret fields; secrets live in Secrets.
type Config struct {
	General    GeneralSection    `toml:"general"`
	API        APISection        `toml:"api"`
	Notify     NotifySection     `toml:"notify"`
	Telemetry  TelemetrySection  `toml:"telemetry"`
	Health     HealthSection     `toml:"health"`
	Conversion ConversionSection `toml:"conversion"`
	Save       SaveSection       `toml:"save"`
	Queue      QueueSection      `toml:"queue"`
}

// GeneralSection holds the [general] TOML section.
type GeneralSection struct {
	// ConfigVersion is the schema version of the config file.
	ConfigVersion int `toml:"config_version"`
}

// APISection holds the [api] TOML section. The API listener is always active
// in daemon mode as of v0.4.0; there is no enable gate.
type APISection struct {
	// Host is the bind address.
	Host string `toml:"host"`
	// Port is the listener TCP port.
	Port int `toml:"port"`
	// AuthentificationMode is either basic_auth or unsecure.
	AuthentificationMode string `toml:"authentification_mode"`
}

// NotifySection holds the [notify] TOML section.
type NotifySection struct {
	// Enable toggles notifications.
	Enable bool `toml:"enable"`
	// Mode identifies the notifier backend (telegram only in v0.4.0).
	Mode string `toml:"mode"`
	// ChannelID is the target chat / channel identifier.
	ChannelID string `toml:"channel_id"`
}

// TelemetrySection holds the [telemetry] TOML section.
type TelemetrySection struct {
	// Enable toggles the Prometheus listener.
	Enable bool `toml:"enable"`
	// Mode identifies the metrics backend (prometheus only).
	Mode string `toml:"mode"`
	// Host is the bind address.
	Host string `toml:"host"`
	// Port is the listener TCP port.
	Port int `toml:"port"`
}

// HealthSection holds the [health] TOML section.
type HealthSection struct {
	// Enable toggles the health listener.
	Enable bool `toml:"enable"`
	// Host is the bind address.
	Host string `toml:"host"`
	// Port is the listener TCP port.
	Port int `toml:"port"`
}

// ConversionSection holds the [conversion] TOML section.
type ConversionSection struct {
	// TemporaryDirectory is the scratch directory for downloads, extraction,
	// conversion and repacking.
	TemporaryDirectory string `toml:"temporary_directory"`
	// DeleteSourceSnapshot removes the extracted source LevelDB tree once the
	// conversion finishes.
	DeleteSourceSnapshot bool `toml:"delete_source_snapshot"`
}

// SaveSection holds the [save] TOML section.
type SaveSection struct {
	// Compression codec applied when repacking the output archive.
	Compression string `toml:"compression"`
	// Local holds the [save.local] sub-section.
	Local LocalSaveSection `toml:"local"`
	// SCP holds the [save.scp] sub-section.
	SCP SCPSaveSection `toml:"scp"`
	// S3 holds the [save.s3] sub-section.
	S3 S3SaveSection `toml:"s3"`
}

// LocalSaveSection holds the [save.local] TOML section.
type LocalSaveSection struct {
	// Enable toggles the local filesystem storer.
	Enable bool `toml:"enable"`
	// LocalSaveDirectory is the output directory, with ~ expanded at load time.
	LocalSaveDirectory string `toml:"local_save_directory"`
}

// SCPSaveSection holds the [save.scp] TOML section.
type SCPSaveSection struct {
	// Enable toggles the SCP storer.
	Enable bool `toml:"enable"`
	// AuthentificationMode is key, password, or none.
	AuthentificationMode string `toml:"authentification_mode"`
	// Host is the SSH host.
	Host string `toml:"host"`
	// Port is the SSH port.
	Port int `toml:"port"`
	// Username is the SSH login.
	Username string `toml:"username"`
}

// S3SaveSection holds the [save.s3] TOML section.
type S3SaveSection struct {
	// Enable toggles the S3 storer.
	Enable bool `toml:"enable"`
	// BucketName is the destination bucket.
	BucketName string `toml:"bucket_name"`
	// S3AccessKey is the public access key (secret key comes from env).
	S3AccessKey string `toml:"s3_access_key"`
	// SavePath is the key prefix applied to uploaded objects.
	SavePath string `toml:"save_path"`
}

// QueueSection holds the [queue] TOML section. Absent section defaults to
// BufferSize = DefaultQueueBufferSize.
type QueueSection struct {
	// BufferSize is the bounded FIFO buffer capacity.
	BufferSize int `toml:"buffer_size"`
}

// Secrets holds values read exclusively from environment variables. Values
// are never logged and never written back to the TOML file.
type Secrets struct {
	// LogLevel is trace|debug|info|warn|error; empty means use default.
	LogLevel string
	// BasicAuthToken is required when api.authentification_mode = basic_auth.
	BasicAuthToken string
	// TelegramBotToken is required when notify.enable = true and notify.mode = telegram.
	TelegramBotToken string
	// SCPKeyPath is required when save.scp.authentification_mode = key.
	SCPKeyPath string
	// SCPPassword is required when save.scp.authentification_mode = password.
	SCPPassword string
	// S3SecretKey is required when save.s3.enable = true.
	S3SecretKey string
}

// Loaded bundles the parsed Config and the Secrets read from the environment.
type Loaded struct {
	// Config holds TOML-sourced fields.
	Config *Config
	// Secrets holds env-sourced fields.
	Secrets Secrets
}

// Load reads the configuration file and environment-sourced secrets, validates
// them, and returns the populated Loaded value.
//
// Resolution order for the config file path:
//  1. the explicit path argument, if non-empty,
//  2. the PEBBLIFY_CONFIG_PATH env var, if set,
//  3. DefaultConfigPath (./config.toml).
//
// All validation errors wrap sentinel errors from this package so callers may
// branch on errors.Is. Paths containing a leading ~ are expanded against the
// current user's home directory.
func Load(path string) (*Loaded, error) {
	resolved := resolveConfigPath(path)

	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", resolved, err)
	}

	cfg := &Config{}
	meta, err := toml.Decode(string(raw), cfg)
	if err != nil {
		return nil, fmt.Errorf("decode config %s: %w", resolved, err)
	}

	if !meta.IsDefined("general", "config_version") {
		return nil, fmt.Errorf("%w: set general.config_version=%d",
			ErrMissingConfigVersion, SupportedConfigVersion)
	}

	if err := applyDefaults(cfg); err != nil {
		return nil, err
	}

	secrets := readSecrets()

	if err := validate(cfg, secrets); err != nil {
		return nil, err
	}

	return &Loaded{Config: cfg, Secrets: secrets}, nil
}

// resolveConfigPath returns the first non-empty path among the argument, the
// env override, and the compiled-in default.
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if fromEnv := os.Getenv(EnvConfigPath); fromEnv != "" {
		return fromEnv
	}
	return DefaultConfigPath
}

// readSecrets snapshots the environment once. Values are stored verbatim;
// validation checks which ones are required given the parsed Config.
func readSecrets() Secrets {
	return Secrets{
		LogLevel:         os.Getenv(EnvLogLevel),
		BasicAuthToken:   os.Getenv(EnvBasicAuthToken),
		TelegramBotToken: os.Getenv(EnvTelegramBotToken),
		SCPKeyPath:       os.Getenv(EnvSCPKeyPath),
		SCPPassword:      os.Getenv(EnvSCPPassword),
		S3SecretKey:      os.Getenv(EnvS3SecretKey),
	}
}

// applyDefaults fills in computed or path-expanded fields that depend on the
// runtime environment (e.g. ~ expansion, queue buffer default).
func applyDefaults(cfg *Config) error {
	if cfg.Queue.BufferSize == 0 {
		cfg.Queue.BufferSize = DefaultQueueBufferSize
	}

	expanded, err := expandHome(cfg.Save.Local.LocalSaveDirectory)
	if err != nil {
		return fmt.Errorf("expand save.local.local_save_directory: %w", err)
	}
	cfg.Save.Local.LocalSaveDirectory = expanded

	expandedTmp, err := expandHome(cfg.Conversion.TemporaryDirectory)
	if err != nil {
		return fmt.Errorf("expand conversion.temporary_directory: %w", err)
	}
	cfg.Conversion.TemporaryDirectory = expandedTmp

	return nil
}

// expandHome replaces a leading ~ or ~/ with the current user's home directory.
// Any other path is returned unchanged.
func expandHome(p string) (string, error) {
	if p == "" || (p[0] != '~') {
		return p, nil
	}
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// validate enforces cross-field invariants defined in the daemon spec.
func validate(cfg *Config, secrets Secrets) error {
	if cfg.General.ConfigVersion != SupportedConfigVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedConfigVersion,
			cfg.General.ConfigVersion, SupportedConfigVersion)
	}

	if err := validatePorts(cfg); err != nil {
		return err
	}
	if err := validateAPI(cfg, secrets); err != nil {
		return err
	}
	if err := validateNotify(cfg, secrets); err != nil {
		return err
	}
	if err := validateSave(cfg, secrets); err != nil {
		return err
	}
	if cfg.Queue.BufferSize < 1 {
		return fmt.Errorf("%w: queue.buffer_size must be >= 1", ErrInvalidField)
	}
	return nil
}

// validatePorts ensures every enabled listener has a port in the valid range.
// The API listener is always active, so api.port is always checked.
func validatePorts(cfg *Config) error {
	if err := checkPort("api.port", cfg.API.Port); err != nil {
		return err
	}
	if cfg.Telemetry.Enable {
		if err := checkPort("telemetry.port", cfg.Telemetry.Port); err != nil {
			return err
		}
	}
	if cfg.Health.Enable {
		if err := checkPort("health.port", cfg.Health.Port); err != nil {
			return err
		}
	}
	if cfg.Save.SCP.Enable {
		if err := checkPort("save.scp.port", cfg.Save.SCP.Port); err != nil {
			return err
		}
	}
	return nil
}

// checkPort returns ErrInvalidPort if p is outside 1..65535.
func checkPort(field string, p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("%w: %s=%d (must be 1..65535)", ErrInvalidPort, field, p)
	}
	return nil
}

// validateAPI enforces the API subsystem invariants. The API listener is
// always active in daemon mode, so these checks run unconditionally.
func validateAPI(cfg *Config, secrets Secrets) error {
	switch cfg.API.AuthentificationMode {
	case APIAuthBasic:
		if secrets.BasicAuthToken == "" {
			return fmt.Errorf("%w: %s required when api.authentification_mode=basic_auth",
				ErrMissingSecret, EnvBasicAuthToken)
		}
	case APIAuthUnsecure:
		// No secret required; orchestrator emits a WARN log at startup.
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAPIAuthMode, cfg.API.AuthentificationMode)
	}
	if cfg.API.Host == "" {
		return fmt.Errorf("%w: api.host must not be empty", ErrInvalidField)
	}
	return nil
}

// validateNotify enforces the notify subsystem invariants.
func validateNotify(cfg *Config, secrets Secrets) error {
	if !cfg.Notify.Enable {
		return nil
	}
	if cfg.Notify.Mode != NotifyModeTelegram {
		return fmt.Errorf("%w: %q (only %q supported in v0.4.0)",
			ErrInvalidNotifyMode, cfg.Notify.Mode, NotifyModeTelegram)
	}
	if secrets.TelegramBotToken == "" {
		return fmt.Errorf("%w: %s required when notify.enable=true and notify.mode=telegram",
			ErrMissingSecret, EnvTelegramBotToken)
	}
	if cfg.Notify.ChannelID == "" {
		return fmt.Errorf("%w: notify.channel_id must not be empty when notify.enable=true",
			ErrInvalidField)
	}
	return nil
}

// validateSave enforces the save subsystem invariants across local, scp, and s3.
func validateSave(cfg *Config, secrets Secrets) error {
	switch cfg.Save.Compression {
	case CompressionNone, CompressionLZ4, CompressionZstd, CompressionGzip, CompressionZip:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidCompression, cfg.Save.Compression)
	}

	if cfg.Save.Local.Enable && cfg.Save.Local.LocalSaveDirectory == "" {
		return fmt.Errorf("%w: save.local.local_save_directory required when save.local.enable=true",
			ErrInvalidField)
	}

	if cfg.Save.SCP.Enable {
		if cfg.Save.SCP.Host == "" {
			return fmt.Errorf("%w: save.scp.host required when save.scp.enable=true",
				ErrInvalidField)
		}
		if cfg.Save.SCP.Username == "" {
			return fmt.Errorf("%w: save.scp.username required when save.scp.enable=true",
				ErrInvalidField)
		}
		switch cfg.Save.SCP.AuthentificationMode {
		case SCPAuthKey:
			if secrets.SCPKeyPath == "" {
				return fmt.Errorf("%w: %s required when save.scp.authentification_mode=key",
					ErrMissingSecret, EnvSCPKeyPath)
			}
		case SCPAuthPassword:
			if secrets.SCPPassword == "" {
				return fmt.Errorf("%w: %s required when save.scp.authentification_mode=password",
					ErrMissingSecret, EnvSCPPassword)
			}
		case SCPAuthNone:
			// No secret required.
		default:
			return fmt.Errorf("%w: %q", ErrInvalidSCPAuthMode, cfg.Save.SCP.AuthentificationMode)
		}
	}

	if cfg.Save.S3.Enable {
		if cfg.Save.S3.BucketName == "" {
			return fmt.Errorf("%w: save.s3.bucket_name required when save.s3.enable=true",
				ErrInvalidField)
		}
		if cfg.Save.S3.S3AccessKey == "" {
			return fmt.Errorf("%w: save.s3.s3_access_key required when save.s3.enable=true",
				ErrInvalidField)
		}
		if secrets.S3SecretKey == "" {
			return fmt.Errorf("%w: %s required when save.s3.enable=true",
				ErrMissingSecret, EnvS3SecretKey)
		}
	}

	return nil
}
