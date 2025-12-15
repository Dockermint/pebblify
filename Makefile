# ==============================================================================
# VARIABLES
# ==============================================================================

BINARY      := pebblify
IMAGE       := dockermint/pebblify

# Git metadata
VERSION     := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null | grep -v '^HEAD$$' || git describe --tags --always --dirty 2>/dev/null || echo "dev")
REVISION    := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
CREATED     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Local platform detection
LOCAL_OS    := $(shell go env GOOS)
LOCAL_ARCH  := $(shell go env GOARCH)

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
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY) .

.PHONY: build-linux-amd64
build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BINARY)-linux-amd64 .

.PHONY: build-linux-arm64
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINARY)-linux-arm64 .

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

.PHONY: install
install: build
	mv $(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || mv $(BINARY) /usr/local/bin/$(BINARY)

# ==============================================================================
# UTILITIES
# ==============================================================================

.PHONY: clean
clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64

.PHONY: info
info:
	@echo "Binary:   $(BINARY)"
	@echo "Image:    $(IMAGE)"
	@echo "Version:  $(VERSION)"
	@echo "Revision: $(REVISION)"
	@echo "Platform: $(LOCAL_OS)/$(LOCAL_ARCH)"
