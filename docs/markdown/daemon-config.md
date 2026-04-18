# Daemon Configuration Reference

The Pebblify daemon is configured entirely through a TOML file and environment variables. Secrets are never stored in the TOML file.

## Config file location

Resolution order (first non-empty value wins):

1. Explicit path passed by the runtime (not currently a CLI flag; see env var below)
2. `PEBBLIFY_CONFIG_PATH` environment variable
3. `./config.toml` (default)

Source: `internal/daemon/config/config.go:resolveConfigPath`

## Schema version

Every config file must begin with:

```toml
[general]
config_version = 0
```

The loader (`internal/daemon/config/config.go:Load`) rejects any file where `general.config_version` is absent or exceeds the supported maximum (`SupportedConfigVersion = 0`). This ensures forward-incompatible configs are caught at startup rather than silently misread.

## Full reference

### [general]

| Key              | Type | Default | Description                                  |
| :--------------- | :--- | :------ | :------------------------------------------- |
| `config_version` | int  | —       | Required. Schema version. Must be `0` in v0.4.0. |

### [api]

The API listener is always active in daemon mode. There is no enable gate.

| Key                     | Type   | Default       | Valid values              | Env override | Notes                                                                        |
| :---------------------- | :----- | :------------ | :------------------------ | :----------- | :--------------------------------------------------------------------------- |
| `host`                  | string | `"127.0.0.1"` | Any bind address          | —            | Bind address for the API listener.                                           |
| `port`                  | int    | `2324`        | `1–65535`                 | —            | TCP port for the API listener.                                               |
| `authentification_mode` | string | `"basic_auth"` | `basic_auth`, `unsecure` | —            | `basic_auth` requires `PEBBLIFY_BASIC_AUTH_TOKEN`. `unsecure` emits a WARN log at startup. |

Security note: `authentification_mode = "unsecure"` is not recommended for production. The daemon logs `"api listener running without authentication; set api.authentification_mode=basic_auth for production"` at WARN level on every startup when this mode is active (per `internal/daemon/api/server.go:New`).

### [notify]

| Key          | Type   | Default      | Valid values | Env override                   | Notes                                                         |
| :----------- | :----- | :----------- | :----------- | :----------------------------- | :------------------------------------------------------------ |
| `enable`     | bool   | `false`      | `true`, `false` | —                           | Toggles the notification subsystem.                          |
| `mode`       | string | `"telegram"` | `telegram`   | —                              | Only `telegram` is valid in v0.4.0. Other values are a fatal startup error. |
| `channel_id` | string | `""`         | Chat ID string | —                            | Required when `enable = true`. Telegram chat or channel ID.  |

When `notify.enable = true`, `PEBBLIFY_TELEGRAM_BOT_TOKEN` is required (see Environment Variables below). Missing either `channel_id` or the bot token is a fatal startup error.

### [telemetry]

| Key      | Type   | Default       | Valid values | Notes                                            |
| :------- | :----- | :------------ | :----------- | :------------------------------------------------ |
| `enable` | bool   | `true`        | `true`, `false` | Toggles the Prometheus `/metrics` listener.   |
| `mode`   | string | `"prometheus"` | `prometheus` | Backend type. Only `prometheus` is valid.       |
| `host`   | string | `"127.0.0.1"` | Any bind address | Bind address for the telemetry listener.    |
| `port`   | int    | `2323`        | `1–65535`    | TCP port for the telemetry listener.             |

### [health]

| Key      | Type   | Default       | Valid values    | Notes                                           |
| :------- | :----- | :------------ | :-------------- | :---------------------------------------------- |
| `enable` | bool   | `true`        | `true`, `false` | Toggles the health probe listener.              |
| `host`   | string | `"127.0.0.1"` | Any bind address | Bind address for the health listener.          |
| `port`   | int    | `2325`        | `1–65535`       | TCP port for the health listener.              |

Health endpoints: `GET /healthz` (liveness, always 200 while running), `GET /readyz` (readiness, 200 only when queue is empty and no job is running).

### [convertion]

Note: the key name `convertion` matches the Go struct tag and config file spelling. It is intentionally preserved to avoid a breaking schema change.

| Key                      | Type   | Default  | Notes                                                                                       |
| :----------------------- | :----- | :------- | :------------------------------------------------------------------------------------------- |
| `temporary_directory`    | string | `"/tmp"` | Scratch directory for download, extraction, conversion, and repacking. Supports `~` prefix. |
| `delete_source_snapshot` | bool   | `true`   | When true, the extracted source LevelDB directory is removed after conversion.               |

The loader expands a leading `~` using `os.UserHomeDir()` (per `internal/daemon/config/config.go:expandHome`).

### [save]

| Key           | Type   | Default | Valid values              | Notes                                        |
| :------------ | :----- | :------ | :------------------------ | :------------------------------------------- |
| `compression` | string | `"lz4"` | `none`, `lz4`, `zstd`, `gzip`, `zip` | Codec applied when repacking the output archive. |

### [save.local]

| Key                    | Type   | Default          | Notes                                                             |
| :--------------------- | :----- | :--------------- | :---------------------------------------------------------------- |
| `enable`               | bool   | `true`           | Toggles the local filesystem storer.                             |
| `local_save_directory` | string | `"~/.snapshots"` | Output directory. Supports `~` prefix. Required when `enable = true`. |

### [save.scp]

| Key                     | Type   | Default | Valid values                  | Env override          | Notes                                          |
| :---------------------- | :----- | :------ | :---------------------------- | :-------------------- | :--------------------------------------------- |
| `enable`                | bool   | `false` | `true`, `false`               | —                     | Toggles the SCP storer.                        |
| `authentification_mode` | string | `"key"` | `key`, `password`, `none`     | —                     | SSH authentication method.                    |
| `host`                  | string | `""`    | Hostname or IP                | —                     | Required when `enable = true`.                |
| `port`                  | int    | `0`     | `1–65535`                     | —                     | SSH port. Required when `enable = true`.      |
| `username`              | string | `""`    | SSH login name                | —                     | Required when `enable = true`.                |

SCP secret env vars (required when the corresponding mode is active):

- `PEBBLIFY_SCP_KEY_PATH` — path to private key file; required when `authentification_mode = key`
- `PEBBLIFY_SCP_PASSWORD` — required when `authentification_mode = password`

### [save.s3]

| Key            | Type   | Default | Notes                                                                               |
| :------------- | :----- | :------ | :---------------------------------------------------------------------------------- |
| `enable`       | bool   | `false` | Toggles the S3 storer.                                                              |
| `bucket_name`  | string | `""`    | Destination S3 bucket. Required when `enable = true`.                              |
| `s3_access_key`| string | `""`    | AWS access key ID (public part). Required when `enable = true`.                    |
| `save_path`    | string | `""`    | Key prefix for uploaded objects. Final key: `<save_path>/<filename>`. May be empty. |

Security note: the S3 secret key is read from `PEBBLIFY_S3_SECRET_KEY` only. It is never stored in `config.toml`.

S3 region resolution order (per `internal/daemon/store/s3/s3.go:resolveRegion`):

1. `AWS_REGION` environment variable
2. `AWS_DEFAULT_REGION` environment variable
3. AWS SDK default config chain (`~/.aws/config`)
4. Falls back to `us-east-1` with a WARN log

### [queue]

This section is optional. If absent, defaults apply.

| Key           | Type | Default | Valid values | Notes                                                              |
| :------------ | :--- | :------ | :----------- | :----------------------------------------------------------------- |
| `buffer_size` | int  | `64`    | `>= 1`       | Maximum number of pending jobs in the FIFO buffer. A full buffer causes new submissions to return HTTP 503. |

Source: `internal/daemon/config/config.go:DefaultQueueBufferSize`

## Environment variables

All secrets and overrides are read from the environment at daemon startup. Missing required secrets for an enabled subsystem cause a fatal startup error (exit 1 before any listener opens).

| Variable                    | Required when                                           | Description                                      |
| :-------------------------- | :------------------------------------------------------ | :----------------------------------------------- |
| `PEBBLIFY_CONFIG_PATH`      | Never                                                   | Overrides the default `./config.toml` path.      |
| `PEBBLIFY_LOG_LEVEL`        | Never                                                   | Log verbosity: `trace`, `debug`, `info`, `warn`, `error`. Default: `info`. |
| `PEBBLIFY_BASIC_AUTH_TOKEN` | `api.authentification_mode = basic_auth`                | API bearer token. Compared using HMAC-SHA-256 constant-time comparison. |
| `PEBBLIFY_TELEGRAM_BOT_TOKEN` | `notify.enable = true` and `notify.mode = telegram`  | Telegram Bot API token. Never logged.            |
| `PEBBLIFY_SCP_KEY_PATH`     | `save.scp.enable = true` and `authentification_mode = key`      | Path to SSH private key file.        |
| `PEBBLIFY_SCP_PASSWORD`     | `save.scp.enable = true` and `authentification_mode = password` | SSH login password.                  |
| `PEBBLIFY_S3_SECRET_KEY`    | `save.s3.enable = true`                                 | AWS secret access key.                           |

Source: `internal/daemon/config/config.go` (constants `EnvConfigPath` through `EnvS3SecretKey`)

## Validation errors

The loader returns typed sentinel errors so callers can branch on specific failures:

| Error                        | Cause                                                          |
| :--------------------------- | :------------------------------------------------------------- |
| `ErrMissingConfigVersion`    | `general.config_version` key is absent from the TOML file.    |
| `ErrUnsupportedConfigVersion`| `general.config_version` exceeds `SupportedConfigVersion (0)`. |
| `ErrInvalidPort`             | A listener port is outside `1–65535`.                         |
| `ErrInvalidAPIAuthMode`      | `api.authentification_mode` is not `basic_auth` or `unsecure`. |
| `ErrInvalidNotifyMode`       | `notify.mode` is not `telegram`.                              |
| `ErrInvalidCompression`      | `save.compression` is not one of the valid codec names.       |
| `ErrInvalidSCPAuthMode`      | `save.scp.authentification_mode` is not `key`, `password`, or `none`. |
| `ErrMissingSecret`           | A required secret environment variable is unset.              |
| `ErrInvalidField`            | A non-enum field failed validation (e.g. empty host, empty directory). |
