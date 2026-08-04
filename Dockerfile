# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Overridable build args (non-breaking, optional)
#   docker build --build-arg CADDY_VERSION=2.11.4 \
#                --build-arg CADDY_SECURITY_VERSION=v1.1.64 .
# ---------------------------------------------------------------------------
# Caddy version, maps to the official caddy:<ver>-builder-alpine / caddy:<ver> tags.
# Default 2 = floating major tag; every pull gets the latest 2.x and picks up
# upstream security fixes automatically. Pin with: --build-arg CADDY_VERSION=2.11.4
ARG CADDY_VERSION=2
# caddy-security plugin version. Default latest = fetch the newest upstream
# release at build time (currently v1.1.64). Pin with (Go modules need the v
# prefix): --build-arg CADDY_SECURITY_VERSION=v1.1.64
ARG CADDY_SECURITY_VERSION=latest

# ---- builder stage --------------------------------------------------------
# The official Alpine builder image is multi-arch (incl. linux/arm64/v8) and
# matches the Alpine-based final image. Under buildx + QEMU it runs as the
# target arch, so xcaddy compiles a native binary for that arch from source.
FROM caddy:${CADDY_VERSION}-builder-alpine AS builder

# ARG scope resets after FROM, so re-declare it here.
ARG CADDY_SECURITY_VERSION

# Print each package as it is fetched/compiled so the long xcaddy `go build`
# step reports progress instead of looking stuck. GOFLAGS applies -v to every
# underlying go command only when that command knows the flag, so it does not
# override xcaddy's default -ldflags/-trimpath/-tags build flags.
ENV GOFLAGS=-v

RUN xcaddy build \
    --with github.com/greenpau/caddy-security@${CADDY_SECURITY_VERSION} \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/WeidiDeng/caddy-cloudflare-ip

# ---- final stage ----------------------------------------------------------
FROM caddy:${CADDY_VERSION}

# Optional, non-breaking security hardening: upgrade Alpine base packages that
# already have published patches, fixing base-image CVEs such as
# zlib (CVE-2026-22184) / openssl. It does not change Caddy behavior, and every
# rebuild automatically pulls the latest patches.
RUN apk upgrade --no-cache && apk del curl

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
