# Multi-stage build for peersh-signaling.
#
# Stage 1: build the static binary inside a Go toolchain image.
# Stage 2: copy the binary into a minimal alpine runtime, run as non-root.
#
# Build context is the repo root: docker build -f server/deploy/Dockerfile .

FROM golang:1.24-alpine AS builder

# Speed up rebuilds: pull module deps first, then source.
WORKDIR /src
COPY go.work go.work.sum ./
COPY core/go.mod core/go.sum ./core/
COPY server/go.mod server/go.sum ./server/
COPY cli/go.mod ./cli/
COPY windows/go.mod ./windows/
RUN go work sync || true

COPY core/ ./core/
COPY server/ ./server/

ENV CGO_ENABLED=0
ARG VERSION=0.1
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/peersh-signaling ./server/cmd/peersh-signaling

FROM alpine:3.20

# Non-root user with a fixed UID for predictable volume permissions.
RUN addgroup -S -g 65532 peersh \
 && adduser  -S -u 65532 -G peersh -h /home/peersh peersh

# Persistent state lives at /data: the SQLite DB plus any TLS material the
# operator chooses to mount.
RUN mkdir -p /data /etc/peersh && chown -R peersh:peersh /data /etc/peersh

COPY --from=builder /out/peersh-signaling /usr/local/bin/peersh-signaling

USER peersh
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8443

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:8443/healthz || exit 1

ENV PEERSH_SIGNALING_DB=/data/peersh-signaling.db

ENTRYPOINT ["peersh-signaling"]
CMD ["serve", "-config", "/etc/peersh/signaling.toml", "-insecure-http"]
