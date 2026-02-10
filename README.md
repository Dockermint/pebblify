# Pebblify

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
![Benchmark](https://img.shields.io/badge/benchmark-216M_keys_in_4m9s-brightgreen)
![Throughput](https://img.shields.io/badge/throughput-866k_keys%2Fs-blue)

**Pebblify** is a high-performance migration tool that converts LevelDB databases to PebbleDB format, specifically designed for Cosmos SDK and CometBFT (formerly Tendermint) blockchain nodes.

PebbleDB offers significant performance improvements over LevelDB, including better write throughput, more efficient compaction, and reduced storage overhead. Pebblify makes it easy to migrate your existing node data without manual intervention.

## Features

- **Fast parallel conversion** — Process multiple databases concurrently with configurable worker count
- **Crash recovery** — Resume interrupted migrations from the last checkpoint
- **Adaptive batching** — Automatically adjusts batch sizes based on memory constraints
- **Real-time progress** — Live progress bar with throughput metrics and ETA
- **Data verification** — Verify converted data integrity with configurable sampling
- **Disk space checks** — Pre-flight validation to ensure sufficient storage
- **Docker support** — Multi-architecture container images (amd64/arm64)

## Installation

### From Source

```bash
git clone https://github.com/Dockermint/pebblify.git
cd pebblify
make build
```

### Install to PATH

```bash
make install
```

### Using Docker


```bash
make build-docker
```

## Usage

### Convert LevelDB to PebbleDB

```bash
pebblify level-to-pebble [options] <source-dir> <output-dir>
```

**Example:**

```bash
# Convert a Cosmos node's data directory
pebblify level-to-pebble ~/.gaia/data ./gaia-pebble

# Use a custom temp directory (useful if /tmp is too small)
pebblify level-to-pebble --tmp-dir /var/tmp ~/.gaia/data ./gaia-pebble

# Run with 4 workers and verbose output
pebblify level-to-pebble -w 4 -v ~/.gaia/data ./gaia-pebble
```

**Options:**

| Flag | Description |
|------|-------------|
| `-f, --force` | Overwrite existing temporary state |
| `-w, --workers N` | Max concurrent DB conversions (0 = auto, based on CPU) |
| `-v, --verbose` | Enable verbose output |
| `--batch-memory M` | Target memory per batch in MB (default: 64) |
| `--tmp-dir DIR` | Directory where `.pebblify-tmp/` will be created |

### Resume an Interrupted Conversion

If a conversion is interrupted (crash, power loss, etc.), you can resume from the last checkpoint:

```bash
pebblify recover [options]
```

**Example:**

```bash
# Resume with default temp directory
pebblify recover

# Resume with custom temp directory (must match the original conversion)
pebblify recover --tmp-dir /var/tmp
```

**Options:**

| Flag | Description |
|------|-------------|
| `-w, --workers N` | Max concurrent DB conversions (0 = auto) |
| `-v, --verbose` | Enable verbose output |
| `--tmp-dir DIR` | Directory containing `.pebblify-tmp/` |

### Verify Converted Data

After conversion, verify that all data was migrated correctly:

```bash
pebblify verify [options] <source-dir> <converted-dir>
```

**Example:**

```bash
# Full verification (all keys)
pebblify verify ~/.gaia/data ./gaia-pebble/data

# Sample 10% of keys for faster verification
pebblify verify --sample 10 ~/.gaia/data ./gaia-pebble/data

# Stop at first error
pebblify verify --stop-on-error ~/.gaia/data ./gaia-pebble/data
```

**Options:**

| Flag | Description |
|------|-------------|
| `-s, --sample P` | Percentage of keys to verify (default: 100 = all) |
| `--stop-on-error` | Stop at first mismatch |
| `-v, --verbose` | Show each key being verified |

### Version Information

```bash
pebblify --version
```

## Docker Usage

Run Pebblify in a container with your data directories mounted:

```bash
docker run --rm \
  -v /path/to/source:/data/source:ro \
  -v /path/to/output:/data/output \
  -v /path/to/tmp:/tmp \
  dockermint/pebblify:latest \
  level-to-pebble /data/source /data/output
```

For recovery:

```bash
docker run --rm \
  -v /path/to/source:/data/source:ro \
  -v /path/to/output:/data/output \
  -v /path/to/tmp:/tmp \
  dockermint/pebblify:latest \
  recover
```

## How It Works

1. **Scanning** — Pebblify scans the source directory to discover all LevelDB databases and estimates key counts
2. **Conversion** — Each database is converted in parallel (up to the worker limit), with adaptive batching to optimize memory usage
3. **Checkpointing** — Progress is saved periodically, enabling crash recovery
4. **Finalization** — Once all databases are converted, the output is moved to the final destination
5. **Cleanup** — Temporary files are removed automatically

### State Management

Pebblify maintains a state file (`.pebblify-tmp/state.json`) that tracks:

- Source and destination paths
- Status of each database (pending, in_progress, done, failed)
- Last checkpoint key for each database
- Migration statistics and metrics

This enables seamless recovery from any interruption.

## Requirements

- **Go 1.25+** (for building from source)
- **Sufficient disk space** — Approximately 1.5x the source data size is recommended during conversion
- **Source database** — Must be a valid LevelDB directory structure (Cosmos/CometBFT `data/` format)

## Build Targets

```bash
make build              # Build for current platform
make build-linux-amd64  # Build for Linux AMD64
make build-linux-arm64  # Build for Linux ARM64
make build-docker       # Build Docker image for current platform
make install            # Build and install to PATH
make clean              # Remove build artifacts
make info               # Show build information
```
## Benchmark

### Real-world Conversion Benchmark

The following benchmark was performed on a production Cosmos node dataset to measure end-to-end LevelDB → PebbleDB conversion performance:

```bash
============================================================
CONVERSION METRICS SUMMARY
============================================================

Global Statistics:
  Total duration:      4m9s
  Total keys:          216404586
  Total data read:     39.00 GiB
  Total data written:  39.00 GiB
  Avg throughput:      866154 keys/sec, 159.83 MB/sec
  Write/Read ratio:    100.0%

Per-Database Statistics:

  blockstore.db:
    Keys:        57
    Duration:    0s
    Throughput:  1835 keys/sec, 20.68 MB/sec
    Avg sizes:   key=1182 B, value=10638 B

  tx_index.db:
    Keys:        14655
    Duration:    0s
    Throughput:  673383 keys/sec, 51.10 MB/sec
    Avg sizes:   key=8 B, value=72 B

  application.db:
    Keys:        216382918
    Duration:    4m8s
    Throughput:  871172 keys/sec, 159.83 MB/sec
    Avg sizes:   key=19 B, value=173 B

  state.db:
    Keys:        6956
    Duration:    0s
    Throughput:  12969 keys/sec, 432.40 MB/sec
    Avg sizes:   key=3496 B, value=31465 B
============================================================

Conversion completed successfully.
New Pebble-backed data directory: pebbledb/data

This run duration:   4m9s
Total elapsed time:  5m7s (since first start)

Size summary:
  Source (LevelDB) data:  23.04 GiB (24743642329 bytes)
  Target (PebbleDB) data: 23.91 GiB (25671213463 bytes)
  Size ratio:             103.7 %
```

### Key Takeaways

- ~216M keys migrated in 4 minutes
- Sustained throughput of ~160 MB/s
- Conversion speed dominated by application.db
- PebbleDB size overhead: +3.7% (We are exploring ways to reduce the space used)
- Zero data loss, 1:1 write/read parity

> [!NOTE]
> The benchmark was performed on a machine with an AMD Ryzen 9 8940HX CPU, 32 GiB of DDR5 RAM, and an NVMe disk using the Btrfs file system. The temporary folder was located on the NVMe disk, not in RAM.

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
