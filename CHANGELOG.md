# Changelog

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
