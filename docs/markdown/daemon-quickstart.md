# Daemon Quickstart

Run Pebblify as a long-running service that accepts snapshot conversion jobs over HTTP.

**Platform note:** `pebblify daemon` runs on Linux only. macOS users must run the daemon inside Docker or Podman — see the [macOS path](#macos-podman-or-docker) section below.

## Prerequisites

- Linux host (amd64 or arm64)
- Pebblify binary installed (see [CLI install](./install-cli.md) or `make install-cli`)
- Sufficient disk space in the temporary directory (default `/tmp`)
- A `config.toml` accessible to the daemon process

## Step 1: Download the default config

Copy `config.toml` from the repository or create it manually:

```bash
curl -fsSL https://raw.githubusercontent.com/Dockermint/pebblify/main/config.toml \
  -o ./config.toml
```

The minimal working config with local save and basic auth:

```toml
[general]
config_version = 0

[api]
host = "127.0.0.1"
port = 2324
authentification_mode = "basic_auth"

[notify]
enable = false
mode = "telegram"
channel_id = ""

[telemetry]
enable = true
mode = "prometheus"
host = "127.0.0.1"
port = 2323

[health]
enable = true
host = "127.0.0.1"
port = 2325

[convertion]
temporary_directory = "/tmp"
delete_source_snapshot = true

[save]
compression = "lz4"

[save.local]
enable = true
local_save_directory = "~/.snapshots"

[save.scp]
enable = false
authentification_mode = "key"
host = ""
port = 0
username = ""

[save.s3]
enable = false
bucket_name = ""
s3_access_key = ""
save_path = ""
```

## Step 2: Set required environment variables

When `api.authentification_mode = "basic_auth"`, you must export a token before starting the daemon. This token is compared against every API request.

```bash
export PEBBLIFY_BASIC_AUTH_TOKEN="your-secret-token-here"
```

All secrets are read from environment variables only. They are never stored in `config.toml`. See [daemon-config.md](./daemon-config.md) for the full list.

## Step 3: Start the daemon

```bash
pebblify daemon
```

The daemon loads `./config.toml` by default. To use a different path:

```bash
PEBBLIFY_CONFIG_PATH=/etc/pebblify/config.toml pebblify daemon
```

On successful startup you will see structured JSON logs on stderr:

```
{"time":"...","level":"INFO","msg":"pebblify daemon started","version":"0.4.0","health_enabled":true,"telemetry_enabled":true,"notify_enabled":false,"save_targets":1}
{"time":"...","level":"INFO","msg":"api listener started","addr":"127.0.0.1:2324"}
{"time":"...","level":"INFO","msg":"health listener started","addr":"127.0.0.1:2325"}
{"time":"...","level":"INFO","msg":"telemetry listener started","addr":"127.0.0.1:2323"}
```

## Step 4: Submit a conversion job

Use `POST /v1/jobs` to submit a snapshot archive URL. The daemon downloads the archive, converts every LevelDB directory it contains, repacks the output, and saves it to the configured targets.

```bash
curl -s -X POST http://127.0.0.1:2324/v1/jobs \
  -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://snapshots.example.com/gaia-snapshot-20260418.tar.lz4"}'
```

A successful response returns HTTP 201 Created:

```json
{
  "job_id": "3f8a2c1d...",
  "queue_depth": 1
}
```

## Step 5: Check status

```bash
curl -s http://127.0.0.1:2324/v1/status \
  -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}"
```

```json
{
  "version": "0.4.0",
  "queue_depth": 0,
  "current": null,
  "uptime_seconds": 42
}
```

While a job is running:

```json
{
  "version": "0.4.0",
  "queue_depth": 0,
  "current": {
    "job_id": "3f8a2c1d...",
    "url": "https://snapshots.example.com/gaia-snapshot-20260418.tar.lz4",
    "submitted_at": "2026-04-18T10:00:00Z"
  },
  "uptime_seconds": 87
}
```

## Step 6: Check health

```bash
# Liveness (200 when process is alive)
curl -s http://127.0.0.1:2325/healthz

# Readiness (200 when idle, 503 when busy)
curl -s http://127.0.0.1:2325/readyz
```

## macOS: Podman or Docker

On macOS, `pebblify daemon` exits immediately with:

```
pebblify daemon is Linux-only (current: darwin/arm64)
On macOS, run the daemon via Docker or Podman.
```

Run via Docker Compose instead:

```bash
# Set your token in .env
echo "PEBBLIFY_BASIC_AUTH_TOKEN=your-secret-token-here" > .env

# Start
docker compose -f docker-compose.daemon.yml up -d
```

Or via Podman (after running `make install-podman` on a machine with Podman Desktop):

```bash
systemctl --user start pebblify
```

See [daemon-api.md](./daemon-api.md) for the full API reference and [daemon-config.md](./daemon-config.md) for all configuration options.
