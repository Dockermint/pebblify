# ==============================================================================
# BUILD STAGE
# ==============================================================================
FROM --platform=$BUILDPLATFORM golang:1.25.4-alpine3.22 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

RUN apk add --no-cache ca-certificates tzdata

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
      org.opencontainers.image.description="LevelDB to PebbleDB migration tool for Cosmos/CometBFT nodes." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/Dockermint/Pebblify" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="Dockermint" \
      org.opencontainers.image.created="${CREATED}"

RUN apk add --no-cache ca-certificates tzdata curl \
    && adduser -D -H -u 10000 -s /sbin/nologin appuser

COPY --from=builder /build/pebblify /usr/local/bin/pebblify

EXPOSE 8086

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8086/healthz/live || exit 1

USER 10000:10000

ENTRYPOINT ["pebblify"]
