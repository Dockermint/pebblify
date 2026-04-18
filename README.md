# Pebblify

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
![Benchmark](https://img.shields.io/badge/benchmark-217M_keys_in_4m11s-brightgreen)
![Throughput](https://img.shields.io/badge/throughput-865k_keys%2Fs-blue)

**Pebblify** is a high-performance migration tool that converts LevelDB databases to PebbleDB format, specifically designed for Cosmos SDK and CometBFT (formerly Tendermint) blockchain nodes.

PebbleDB offers significant performance improvements over LevelDB, including better write throughput, more efficient compaction, and reduced storage overhead. Pebblify makes it easy to migrate your existing node data without manual intervention.

📖 [Documentation](https://docs.dockermint.io/pebblify/) · 🌐 [Website](https://dockermint.io/tools)

> [!WARNING]
> This tool is still in the early stages of development and may contain bugs or be unstable. If you notice any unusual behavior, please open an issue.

## Features

- **Fast parallel conversion** — Process multiple databases concurrently with configurable worker count
- **Crash recovery** — Resume interrupted migrations from the last checkpoint
- **Adaptive batching** — Automatically adjusts batch sizes based on memory constraints
- **Real-time progress** — Live progress bar with throughput metrics and ETA
- **Data verification** — Verify converted data integrity with configurable sampling
- **Disk space checks** — Pre-flight validation to ensure sufficient storage
- **Docker support** — Multi-architecture container images (amd64/arm64)
- **Health probes** — HTTP liveness, readiness, and startup endpoints for orchestrators
- **Prometheus metrics** — Opt-in metrics exporter for conversion monitoring
- **Shell completion** — Bash and zsh autocompletion via `pebblify completion`

## Requirements

- **Go 1.25+** (for building from source)
- **Sufficient disk space** — Approximately 1.5x the source data size during conversion
- **Source database** — Valid LevelDB directory structure (Cosmos/CometBFT `data/` format)

## Installation

### CLI (all platforms)

Cross-platform. Works on `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.

```bash
git clone https://github.com/Dockermint/pebblify.git
cd pebblify
make install-cli   # build and install pebblify binary to PATH
```

Or download a pre-built binary from [releases](https://github.com/Dockermint/pebblify/releases).

### Systemd daemon — **Linux only**

Installs the daemon as a system service. Requires root. Not supported on macOS.

```bash
sudo make install-systemd-daemon
```

After installation, fill in `/etc/pebblify/.env`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now pebblify
```

See [daemon quickstart](docs/markdown/daemon-quickstart.md) for the full setup guide.

### Podman Quadlet (rootless)

Deploys the daemon as a rootless Podman container managed by the systemd user session. Linux-native; macOS users need Podman Desktop.

```bash
make install-podman
```

After installation:

```bash
systemctl --user daemon-reload
systemctl --user start pebblify
```

### Docker

```bash
make build-docker
```

## Daemon mode

`pebblify daemon` is a long-running HTTP service that accepts snapshot archive URLs, converts them from LevelDB to PebbleDB format, repacks the output, and saves it to one or more storage targets (local directory, SCP, S3). Jobs are submitted via a REST API and processed serially.

**Platform:** The `daemon` subcommand is Linux-only at runtime. On macOS, use Docker Compose or Podman.

```bash
# Set the API token
export PEBBLIFY_BASIC_AUTH_TOKEN="your-secret-token-here"

# Start the daemon (Linux only)
pebblify daemon

# Submit a job
curl -s -X POST http://127.0.0.1:2324/v1/jobs \
  -u "ignored:${PEBBLIFY_BASIC_AUTH_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://snapshots.example.com/gaia-snapshot.tar.lz4"}'
```

> **macOS users:** Run the daemon via Docker Compose (`docker compose -f docker-compose.daemon.yml up -d`) or Podman Desktop. See [daemon quickstart](docs/markdown/daemon-quickstart.md).

Full guide: [docs/markdown/daemon-quickstart.md](docs/markdown/daemon-quickstart.md)

## Quick Start

### Convert

```bash
pebblify level-to-pebble ~/.gaia/data ./gaia-pebble
```

### Recover an Interrupted Conversion

```bash
pebblify recover --tmp-dir /var/tmp
```

### Verify

```bash
pebblify verify --sample 10 ~/.gaia/data ./gaia-pebble/data
```

### Docker

```bash
docker pull ghcr.io/dockermint/pebblify:latest

docker run --rm \
  -v /path/to/source:/data/source:ro \
  -v /path/to/output:/data/output \
  -v /path/to/tmp:/tmp \
  ghcr.io/dockermint/pebblify:latest \
  level-to-pebble --health --metrics /data/source /data/output
```

> For full command reference and all available flags, see the [documentation](https://docs.dockermint.io/pebblify/).

## Artifact attestation

Starting with v0.4.0, every release binary and Docker image is published with SLSA provenance and SBOM attestations. Verify before running:

```bash
# Verify a release binary
gh attestation verify pebblify-linux-amd64 \
  --repo Dockermint/Pebblify

# Verify the Docker image
gh attestation verify \
  oci://ghcr.io/dockermint/pebblify:v0.4.0 \
  --repo Dockermint/Pebblify
```

Available attestation targets: `pebblify-linux-amd64`, `pebblify-linux-arm64`, `pebblify-darwin-amd64`, `pebblify-darwin-arm64`, and the multi-arch Docker image.

Full guide: [docs/markdown/release-automation.md](docs/markdown/release-automation.md)

## Benchmark

Real-world conversion on a production Cosmos node dataset:

| Metric | Value |
|---|---|
| Total keys | 217,895,735 |
| Duration | 4m 11s |
| Throughput | ~865k keys/s · ~160 MB/s |
| Data processed | 39.54 GiB read / 39.54 GiB written |
| Size reduction | -16.3% (LevelDB 23.32 GiB → PebbleDB 19.51 GiB) |
| Data loss | None — 1:1 write/read parity |

> [!NOTE]
> Benchmark performed on AMD Ryzen 9 8940HX, 32 GiB DDR5, NVMe (Btrfs). Temp folder on disk, not in RAM.

## Performance Tips

- **Use SSDs** — NVMe storage significantly improves conversion speed
- **Increase workers** — For systems with many CPU cores, increase `-w` for faster parallel processing
- **Adjust batch memory** — Increase `--batch-memory` if you have RAM to spare
- **Use local temp** — If `/tmp` is a tmpfs (RAM-based), use `--tmp-dir` to point to disk storage for large datasets

## Contributing

Contributions are welcome! Please feel free to submit issues and pull requests.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENCE) file for details.

## Acknowledgments

- [CockroachDB Pebble](https://github.com/cockroachdb/pebble) — The high-performance storage engine
- [syndtr/goleveldb](https://github.com/syndtr/goleveldb) — LevelDB implementation in Go
