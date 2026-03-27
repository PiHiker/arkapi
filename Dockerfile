# ---- Build stage ----
FROM golang:1.24-bookworm AS builder

WORKDIR /build

# Copy source and resolve dependencies
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -o arkapi ./cmd/arkapi

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        dnsutils \
        whois \
        curl \
        ca-certificates \
        netbase && \
    rm -rf /var/lib/apt/lists/*

# Run as non-root
RUN useradd -r -s /usr/sbin/nologin arkapi

RUN mkdir -p /data /usr/local/share/arkapi && chown arkapi:arkapi /data

COPY --from=builder /build/arkapi /usr/local/bin/arkapi
COPY openapi.json /usr/local/share/arkapi/openapi.json

USER arkapi

ENTRYPOINT ["arkapi"]
