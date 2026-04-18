# Spec: Daemon Mode

Owner: `@software-architect`
Date: 2026-04-18
Last revised: 2026-04-18 (CEO decisions locked — see section "CEO Decisions Locked 2026-04-18")
Feature branch: `feat/daemon-mode`
Implementation owner: `@go-developer`, `@lead-dev`, `@container-engineer`

---

## CEO Decisions Locked 2026-04-18

The five open questions from the initial draft were answered by the CEO on 2026-04-18. All decisions below are locked and must not be re-opened without a new CEO confirmation.

1. **S3 backend**: `aws-sdk-go-v2` confirmed. `@lead-dev` must import only the required sub-modules (at minimum: `github.com/aws/aws-sdk-go-v2/config`, `github.com/aws/aws-sdk-go-v2/credentials`, `github.com/aws/aws-sdk-go-v2/service/s3`). No other aws-sdk-go-v2 service modules may be added without explicit CEO approval. The fallback (hand-rolled SigV4) is removed from scope.

2. **Notify scope**: Telegram only for v0.4.0. Webhook is deferred to backlog. The `Notifier` interface (see below) is designed for drop-in extension; adding a webhook or other backend in a future release requires only a new implementation file and a config enum value, with no changes to the orchestrator or config schema.

3. **Concurrency model**: In-memory FIFO queue with a single worker goroutine. Full design in the "Job Queue" section below.

4. **systemd unit ownership**: Reassigned to `@container-engineer`. See "systemd Unit Ownership" section and the CLAUDE.md scope amendment flag.

5. **Platform**: Daemon is Linux-only. macOS users run the daemon via Docker or Podman. The `install-cli` target remains cross-platform (all four build targets). `install-systemd-daemon` is Linux-only. There is no `install-launchd-daemon` target in v0.4.0 or any planned release.

---

## Overview

`pebblify daemon` is a long-running HTTP service. It accepts snapshot archive URLs via a REST API, downloads and converts them from LevelDB to PebbleDB format, repacks the output with the configured compression codec, and pushes the result to one or more save targets (local directory, SCP, S3). All configuration is file- and env-driven; the daemon subcommand accepts no positional arguments or conversion flags.

Existing subcommands (`level-to-pebble`, `recover`, `verify`, `completion`) are unchanged.

**Platform constraint**: the `daemon` subcommand is Linux-only. The binary compiles on all four mandatory targets, but `pebblify daemon` at runtime requires Linux (systemd, `/proc`, inotify assumptions in the job pipeline). macOS users must run the daemon inside a Docker or Podman container. A build-tag guard (`//go:build linux`) is placed on `daemon.go`; the `cmd/pebblify/main.go` switch case for `"daemon"` is also guarded by the same build tag. Attempting to run the binary built for darwin with the `daemon` subcommand prints `pebblify daemon is not supported on this platform; use Docker or Podman` and exits 1.

---

## Package Layout

```
internal/daemon/
  config/
    config.go        -- TOML loader, env overlay, validation, Config struct
  api/
    server.go        -- HTTP server construction, route wiring
    middleware.go    -- auth middleware (basic_auth / unsecure)
    handler.go       -- POST /job handler, request validation, queue submission
    types.go         -- request/response types
  notify/
    notifier.go      -- Notifier interface
    telegram.go      -- Telegram implementation
    noop.go          -- no-op implementation (notify disabled)
  queue/
    queue.go         -- in-memory FIFO queue, dedup logic, shutdown gate
  repack/
    repacker.go      -- Repacker interface
    lz4.go           -- lz4 implementation
    zstd.go          -- zstd implementation
    gzip.go          -- gzip implementation
    none.go          -- passthrough (no compression) implementation
  store/
    storer.go        -- Storer interface
    local.go         -- local filesystem implementation
    scp.go           -- SCP/SSH implementation
    s3.go            -- S3 implementation (aws-sdk-go-v2)
  orchestrator.go    -- wires config + all sub-packages, runs job pipeline
  daemon.go          -- Run(cfg) entry point called from cmd/pebblify (linux build tag)
```

Sub-packages are independently testable. The orchestrator depends on interfaces, not concrete types.

---

## Interfaces

### Notifier

```
type Notifier interface {
    Notify(ctx context.Context, msg string) error
}
```

Implementations: `TelegramNotifier`, `NoopNotifier`.

**`TelegramNotifier` implementation** (`internal/daemon/notify/telegram.go`):
- Uses `net/http.Client` directly against `https://api.telegram.org/bot<token>/sendMessage`. No third-party Telegram library.
- Inputs: `PEBBLIFY_TELEGRAM_BOT_TOKEN` env var (bot token) + `notify.channel_id` config field (chat ID). Both are required when `notify.enable = true` and `notify.mode = "telegram"`; missing either is a fatal startup error.
- Retry policy: on HTTP 5xx response, retry once after an exponential backoff interval (base 2 s, factor 2). On HTTP 4xx response, log at ERROR level and return immediately (permanent failure — no retry). Notification failure is non-fatal to the job pipeline: the worker logs the error and continues.
- The bot token is never logged. Log lines reference only the chat ID and HTTP status code.

**Extension path**: to add a new backend (webhook, Slack, PagerDuty) in a future release:
1. Create a new file in `internal/daemon/notify/` implementing `Notifier`.
2. Add a new enum value to `config.notify.mode`.
3. Wire the new implementation in the orchestrator's factory function.
No other files change. The `Notifier` interface contract is intentionally minimal (`Notify(ctx, msg) error`) so all backends fit without interface widening.

### Repacker

```
type Repacker interface {
    // Pack reads from src archive path, writes repacked archive to dst path.
    // nonDBFiles must be preserved from the original archive unchanged.
    Pack(ctx context.Context, src, dst string) error
    // Extension returns the file extension this repacker produces, e.g. ".tar.lz4".
    Extension() string
}
```

Implementations: `LZ4Repacker`, `ZstdRepacker`, `GzipRepacker`, `NoneRepacker`.

### Storer

```
type Storer interface {
    // Store uploads the file at localPath to the configured target.
    // Returns the remote path/URL for logging.
    Store(ctx context.Context, localPath, fileName string) (string, error)
}
```

Implementations: `LocalStorer`, `SCPStorer`, `S3Storer`.

Multiple Storers may be active simultaneously. The orchestrator iterates enabled stores sequentially. Partial store failures are non-fatal (logged + notified) unless all stores fail, which is treated as a fatal job error.

---

## Configuration

### File

Default path: `./config.toml`. Overridden by `PEBBLIFY_CONFIG_PATH` env var.

```toml
[general]
config_version = 0

[api]
enable = false
host = "127.0.0.1"
port = 2324
authentification_mode = "basic_auth"  # basic_auth | unsecure

[notify]
enable = false
mode = "telegram"                     # telegram (only valid value in v0.4.0)
channel_id = "..."

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
compression = "lz4"  # none | lz4 | zstd | gzip

[save.local]
enable = true
local_save_directory = "~/.snapshots"

[save.scp]
enable = false
authentification_mode = "key"  # key | password | none
host = ""
port = 0
username = ""

[save.s3]
enable = false
bucket_name = ""
s3_access_key = ""
save_path = ""
```

Schema version: `config_version = 0`. The loader rejects configs where `config_version` is absent or exceeds the supported maximum, with a clear error message directing the user to migrate.

Field notes:
- `api.authentification_mode = "unsecure"` emits a WARN-level log line at startup: `daemon API is running without authentication; set authentification_mode to basic_auth for production use`.
- `save.local.local_save_directory` supports `~` prefix; the loader expands it via `os.UserHomeDir()`.
- `save.s3.save_path` is the key prefix; the final S3 key is `/<bucket_name>/<save_path>/<filename>`.
- `notify.mode` accepts only `"telegram"` in v0.4.0. Any other value is a fatal startup error. The enum is validated at config load time so future modes can be added without changing the loader signature.

### Environment Variables

```
PEBBLIFY_LOG_LEVEL=info            # trace | debug | info | warn | error
PEBBLIFY_CONFIG_PATH=              # overrides default ./config.toml
PEBBLIFY_BASIC_AUTH_TOKEN=         # required when authentification_mode = basic_auth
PEBBLIFY_TELEGRAM_BOT_TOKEN=       # required when notify.mode = telegram
PEBBLIFY_SCP_KEY_PATH=             # path to private key; required when scp.authentification_mode = key
PEBBLIFY_SCP_PASSWORD=             # required when scp.authentification_mode = password
PEBBLIFY_S3_SECRET_KEY=            # required when save.s3.enable = true
```

All secret env vars are read at startup. Missing required secrets for an enabled subsystem are a fatal startup error (logged + exit 1 before any listener opens).

---

## Job Queue

### Design

The daemon maintains an in-memory FIFO queue and a single worker goroutine that consumes it. Rationale: PebbleDB and LevelDB are I/O-heavy; parallelizing multiple conversions on the same host causes disk bandwidth contention without a meaningful throughput gain, and complicates cleanup on failure. Serial processing is the correct default; bounded concurrency is a post-v0.4.0 backlog item.

**Queue persistence**: the queue is in-memory only. If the daemon crashes or is stopped, all queued (not yet started) jobs are lost. This is intentional and documented. The operator must resubmit jobs after a restart. The `GET /v1/status` endpoint reflects the current queue depth so operators can verify queue state before stopping the daemon.

### Queue Mechanics

- Data structure: unbounded channel (`chan Job`) used as a FIFO queue. Channel is created with a configurable buffer (default 64); if the buffer fills, new enqueue requests return 503 (not 409 — 409 is reserved for duplicate URLs).
- Single worker goroutine: started at daemon startup, runs until the shutdown channel is closed.
- Dedup key: canonicalized snapshot URL. Canonicalization normalizes scheme (lowercased), host (lowercased), path (cleaned via `path.Clean`), and query parameters (sorted by key, then by value). Fragment is discarded. Two URLs that canonicalize to the same string are considered duplicates.
- Dedup scope: a URL is a duplicate if it is currently running OR currently in the queue. Completed or failed jobs are not tracked for dedup purposes; the same URL may be submitted again after the job finishes.

### Enqueue Rules

| Condition                                                  | HTTP Response                              |
| :--------------------------------------------------------- | :----------------------------------------- |
| URL canonicalizes to a running or queued job               | 409 Conflict `{"error":"duplicate job: url already running or queued"}` |
| Queue buffer full (64 pending jobs)                        | 503 Service Unavailable `{"error":"queue full"}` |
| Daemon is shutting down (SIGTERM received)                 | 503 Service Unavailable `{"error":"daemon shutting down"}` |
| Disk space check fails                                     | 507 Insufficient Storage                   |
| URL validation fails                                       | 400 Bad Request                            |
| All checks pass                                            | 202 Accepted `{"job_id":"<uuid>","status":"queued"}` |

### Shutdown Sequence (SIGTERM)

1. Signal handler receives SIGTERM.
2. Daemon sets a shutdown flag; the enqueue path returns 503 for all new requests.
3. The HTTP API listener stops accepting new connections (graceful shutdown with a 30-second timeout).
4. The worker goroutine finishes the job currently in progress (no mid-job interruption). All cleanup steps (temp directory removal, notification) run to completion.
5. Jobs remaining in the queue that have not started are dropped. A WARN-level log entry is emitted for each dropped job, listing its URL.
6. Daemon exits 0 after the current job completes and all listeners have shut down.

If the current job does not complete within 5 minutes of SIGTERM, the daemon forces exit 1 and logs the timeout. This prevents indefinite hang on a stalled download.

### State Exposed via API and Prometheus

`GET /v1/status` returns `queue_depth` (number of jobs waiting, not counting the running job) and `current_job` (null if idle).

Prometheus gauge `pebblify_daemon_queue_depth` mirrors `queue_depth` in real time.

`/readyz` returns 503 whenever `queue_depth > 0` or a job is running (daemon is busy). Returns 200 only when queue is empty and no job is running.

---

## API

### Listener

Bound to `api.host:api.port`. Enabled only when `api.enable = true`.

HTTP/1.1, no TLS (operators terminate TLS at a reverse proxy). Future versions may add TLS config.

### Authentication

- `basic_auth`: HTTP Basic Auth. Username is ignored. Password is compared to `PEBBLIFY_BASIC_AUTH_TOKEN` using `subtle.ConstantTimeCompare`. Request with invalid token → 401 Unauthorized.
- `unsecure`: no auth. Warn at startup (see above).

### Routes

```
POST /v1/job      Submit a conversion job (enqueues; returns 202 immediately)
GET  /v1/status   Return daemon state, queue depth, and current job info (if any)
```

#### POST /v1/job

Request body (JSON):

```json
{
  "snapshot_url": "https://example.com/snapshot.tar.lz4"
}
```

Validation:
- `snapshot_url` must be a valid URL (scheme http or https).
- Archive format is inferred from the URL file extension. Supported: `.tar`, `.tar.gz`, `.tar.lz4`, `.tar.zst`, `.zip`. Rejection if extension is unrecognized.
- Daemon checks free disk space in `convertion.temporary_directory` before accepting the job. Minimum required: estimated archive size * 4 (download + extract + convert + repack). If insufficient, return 507 Insufficient Storage.
- Dedup check: if the canonicalized URL matches a running or queued job, return 409 Conflict.
- If daemon is shutting down, return 503.

Response (202 Accepted):

```json
{
  "job_id": "<uuid>",
  "status": "queued"
}
```

Response (4xx/5xx):

```json
{
  "error": "<message>"
}
```

#### GET /v1/status

Response (200 OK):

```json
{
  "daemon_version": "0.4.0",
  "state": "idle",        // idle | converting | error
  "queue_depth": 0,       // number of jobs waiting (not counting running job)
  "current_job": null     // null when idle, or job object
}
```

Job object:

```json
{
  "job_id": "<uuid>",
  "snapshot_url": "...",
  "phase": "downloading",  // downloading | extracting | converting | verifying | repacking | storing | notifying
  "started_at": "<rfc3339>",
  "elapsed_seconds": 42
}
```

---

## Job Pipeline (Orchestrator)

The orchestrator processes one job at a time via the single worker goroutine. Jobs are accepted into the queue immediately (202 response) and processed serially.

```
Step 1: Dequeue job from FIFO queue (worker goroutine blocks until job available)
Step 2: Validate free disk space in convertion.temporary_directory
Step 3: Download archive to convertion.temporary_directory/<job_id>/download/<filename>
Step 4: Extract archive; detect all LevelDB directories within the extracted tree
Step 5: For each LevelDB directory: call internal/migration.RunLevelToPebble
        - Uses internal migration + verify packages; reuses existing RunConfig interface
        - No crash recovery in daemon mode (stateless per job)
        - If convertion.delete_source_snapshot = true → remove source LevelDB dir after conversion
Step 6: Run internal/verify.Run on each converted DB (full verification; no sampling skip in daemon)
Step 7: Repack entire output directory (converted PebbleDB dirs + non-DB files from original archive)
        into convertion.temporary_directory/<job_id>/output/<original_name>_pebbledb_<unix_ts>.<ext>
Step 8: Upload output file to each enabled Storer (local, scp, s3) sequentially
Step 9: Delete convertion.temporary_directory/<job_id>/ (both download and output)
Step 10: Notify via Notifier (success or failure message) if notify.enable = true
```

Output filename pattern: `<original_name>_pebbledb_<unix_timestamp>.<extension>`
- `<original_name>`: filename of the input archive without extension.
- `<unix_timestamp>`: Unix timestamp (seconds) at job start.
- `<extension>`: determined by the Repacker (e.g. `tar.lz4`, `tar.zst`, `tar.gz`, `tar`).

### Error Handling

Non-fatal error (single Storer failure, notification failure):
- Log at WARN level to stderr.
- Attempt remaining Storers.
- Notify failure (if notify enabled).
- Mark job failed.
- Worker goroutine loops back to dequeue next job.
- Do NOT exit.

Fatal error (download failure, extraction failure, conversion failure, all Storers fail):
- Notify failure (if notify enabled).
- Log at ERROR level to stderr.
- Cleanup `convertion.temporary_directory/<job_id>/` completely.
- Mark job failed.
- Worker goroutine loops back to dequeue next job.
- Daemon remains running; fatal error does NOT exit the daemon.

Exit 1 triggers: missing required env var, config parse failure, config_version mismatch, port bind failure on startup.

---

## Telemetry (Prometheus)

Listener bound to `telemetry.host:telemetry.port`. Enabled only when `telemetry.enable = true`.

Route: `GET /metrics` (delegated to existing `internal/prom` handler; add daemon-specific collectors alongside existing ones).

New Prometheus metrics (all namespaced `pebblify_daemon_`):

| Metric                                      | Type      | Labels         | Description                                    |
| :------------------------------------------ | :-------- | :------------- | :--------------------------------------------- |
| `pebblify_daemon_jobs_in_progress`          | Gauge     | —              | 0 or 1; jobs run serially                      |
| `pebblify_daemon_job_success_total`         | Counter   | —              | Total successful jobs                          |
| `pebblify_daemon_job_failure_total`         | Counter   | phase          | Failures by pipeline phase                     |
| `pebblify_daemon_queue_depth`               | Gauge     | —              | Pending jobs waiting in FIFO queue             |
| `pebblify_daemon_bytes_downloaded_total`    | Counter   | —              | Bytes downloaded from snapshot URL             |
| `pebblify_daemon_bytes_uploaded_total`      | Counter   | target         | Bytes uploaded per store target (local/scp/s3) |
| `pebblify_daemon_conversion_duration_seconds` | Histogram | —            | Conversion phase duration per job              |

Existing `pebblify_keys_processed_total`, `pebblify_bytes_read_total`, etc. remain registered by `internal/prom.init()`. Daemon reuses them when calling `internal/migration`.

---

## Health

Listener bound to `health.host:health.port`. Enabled only when `health.enable = true`.

Reuses `internal/health.ProbeState` and `internal/health.Server` (existing types).

### Semantics

- `/healthz` (liveness): returns 200 as long as the daemon process is alive and listeners are serving. Never returns non-200 unless the process is about to crash. The ping ticker from `internal/health.PingTicker` keeps liveness alive between jobs.
- `/readyz` (readiness): returns 200 only when the daemon is idle with an empty queue (ready to accept and immediately start a new job). Returns 503 Service Unavailable while a job is running or while jobs are queued. This allows external orchestrators to back off when the daemon is busy.

State transitions:
```
startup          → SetStarted() + SetNotReady()
config loaded    → (still not ready — listeners not up)
all listeners up → SetReady()   (queue empty, no job running)
job enqueued     → SetNotReady()
job complete     → SetReady()   (if queue now empty; else stays NotReady)
job failed       → SetReady()   (if queue now empty; else stays NotReady)
shutdown gate    → SetNotReady() (permanently, until process exits)
fatal exit       → process exits; readyz/healthz stop responding
```

---

## Services Independence

All three listeners (api, telemetry, health) bind independently in separate goroutines. A bind error on any single listener is a fatal startup error (exit 1). An I/O error on an established listener connection is logged at WARN and does not terminate the daemon.

Each listener has its own `http.Server` with sensible timeouts:

| Listener  | ReadHeaderTimeout | ReadTimeout | WriteTimeout | IdleTimeout |
| :-------- | :---------------- | :---------- | :----------- | :---------- |
| api       | 10s               | 30s         | 120s         | 60s         |
| telemetry | 5s                | 5s          | 10s          | 30s         |
| health    | 5s                | 5s          | 10s          | 30s         |

---

## Sequence Diagram (ASCII)

```
Client          API server      Queue       Worker          Storer(s)   Notifier
  |                |              |             |               |           |
  |--POST /v1/job->|              |             |               |           |
  |                |--enqueue---->|             |               |           |
  |<--202 Accepted-|              |             |               |           |
  |                |              |--dequeue--->|               |           |
  |                |              |             |--download---->|           |
  |                |              |             |<--done--------|           |
  |                |              |             |--convert+verify---------->|
  |                |              |             |<--done--------------------|
  |                |              |             |--repack+store------------>|
  |                |              |             |<--done--------------------|
  |                |              |             |--cleanup                  |
  |                |              |             |--notify------------------->
  |                |              |             |<--done---------------------|
  |--GET /v1/status|              |             |               |           |
  |<--200 idle-----|              |             |               |           |
```

---

## systemd Unit Ownership

**Decision locked 2026-04-18**: the systemd unit file is owned by `@container-engineer`, not `@lead-dev`.

File paths (to be produced by `@container-engineer`):

| File                             | Description                                              |
| :------------------------------- | :------------------------------------------------------- |
| `systemd/pebblify.service`       | Canonical systemd unit template checked into the repo    |
| `systemd/pebblify.env.example`   | Example env file stub with all `PEBBLIFY_*` keys empty   |

The `install-systemd-daemon` Makefile target (owned by `@lead-dev`) copies these files to `/etc/systemd/system/pebblify.service` and `/etc/pebblify/.env` during installation. `@lead-dev` does not author the unit file content; it reads the file from the repository and copies it. This keeps systemd unit syntax exclusively in `@container-engineer`'s scope.

**CLAUDE.md scope amendment required**: `systemd/` and `**/*.service` files are NOT currently listed in `@container-engineer`'s write scope. See "CLAUDE.md Amendment Flag" in `docs/roadmap/v0.4.0.md`. CEO must extend the scope matrix before step 5 (GitHub issue creation) can include systemd work under `@container-engineer`.

---

## S3 Sub-module Constraint

`@lead-dev` must import only these aws-sdk-go-v2 sub-modules:

```
github.com/aws/aws-sdk-go-v2/config
github.com/aws/aws-sdk-go-v2/credentials
github.com/aws/aws-sdk-go-v2/service/s3
```

No other `aws/aws-sdk-go-v2` service packages (e.g. `service/sts`, `service/iam`, `feature/s3/manager`) may be added without explicit CEO approval. The goal is to keep the binary size reasonable; the full SDK adds many unused service clients. `@lead-dev` must verify that only these three sub-modules appear as direct dependencies in `go.mod` after running `go mod tidy`.

---

## Makefile Targets (authored by @lead-dev)

`@lead-dev` writes these targets in `Makefile`. This spec defines requirements only; `@lead-dev` owns the implementation.

### `install-cli`

Replaces the existing `install` target. Installs the pebblify binary for the current user. Cross-platform: works on all four build targets (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64).

Requirements:
- Build with native `CGO_ENABLED=0`.
- Install to `$(GOPATH)/bin/pebblify` if `GOPATH` is set and writable; else `/usr/local/bin/pebblify`.
- Does not require root.

### `install-systemd-daemon`

Installs daemon for system-wide use under systemd. **Linux-only**. Requires root (`sudo`). Must fail with a clear error message if run on a non-Linux platform.

Requirements:
- Binary installed to `/usr/local/bin/pebblify`.
- Creates `/etc/pebblify/` directory (mode 0750, owned root:root).
- Creates `/etc/pebblify/config.toml` from an embedded template with sane defaults (api.enable=false, health.enable=true, telemetry.enable=true). Does NOT overwrite if file already exists.
- Copies `systemd/pebblify.env.example` to `/etc/pebblify/.env`. Mode 0600, owned root:root. Does NOT overwrite if file already exists.
- Copies `systemd/pebblify.service` (authored by `@container-engineer`) to `/etc/systemd/system/pebblify.service`. Does NOT overwrite if file already exists.
- Does NOT run `systemctl enable` or `systemctl start`. The operator does this manually.
- Emits a post-install message listing next steps (fill .env, enable unit).

The unit file content requirements (for `@container-engineer`'s reference when authoring `systemd/pebblify.service`):
- `EnvironmentFile=/etc/pebblify/.env`
- `ExecStart=/usr/local/bin/pebblify daemon`
- `Restart=on-failure`
- `User=pebblify` (operator creates this user; Makefile does not create system users)

### `clean` extensions

Add cleanup of any local `pebblify-darwin-*` artifacts alongside existing `pebblify-linux-*` cleanup.

---

## Docker Compose Reference (`docker-compose.daemon.yml`)

Authored by `@container-engineer`. This spec provides a reference layout as a code block; `@container-engineer` produces the actual file.

```yaml
# Reference layout — actual file authored by @container-engineer
version: "3.9"

services:
  pebblify-daemon:
    image: ghcr.io/dockermint/pebblify:v0.4.0
    command: daemon
    env_file:
      - .env
    ports:
      - "2324:2324"   # API
      - "2325:2325"   # Health
      - "2323:2323"   # Telemetry/Prometheus
    volumes:
      - ./config.toml:/etc/pebblify/config.toml:ro
      - snapshots:/snapshots
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:2325/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s

volumes:
  snapshots:
```

`.env` file alongside the compose file holds all `PEBBLIFY_*` secrets (gitignored).

`config.toml` must configure `save.local.local_save_directory` to `/snapshots` to match the volume mount.

Note for macOS operators: Docker and Podman are the supported paths for running the daemon on macOS. There is no native daemon install for macOS.

---

## CLI Invariance

The switch statement in `cmd/pebblify/main.go` gains one new case, guarded by a Linux build tag:

```
// +build linux

case "daemon":
    daemonCmd(os.Args[2:])
```

On non-Linux builds, the `"daemon"` case is replaced by a stub that prints `pebblify daemon is not supported on this platform; use Docker or Podman` and exits 1.

`daemonCmd` validates that no positional arguments or conversion flags are passed (exits 1 with usage if any are present), reads the config path from `PEBBLIFY_CONFIG_PATH` or defaults to `./config.toml`, loads config, and calls `daemon.Run(cfg)`.

Existing cases (`level-to-pebble`, `recover`, `verify`, `completion`) are not modified.

---

## Dependency Recommendations

| Need                       | Recommended package                                      | Decision owner | Status         |
| :------------------------- | :------------------------------------------------------- | :------------- | :------------- |
| TOML parsing               | `github.com/BurntSushi/toml` v1.x                        | `@lead-dev`    | Pending eval   |
| S3 uploads                 | `github.com/aws/aws-sdk-go-v2/config` + `credentials` + `service/s3` | `@lead-dev` | **CEO confirmed** — only these 3 sub-modules |
| Telegram notify            | `net/http` + `encoding/json` (stdlib only)               | `@lead-dev`    | **CEO locked** — no third-party lib; `go-telegram-bot-api/v5` rejected (abandoned, last release 2021-12-13) |
| SCP / SSH transport        | `golang.org/x/crypto/ssh`                                | `@lead-dev`    | Pending eval   |

The `aws-sdk-go-v2` fallback (hand-rolled SigV4) is removed from scope; CEO confirmed the library. Telegram implementation uses `net/http` + `encoding/json` (stdlib only); `go-telegram-bot-api/v5` is rejected as abandoned. Zero additional deps for notification.

---

## Hand-off

| Agent               | Scope files                                                                             | Action                                                    |
| :------------------ | :-------------------------------------------------------------------------------------- | :-------------------------------------------------------- |
| `@lead-dev`         | `go.mod`, `go.sum`, `Makefile`                                                          | Add deps (3 aws sub-modules + x/crypto only; no Telegram lib); add install-cli, install-systemd-daemon (Linux-only) |
| `@go-developer`     | `internal/daemon/**/*.go`, `cmd/pebblify/main.go`                                       | Implement all packages and CLI wiring; add linux build tag to daemon.go |
| `@container-engineer` | `docker-compose.daemon.yml`, `systemd/pebblify.service`, `systemd/pebblify.env.example` | Produce compose file + systemd unit + env stub (pending CLAUDE.md scope amendment) |
| `@qa`               | `internal/daemon/**/*_test.go`                                                          | Unit + integration tests; mutation testing                |
| `@reviewer`         | read-only                                                                               | APPROVE or BLOCK                                          |
| `@sysadmin`         | git operations                                                                          | Branch, commit, PR                                        |
| `@devops`           | no changes in this feature branch                                                       | —                                                         |
| `@technical-writer` | invoked post-merge; owns README + docs/                                                 | Document daemon mode; split install docs by platform      |
