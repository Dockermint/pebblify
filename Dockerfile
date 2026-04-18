# ==============================================================================
# BUILD STAGE
# ==============================================================================
FROM --platform=$BUILDPLATFORM golang:1.25.4-alpine3.22 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

RUN apk add --no-cache ca-certificates=20260413 tzdata=2026a

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X main.Version=${VERSION} -X main.Revision=${REVISION}" \
    -trimpath \
    -o pebblify \
    ./cmd/pebblify

# ==============================================================================
# PRODUCTION STAGE
# ==============================================================================
FROM alpine:3.22

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

LABEL org.opencontainers.image.title="Pebblify" \
      org.opencontainers.image.description="LevelDB to PebbleDB migration tool and long-running daemon for Cosmos/CometBFT nodes (CLI + daemon modes)." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/Dockermint/Pebblify" \
      org.opencontainers.image.url="https://www.dockermint.io" \
      org.opencontainers.image.documentation="https://docs.dockermint.io/pebblify/" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="Dockermint" \
      org.opencontainers.image.authors="Dockermint" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.base.name="alpine:3.22"

RUN apk add --no-cache \
        ca-certificates=20260413 \
        tzdata=2026a \
        wget=1.25.0 \
    && addgroup -g 10000 pebblify \
    && adduser -D -H -u 10000 -G pebblify -s /sbin/nologin pebblify

COPY --from=builder /build/pebblify /usr/local/bin/pebblify

# CLI legacy health port + metrics port
EXPOSE 8086 9090
# Daemon: Prometheus/telemetry (2323), REST API (2324), Health (2325)
EXPOSE 2323 2324 2325

USER 10000:10000

ENTRYPOINT ["pebblify"]
