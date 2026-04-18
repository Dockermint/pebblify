# Daemon API Reference

The Pebblify daemon exposes an HTTP/1.1 control plane on `api.host:api.port` (default `127.0.0.1:2324`). The API listener is always active in daemon mode; there is no enable flag.

Source: `internal/daemon/api/`

## Base URL

```
http://<api.host>:<api.port>
```

Default: `http://127.0.0.1:2324`

## Authentication

Two modes are configured via `api.authentification_mode` in `config.toml`.

### basic_auth (recommended)

HTTP Basic Auth or Bearer token. The username field is ignored; only the password is compared. The token is read from `PEBBLIFY_BASIC_AUTH_TOKEN` at startup.

Comparison is HMAC-SHA-256 constant-time (per `internal/daemon/api/middleware.go:checkAuth`) to prevent timing attacks and satisfy length-hiding requirements.

Both formats are accepted:

```bash
# HTTP Basic Auth (username ignored, password = token)
curl -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}" http://127.0.0.1:2324/v1/status

# Bearer token
curl -H "Authorization: Bearer ${PEBBLIFY_BASIC_AUTH_TOKEN}" \
  http://127.0.0.1:2324/v1/status
```

Invalid token: `401 Unauthorized` with `WWW-Authenticate: Basic realm="pebblify"` header.

### unsecure

No authentication. All requests are accepted without credentials. A WARN log is emitted at daemon startup:

```
"api listener running without authentication; set api.authentification_mode=basic_auth for production"
```

**Do not use `unsecure` in production.** Requests to the API in unsecure mode accept no credentials:

```bash
curl http://127.0.0.1:2324/v1/status
```

## Endpoints

### POST /v1/jobs

Submit a snapshot archive URL for conversion. The job is enqueued immediately; the response returns before any conversion begins.

The endpoint is also registered as `/v1/job` (singular) for backwards compatibility (per `internal/daemon/api/server.go:New`).

**Request body** (JSON):

```json
{
  "url": "https://snapshots.example.com/gaia-20260418.tar.lz4"
}
```

| Field | Type   | Required | Description                                           |
| :---- | :----- | :------- | :---------------------------------------------------- |
| `url` | string | Yes      | HTTP or HTTPS URL of the snapshot archive to convert. |

Supported archive extensions inferred from the URL: `.tar`, `.tar.gz`, `.tar.lz4`, `.tar.zst`, `.zip`. Unrecognized extensions are rejected with `400`.

Request body size is capped at 64 KiB (per `internal/daemon/api/handler.go:maxRequestBodyBytes`).

**Responses:**

#### 201 Created

The job was accepted and queued.

```json
{
  "job_id": "3f8a2c1d4e5b6f7a8c9d0e1f2a3b4c5d",
  "queue_depth": 1
}
```

| Field         | Type   | Description                                              |
| :------------ | :----- | :------------------------------------------------------- |
| `job_id`      | string | Opaque 32-character hex identifier assigned by the daemon. |
| `queue_depth` | int    | Queue depth observed immediately after the enqueue.      |

#### 400 Bad Request

The request body is malformed, the `url` field is absent or empty, or the URL fails validation (invalid scheme, unrecognized extension).

```json
{
  "error": "url is required"
}
```

#### 401 Unauthorized

`basic_auth` mode is active and the request did not include a valid token.

```json
{
  "error": "unauthorized"
}
```

#### 409 Conflict

The canonicalized URL is already running or queued (dedup rejection). The `job_id` field is present when the duplicate is the currently running job.

```json
{
  "error": "duplicate",
  "job_id": "3f8a2c1d4e5b6f7a8c9d0e1f2a3b4c5d"
}
```

URL dedup uses canonicalization: scheme and host are lowercased, default ports are stripped, path is cleaned, query parameters are sorted, and the fragment is discarded. Two URLs that canonicalize to the same string are considered duplicates (per `internal/daemon/queue/queue.go:Canonicalize`).

#### 413 Request Entity Too Large

The request body exceeds 64 KiB.

```json
{
  "error": "request body too large"
}
```

#### 503 Service Unavailable

Either the queue buffer is full (64 jobs by default, configurable via `queue.buffer_size`) or the daemon is shutting down (SIGTERM received).

```json
{
  "error": "queue full"
}
```

```json
{
  "error": "daemon shutting down"
}
```

#### 500 Internal Server Error

Unexpected internal error (e.g. job ID generation failure).

```json
{
  "error": "internal server error"
}
```

---

### GET /v1/jobs

Return the current queue depth and the job currently in progress (if any).

The endpoint is also registered as `/v1/job` (singular) for backwards compatibility.

**Response:** `200 OK`

```json
{
  "queue_depth": 2,
  "current": {
    "job_id": "3f8a2c1d4e5b6f7a8c9d0e1f2a3b4c5d",
    "url": "https://snapshots.example.com/gaia-20260418.tar.lz4",
    "submitted_at": "2026-04-18T10:00:00Z"
  }
}
```

When idle:

```json
{
  "queue_depth": 0,
  "current": null
}
```

| Field            | Type        | Description                                                    |
| :--------------- | :---------- | :------------------------------------------------------------- |
| `queue_depth`    | int         | Number of jobs waiting in the FIFO buffer (not counting the running job). |
| `current`        | object/null | The job currently being processed, or `null` when idle.        |
| `current.job_id` | string      | Opaque job identifier.                                         |
| `current.url`    | string      | The snapshot URL being processed.                              |
| `current.submitted_at` | string | RFC3339 timestamp when the job was enqueued.             |

**curl examples:**

```bash
# basic_auth
curl -s http://127.0.0.1:2324/v1/jobs \
  -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}"

# unsecure mode
curl -s http://127.0.0.1:2324/v1/jobs
```

---

### GET /v1/status

Return daemon state including version and uptime.

**Response:** `200 OK`

```json
{
  "version": "0.4.0",
  "queue_depth": 0,
  "current": null,
  "uptime_seconds": 3600
}
```

| Field            | Type        | Description                                                  |
| :--------------- | :---------- | :----------------------------------------------------------- |
| `version`        | string      | Daemon build version string.                                 |
| `queue_depth`    | int         | Number of jobs waiting in the FIFO buffer.                   |
| `current`        | object/null | Currently running job, or `null` when idle.                  |
| `uptime_seconds` | int         | Seconds since the API server started.                        |

**curl examples:**

```bash
# basic_auth
curl -s http://127.0.0.1:2324/v1/status \
  -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}"

# Bearer token
curl -s http://127.0.0.1:2324/v1/status \
  -H "Authorization: Bearer ${PEBBLIFY_BASIC_AUTH_TOKEN}"

# unsecure mode
curl -s http://127.0.0.1:2324/v1/status
```

---

## Health endpoints

The health listener runs on `health.host:health.port` (default `127.0.0.1:2325`) and is independent from the API listener. It requires no authentication.

### GET /healthz

Liveness probe. Returns `200 OK` as long as the daemon process is alive and serving. Never returns a non-200 response during normal operation.

```bash
curl -s http://127.0.0.1:2325/healthz
```

### GET /readyz

Readiness probe. Returns `200 OK` only when the daemon is idle (queue empty, no job running). Returns `503 Service Unavailable` while a job is in progress or jobs are queued.

```bash
curl -s http://127.0.0.1:2325/readyz
```

Use `/readyz` to determine whether the daemon can immediately start a new job before submitting one. External orchestrators (Kubernetes, Nomad) use this to back off when the daemon is busy.

---

## Telemetry endpoints

The Prometheus listener runs on `telemetry.host:telemetry.port` (default `127.0.0.1:2323`) when `telemetry.enable = true`.

### GET /metrics

Returns Prometheus-formatted metrics. No authentication.

Daemon-specific metrics (all namespaced `pebblify_daemon_`):

| Metric                                      | Type      | Labels   | Description                                |
| :------------------------------------------ | :-------- | :------- | :----------------------------------------- |
| `pebblify_daemon_jobs_in_progress`          | Gauge     | —        | 0 or 1 (jobs run serially)                 |
| `pebblify_daemon_job_success_total`         | Counter   | —        | Total successful jobs                      |
| `pebblify_daemon_job_failure_total`         | Counter   | `phase`  | Failures by pipeline phase                 |
| `pebblify_daemon_queue_depth`               | Gauge     | —        | Pending jobs in the FIFO buffer            |
| `pebblify_daemon_bytes_downloaded_total`    | Counter   | —        | Total bytes downloaded                     |
| `pebblify_daemon_bytes_uploaded_total`      | Counter   | `target` | Bytes uploaded per store target            |
| `pebblify_daemon_conversion_duration_seconds` | Histogram | —      | Conversion phase duration per job          |

```bash
curl -s http://127.0.0.1:2323/metrics
```

## Server timeouts

| Listener  | ReadHeaderTimeout | ReadTimeout | WriteTimeout | IdleTimeout |
| :-------- | :---------------- | :---------- | :----------- | :---------- |
| api       | 10s               | 30s         | 120s         | 60s         |
| telemetry | 5s                | 5s          | 10s          | 30s         |
| health    | 5s                | 5s          | 10s          | 30s         |

Source: `internal/daemon/api/server.go` and `docs/specs/daemon-mode.md`
