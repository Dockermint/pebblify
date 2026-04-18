# ==============================================================================
# VARIABLES
# ==============================================================================

BINARY      := pebblify
IMAGE       := dockermint/pebblify

# Git metadata
VERSION     := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null | grep -v '^HEAD$$' || git describe --tags --always --dirty 2>/dev/null || echo "dev")
REVISION    := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
CREATED     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Local platform detection (works without Go installed)
LOCAL_OS    := $(shell go env GOOS 2>/dev/null || uname -s | tr A-Z a-z)
LOCAL_ARCH  := $(shell go env GOARCH 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

# Build flags
LDFLAGS     := -s -w -X main.Version=$(VERSION) -X main.Revision=$(REVISION)
GOFLAGS     := -trimpath -ldflags="$(LDFLAGS)"

# ==============================================================================
# DEFAULT
# ==============================================================================

.PHONY: all
all: build

# ==============================================================================
# BUILD (native)
# ==============================================================================

.PHONY: build
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY) ./cmd/pebblify

.PHONY: build-linux-amd64
build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BINARY)-linux-amd64 ./cmd/pebblify

.PHONY: build-linux-arm64
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINARY)-linux-arm64 ./cmd/pebblify

# ==============================================================================
# DOCKER
# ==============================================================================

.PHONY: build-docker
build-docker:
	docker build \
		--platform linux/$(LOCAL_ARCH) \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

.PHONY: build-docker-linux-amd64
build-docker-linux-amd64:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		-t $(IMAGE):$(VERSION)-amd64 \
		.

.PHONY: build-docker-linux-arm64
build-docker-linux-arm64:
	docker build \
		--platform linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) \
		-t $(IMAGE):$(VERSION)-arm64 \
		.

# ==============================================================================
# INSTALL
# ==============================================================================

# install-cli: cross-platform (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64).
# Installs to $(GOPATH)/bin if set and writable, else /usr/local/bin. No root required.
.PHONY: install-cli
install-cli:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY) ./cmd/pebblify
	@if [ -n "$(GOPATH)" ] && [ -w "$(GOPATH)/bin" ]; then \
		mv $(BINARY) $(GOPATH)/bin/$(BINARY); \
		echo "Installed to $(GOPATH)/bin/$(BINARY)"; \
	else \
		mv $(BINARY) /usr/local/bin/$(BINARY); \
		echo "Installed to /usr/local/bin/$(BINARY)"; \
	fi

# install-systemd-daemon: Linux-only. Requires root (run via sudo).
# Copies binary, config template, env stub, and systemd unit file.
# Does NOT run systemctl enable/start — operator does this manually.
.PHONY: install-systemd-daemon
install-systemd-daemon:
	@if [ "$$(uname -s)" != "Linux" ]; then \
		echo "Error: install-systemd-daemon is Linux-only. Use Docker or Podman on macOS."; \
		exit 1; \
	fi
	CGO_ENABLED=0 go build $(GOFLAGS) -o /usr/local/bin/$(BINARY) ./cmd/pebblify
	install -d -m 0750 /etc/pebblify
	@if [ ! -f /etc/pebblify/config.toml ]; then \
		install -m 0640 config.toml /etc/pebblify/config.toml; \
		echo "Created /etc/pebblify/config.toml"; \
	else \
		echo "Skipped /etc/pebblify/config.toml (already exists)"; \
	fi
	@if [ ! -f /etc/pebblify/.env ]; then \
		install -m 0600 systemd/pebblify.env.example /etc/pebblify/.env; \
		echo "Created /etc/pebblify/.env"; \
	else \
		echo "Skipped /etc/pebblify/.env (already exists)"; \
	fi
	@if [ ! -f /etc/systemd/system/pebblify.service ]; then \
		install -m 0644 systemd/pebblify.service /etc/systemd/system/pebblify.service; \
		echo "Created /etc/systemd/system/pebblify.service"; \
	else \
		echo "Skipped /etc/systemd/system/pebblify.service (already exists)"; \
	fi
	@echo ""
	@echo "Installation complete. Next steps:"
	@echo "  1. Edit /etc/pebblify/.env — fill in all PEBBLIFY_* secrets"
	@echo "  2. Review /etc/pebblify/config.toml and adjust as needed"
	@echo "  3. Create system user: useradd --system --no-create-home pebblify"
	@echo "  4. Enable + start: systemctl enable --now pebblify"

# install: retained as alias for install-cli (backward compat).
.PHONY: install
install: install-cli

# ==============================================================================
# UTILITIES
# ==============================================================================

.PHONY: clean
clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-darwin-amd64 $(BINARY)-darwin-arm64

.PHONY: info
info:
	@echo "Binary:   $(BINARY)"
	@echo "Image:    $(IMAGE)"
	@echo "Version:  $(VERSION)"
	@echo "Revision: $(REVISION)"
	@echo "Platform: $(LOCAL_OS)/$(LOCAL_ARCH)"
