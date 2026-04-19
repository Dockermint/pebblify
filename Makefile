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
		install -m 0755 $(BINARY) $(GOPATH)/bin/$(BINARY); \
		echo "Installed to $(GOPATH)/bin/$(BINARY)"; \
	else \
		install -m 0755 $(BINARY) /usr/local/bin/$(BINARY); \
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
	@if ! getent group pebblify >/dev/null; then \
		groupadd --system pebblify; \
	fi
	@if ! getent passwd pebblify >/dev/null; then \
		useradd --system --gid pebblify --no-create-home \
			--home-dir /var/lib/pebblify --shell /usr/sbin/nologin pebblify; \
	fi
	install -d -m 0750 -o pebblify -g pebblify /etc/pebblify
	install -d -m 0750 -o pebblify -g pebblify /var/lib/pebblify
	@if [ ! -f /etc/pebblify/config.toml ]; then \
		install -o pebblify -g pebblify -m 0640 config.toml /etc/pebblify/config.toml; \
		echo "Created /etc/pebblify/config.toml"; \
	else \
		echo "Skipped /etc/pebblify/config.toml (already exists)"; \
	fi
	@if [ ! -f /etc/pebblify/.env ]; then \
		install -o pebblify -g pebblify -m 0600 systemd/pebblify.env.example /etc/pebblify/.env; \
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
	@echo "  3. Run: systemctl daemon-reload"
	@echo "  4. Enable + start: systemctl enable --now pebblify"

# install-podman: rootless, per-user Podman Quadlet install for systemd user session.
# Does NOT enable or start the unit — operator does this manually.
# Requires podman on PATH. Warns but continues if systemd user session is unavailable.
.PHONY: install-podman
install-podman:
	@if ! command -v podman >/dev/null 2>&1; then \
		echo "Error: podman not found on PATH. Install Podman before running this target."; \
		exit 1; \
	fi
	@if ! systemctl --user status >/dev/null 2>&1; then \
		echo "Warning: systemd user session is not available. Quadlet units will not activate until a user session with systemd is present."; \
	fi
	install -d -m 0755 $(HOME)/.config/containers/systemd
	@if [ ! -f $(HOME)/.config/containers/systemd/pebblify.container ]; then \
		install -m 0644 quadlet/pebblify.container \
			$(HOME)/.config/containers/systemd/pebblify.container; \
		echo "Created $(HOME)/.config/containers/systemd/pebblify.container"; \
	else \
		echo "Skipped $(HOME)/.config/containers/systemd/pebblify.container (already exists)"; \
	fi
	install -d -m 0700 $(HOME)/.pebblify
	@if [ ! -f $(HOME)/.pebblify/config.toml ]; then \
		install -m 0600 config.toml $(HOME)/.pebblify/config.toml; \
		echo "Created $(HOME)/.pebblify/config.toml"; \
	else \
		echo "Skipped $(HOME)/.pebblify/config.toml (already exists)"; \
	fi
	@if [ ! -f $(HOME)/.pebblify/.env ]; then \
		install -m 0600 systemd/pebblify.env.example $(HOME)/.pebblify/.env; \
		echo "Created $(HOME)/.pebblify/.env"; \
	else \
		echo "Skipped $(HOME)/.pebblify/.env (already exists)"; \
	fi
	@echo ""
	@echo "Pebblify Quadlet installed."
	@echo "Next steps:"
	@echo "  1. Edit ~/.pebblify/config.toml"
	@echo "  2. Fill in secrets: ~/.pebblify/.env"
	@echo "  3. Reload user units: systemctl --user daemon-reload"
	@echo "  4. Start: systemctl --user start pebblify"
	@echo "  5. Enable on login: systemctl --user enable pebblify"

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
