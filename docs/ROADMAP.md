# Pebblify Roadmap

Owner: `@software-architect`
Last updated: 2026-04-18

---

## Release History

| Version | Date       | Theme                                              |
| :------ | :--------- | :------------------------------------------------- |
| v0.1.0  | —          | Initial release: LevelDB→PebbleDB conversion CLI  |
| v0.2.0  | —          | Modular refactor, cmd/pebblify, Docker labels      |
| v0.3.0  | —          | Health probes, Prometheus metrics, shell completion|
| v0.3.1  | —          | PebbleDB write-option tuning, CI on develop        |
| v0.3.2  | —          | Prometheus Help strings, client_golang bump        |

---

## Active Release

### v0.4.0 — Daemon Mode, CI Attestations, Podman

Target: minor bump — no breaking changes to existing CLI surface.

Full plan: [`docs/roadmap/v0.4.0.md`](roadmap/v0.4.0.md)

Platform notes:
- `pebblify daemon` is Linux-only at runtime. All four build targets (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64) compile, but the daemon subcommand is guarded by a Linux build tag. macOS users run the daemon via Docker or Podman.
- `install-cli` is cross-platform. `install-systemd-daemon` is Linux-only. There is no `install-launchd-daemon` target.
- CLAUDE.md scope amendment required before `@container-engineer` can author `systemd/` files. See `docs/roadmap/v0.4.0.md` — "CLAUDE.md Amendment Required".

Feature branches (1 PR = 1 feature = 1 issue):

| Branch                       | Spec                                              | Owner(s)                                |
| :--------------------------- | :------------------------------------------------ | :-------------------------------------- |
| `feat/ci-attestations-arm64` | `docs/specs/ci-attestations-arm64.md`             | `@devops`                               |
| `feat/daemon-mode`           | `docs/specs/daemon-mode.md`                       | `@go-developer`, `@lead-dev`, `@container-engineer` |
| `feat/podman-support`        | `docs/specs/podman-support.md`                    | `@lead-dev`, `@container-engineer`      |
| `docs/release-v0.4.0`        | `docs/specs/documentation-refresh.md`             | `@technical-writer`                     |

---

## Backlog (post-v0.4.0)

Items captured for future planning. Not committed to a release.

- Fish shell completion
- YAML config file support (alternative to TOML)
- Metrics streaming via OpenTelemetry OTLP
- Daemon: webhook notify mode (Telegram confirmed for v0.4.0; webhook deferred — Notifier interface ready for drop-in extension)
- Daemon: GCS and Azure Blob save targets
- Daemon: bounded concurrency (v0.4.0 uses serial FIFO queue; parallel processing deferred)
- Windows build target (CGO-free path investigation)
- `pebblify validate-config` subcommand for daemon config dry-run

---

## Architecture Principles

These govern all releases. Non-negotiable.

1. **Interface-first** — every package boundary is a Go interface. Replace implementations without touching callers.
2. **Zero CGO** — all build targets compile with `CGO_ENABLED=0`. No C dependencies.
3. **Additive CLI** — new subcommands never alter existing flag behavior.
4. **Config versioning** — every config schema carries a `config_version` integer field. Breaking changes increment it.
5. **Secrets in env only** — `PEBBLIFY_*` env vars for all secrets. Never in config files, never in code.
6. **Lint-clean** — `golangci-lint run` and `govulncheck ./...` pass before any commit. No `//nolint` suppressions.
