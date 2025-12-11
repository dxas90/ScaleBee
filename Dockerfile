# syntax=docker/dockerfile:1

# Multi-platform build optimized with cross-compilation
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Build arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build
COPY . /build/
ENV GOPROXY=https://proxy.golang.org,direct

RUN go mod tidy && go mod vendor

# Cross-compile for target platform (fast on any builder platform)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -a -installsuffix cgo \
    -ldflags="-w -s" \
    -o scalebee .

FROM alpine:3.23 AS production
ARG CREATED="0000-00-00T00:00:00Z"

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

LABEL org.opencontainers.image.authors="Daniel Ramirez <dxas90@gmail.com>" \
    org.opencontainers.image.created=${CREATED} \
    org.opencontainers.image.description="Docker Swarm autoscaler based on Prometheus metrics" \
    org.opencontainers.image.licenses="MIT" \
    org.opencontainers.image.source="https://github.com/dxas90/scalebee.git" \
    org.opencontainers.image.title="ScaleBee" \
    org.opencontainers.image.version="1.0.0"

WORKDIR /app
COPY --from=builder /build/scalebee /app/

# Note: Running as root to access Docker socket in Colima/dev environments
# In production, configure proper socket permissions or use user namespaces

# Environment variables with defaults
ENV PROMETHEUS_URL=http://prometheus:9090 \
    LOOP=yes \
    INTERVAL_SECONDS=60 \
    CPU_PERCENTAGE_UPPER_LIMIT=75 \
    CPU_PERCENTAGE_LOWER_LIMIT=20 \
    MEMORY_PERCENTAGE_UPPER_LIMIT=80 \
    MEMORY_PERCENTAGE_LOWER_LIMIT=20

ENTRYPOINT [ "/app/scalebee" ]
