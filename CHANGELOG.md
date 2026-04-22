# Changelog

## [v0.4.1](https://github.com/Dockermint/Pebblify/compare/v0.4.0...v0.4.1)

### Bug Fixes

- fix(docs): correct install-podman image tag from v0.5.0 to v0.4.0; rename `LICENCE` to `LICENSE` for Apache-2.0 / GitHub license-detection compliance ([#65](https://github.com/Dockermint/Pebblify/pull/65))
- fix(ci): surface semver/`latest` tag before Sigstore attestation digest in the GHCR package-page tag list via post-attestation `imagetools create` re-tag, guarded against pre-releases ([#67](https://github.com/Dockermint/Pebblify/pull/67))
- fix(container): remove hardcoded `org.opencontainers.image.source` LABEL from Dockerfile (was capital-P URL breaking GHCR case-sensitive repo-link heuristic); metadata-action is now sole source of truth ([#67](https://github.com/Dockermint/Pebblify/pull/67))

### Documentation

- docs(go-doc): add godoc comments on ~76 exported identifiers across `cmd/pebblify` and 13 `internal/` packages; add package-level synopses via `doc.go` convention ([#63](https://github.com/Dockermint/Pebblify/pull/63))
- docs(arch): land v0.4.1 architecture specs (`godoc-coverage`, `docs-freshness-v0.4.1`, `ghcr-package-display-fix`) and ROADMAP v0.4.1 section ([#61](https://github.com/Dockermint/Pebblify/pull/61))

### CI

- ci(release): add `latest` tag via `docker/metadata-action` and re-push post-attestation so semver appears first in GHCR UI; both tag addition and re-push guarded by `!contains(github.ref_name, '-')` to skip pre-releases ([#67](https://github.com/Dockermint/Pebblify/pull/67))

### Chore / Governance

- chore(github): bootstrap `.github/ISSUE_TEMPLATE/` with 8 templates + `config.yml` (`blank_issues_enabled: false`); unblocks strict CLAUDE.md step-5 workflow enforcement ([#61](https://github.com/Dockermint/Pebblify/pull/61))
- chore(governance): flush pending CLAUDE.md changes — workflow step renumbering, step 11 pre-push verify, step 14 PR title convention, step 17 release verification gate, step 16 CodeRabbit pre-merge panel enforcement ([#61](https://github.com/Dockermint/Pebblify/pull/61))
- chore(lint): bootstrap `.golangci.yml` (v2 schema) with `revive.exported` at severity=error; stuttering check disabled for Feat 1 scope; gofumpt/gocritic/misspell deferred to follow-up chore PR ([#63](https://github.com/Dockermint/Pebblify/pull/63))

## [v0.4.0](https://github.com/Dockermint/Pebblify/compare/v0.3.2...v0.4.0)

### Features

- feat(daemon): add `pebblify daemon` Linux-only subcommand with HTTP job queue API, FIFO queue with URL deduplication, LevelDB→PebbleDB pipeline, Prometheus metrics, and health probes ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(daemon): add TOML config schema (config_version = 0) with env-var secret overlay for API, notify, telemetry, health, conversion, and save targets ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(daemon): add store backends — local directory, SCP, and S3 via aws-sdk-go-v2 ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(daemon): add Telegram notifier using stdlib net/http only; no third-party library ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(daemon): add repack support for lz4, zstd, gzip, and none compression formats ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(install): add `install-cli` (cross-platform) and `install-systemd-daemon` (Linux-only) Makefile targets; add systemd unit and placeholder env template ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- feat(install): add `install-podman` Makefile target and Podman Quadlet `.container` file for rootless daemon deployment ([#44](https://github.com/Dockermint/Pebblify/pull/44))

### Bug Fixes

- fix(container): remove `# hadolint ignore=` directives; replace fuzzy apk version constraints with exact pinned versions ([#43](https://github.com/Dockermint/Pebblify/pull/43))

### Documentation

- docs: v0.4.0 release documentation refresh — README installation split, daemon mode section, artifact attestation examples, and platform-split install guides ([#45](https://github.com/Dockermint/Pebblify/pull/45))
- docs: land v0.4.0 roadmap and per-feature architecture specs ([#37](https://github.com/Dockermint/Pebblify/pull/37))

### CI

- ci: add darwin/amd64 and darwin/arm64 release binary targets to build matrix ([#38](https://github.com/Dockermint/Pebblify/pull/38))
- ci: add SLSA provenance and SBOM attestations for release binaries and Docker images via `actions/attest-build-provenance` and `actions/attest-sbom` ([#38](https://github.com/Dockermint/Pebblify/pull/38))

### Security

- security(daemon): use HMAC-SHA-256 with constant-time comparison for API token validation to satisfy CodeQL timing-attack checks ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- security(daemon): reject symlink tar and zip entries during archive extraction to prevent path-traversal; covered by CodeQL analysis ([#39](https://github.com/Dockermint/Pebblify/pull/39))
- security(daemon): enforce SSH known_hosts validation for SCP store; no host-key bypass permitted ([#39](https://github.com/Dockermint/Pebblify/pull/39))

### Chore / Governance

- chore: amend CLAUDE.md scope matrix to assign systemd unit files to `@container-engineer`; add env-template placeholder rule; land v0.4.0 governance docs ([#37](https://github.com/Dockermint/Pebblify/pull/37))
- chore: apply `@it-consultant` retro tightenings — extend linter-suppression ban to all languages, add pre-push verify step 10b, per-agent scope tightenings ([#41](https://github.com/Dockermint/Pebblify/pull/41))

## [v0.3.2](https://github.com/Dockermint/Pebblify/compare/v0.3.1...v0.3.2)

### Bug Fixes

- fix(prom): add missing Help descriptions to all Prometheus metrics ([#24](https://github.com/Dockermint/Pebblify/pull/24))

### Dependencies

- deps(deps): bump github.com/prometheus/client_golang from 1.21.1 to 1.23.2 ([#22](https://github.com/Dockermint/Pebblify/pull/22))

## [v0.3.1](https://github.com/Dockermint/Pebblify/compare/v0.3.0...v0.3.1)

### Performance

- perf(migration): optimize PebbleDB write options for smaller output ([#16](https://github.com/Dockermint/Pebblify/pull/16))

### CI

- ci: run CI on develop branch ([#15](https://github.com/Dockermint/Pebblify/pull/15))

### Documentation

- docs: update documentation link to remove versioned path ([#14](https://github.com/Dockermint/Pebblify/pull/14))
- docs(readme): update benchmark with optimized results ([#16](https://github.com/Dockermint/Pebblify/pull/16))

## [v0.3.0](https://github.com/Dockermint/Pebblify/compare/v0.2.0...v0.3.0)

### Features

- feat(health): add liveness, readiness, and startup HTTP probe server ([#7](https://github.com/Dockermint/Pebblify/pull/7))
- feat(cli): integrate health probes into level-to-pebble and recover commands ([#7](https://github.com/Dockermint/Pebblify/pull/7))
- feat(docker): add health check, expose probe port, and add docker-compose for local testing ([#7](https://github.com/Dockermint/Pebblify/pull/7))
- feat(prom): add Prometheus metrics exporter for conversion monitoring ([#9](https://github.com/Dockermint/Pebblify/pull/9))
- feat(cli): add --metrics and --metrics-port flags with Docker integration ([#9](https://github.com/Dockermint/Pebblify/pull/9))
- feat(completion): add bash and zsh completion generation with install support ([#7](https://github.com/Dockermint/Pebblify/pull/7))
- feat(cli): add completion command for shell autocompletion generation and installation ([#7](https://github.com/Dockermint/Pebblify/pull/7))

### Bug Fixes

- fix(health): use periodic ping ticker to keep liveness probe alive during long migrations ([#7](https://github.com/Dockermint/Pebblify/pull/7))
- fix(health): handle fmt.Fprintln return values to satisfy errcheck linter ([#9](https://github.com/Dockermint/Pebblify/pull/9))

### CI

- ci(docker): add missing OCI image labels to CI and release workflows ([#11](https://github.com/Dockermint/Pebblify/pull/11))

## [v0.2.0](https://github.com/Dockermint/Pebblify/compare/v0.1.0...v0.2.0)

### Bug Fixes

- fix(docker): correct repository URL case in OCI labels ([#1](https://github.com/Dockermint/Pebblify/pull/1))
- fix: relax OUT validation to only check OUT/data and cleanup tmp on non-conversion errors ([#4](https://github.com/Dockermint/Pebblify/pull/4))

### Refactoring

- refactor: split monolithic main.go into modular internal packages ([#3](https://github.com/Dockermint/Pebblify/pull/3))
- refactor: replace root main.go with cmd/pebblify entry point ([#3](https://github.com/Dockermint/Pebblify/pull/3))

### Build

- build: update Dockerfile and Makefile for cmd/pebblify layout ([#3](https://github.com/Dockermint/Pebblify/pull/3))
- build: detect platform via uname when Go is not installed ([#3](https://github.com/Dockermint/Pebblify/pull/3))

### Documentation

- docs(README): add benchmark ([#2](https://github.com/Dockermint/Pebblify/pull/2))
- docs(README): add warning ([#2](https://github.com/Dockermint/Pebblify/pull/2))
- docs: streamline README for clarity and remove redundancy ([#5](https://github.com/Dockermint/Pebblify/pull/5))
