# Changelog

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
