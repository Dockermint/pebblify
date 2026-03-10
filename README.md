# Pebblify

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
![Benchmark](https://img.shields.io/badge/benchmark-216M_keys_in_4m9s-brightgreen)
![Throughput](https://img.shields.io/badge/throughput-866k_keys%2Fs-blue)

**Pebblify** is a high-performance migration tool that converts LevelDB databases to PebbleDB format, specifically designed for Cosmos SDK and CometBFT (formerly Tendermint) blockchain nodes.

PebbleDB offers significant performance improvements over LevelDB, including better write throughput, more efficient compaction, and reduced storage overhead. Pebblify makes it easy to migrate your existing node data without manual intervention.

📖 [Documentation](https://docs.dockermint.io/pebblify/v0.3.0/overview) · 🌐 [Website](https://dockermint.io/tools)

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

### From Source

```bash
git clone https://github.com/Dockermint/pebblify.git
cd pebblify
make build     # build for current platform
make install   # build and install to PATH
```

### Docker

```bash
make build-docker
```

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

> For full command reference and all available flags, see the [documentation](https://docs.dockermint.io/pebblify/v0.3.0/overview).

## Benchmark

Real-world conversion on a production Cosmos node dataset:

| Metric | Value |
|---|---|
| Total keys | 216,404,586 |
| Duration | 4m 9s |
| Throughput | ~866k keys/s · ~160 MB/s |
| Data processed | 39 GiB read / 39 GiB written |
| Size overhead | +3.7% (LevelDB 23.04 GiB → PebbleDB 23.91 GiB) |
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
