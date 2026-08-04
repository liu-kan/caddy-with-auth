# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Overridable build args (non-breaking, optional)
#   docker build --build-arg CADDY_VERSION=2.11.4 \
#                --build-arg CADDY_SECURITY_VERSION=v1.1.64 .
# ---------------------------------------------------------------------------
# Caddy version, maps to dhi.io/caddy:<ver>-debian-dev (builder) and
# dhi.io/caddy:<ver> (runtime). Default 2 = floating major tag; every pull
# gets the latest 2.x and picks up upstream security fixes automatically.
# Pin with: --build-arg CADDY_VERSION=2.11.4
ARG CADDY_VERSION=2
# caddy-security plugin version. Default latest = fetch the newest upstream
# release at build time (currently v1.1.64). Pin with (Go modules need the v
# prefix): --build-arg CADDY_SECURITY_VERSION=v1.1.64
ARG CADDY_SECURITY_VERSION=latest

# ---- builder stage --------------------------------------------------------
# DHI Caddy debian-dev image: shell + apt for installing the Go toolchain.
# Requires: docker login dhi.io
FROM dhi.io/caddy:${CADDY_VERSION}-debian-dev AS builder

# ARG scope resets after FROM, so re-declare it here.
ARG CADDY_SECURITY_VERSION
# BuildKit sets this to the target arch (amd64 / arm64). Used to pick the
# matching Go tarball from https://go.dev/dl/.
ARG TARGETARCH

# Fetch tools only — do not install Debian's golang-* packages (they lag
# upstream and inflate CVE counts). xcaddy comes from go install below.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gzip \
    && rm -rf /var/lib/apt/lists/*

# Install the newest Stable Go toolchain from go.dev for TARGETARCH.
# Checksum is taken from the same go.dev/dl JSON metadata.
RUN set -eux; \
    arch="${TARGETARCH:?}"; \
    case "$arch" in amd64|arm64) ;; \
      *) echo >&2 "error: unsupported TARGETARCH: $arch"; exit 1 ;; \
    esac; \
    meta="$(mktemp)"; \
    curl -fsSL 'https://go.dev/dl/?mode=json' -o "$meta"; \
    version="$(sed -n 's/.*"version": "\(go[^"]*\)".*/\1/p' "$meta" | head -1)"; \
    filename="${version}.linux-${arch}.tar.gz"; \
    sha256="$(grep -A5 -F "\"filename\": \"${filename}\"" "$meta" \
        | sed -n 's/.*"sha256": "\([^"]*\)".*/\1/p' | head -1)"; \
    test -n "$version" && test -n "$sha256"; \
    curl -fsSL "https://go.dev/dl/${filename}" -o /tmp/go.tgz; \
    echo "${sha256}  /tmp/go.tgz" | sha256sum -c -; \
    rm -rf /usr/local/go; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz "$meta"; \
    /usr/local/go/bin/go version

ENV CGO_ENABLED=0
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"
# Print each package as it is fetched/compiled so the long xcaddy `go build`
# step reports progress instead of looking stuck. GOFLAGS applies -v to every
# underlying go command only when that command knows the flag, so it does not
# override xcaddy's default -ldflags/-trimpath/-tags build flags.
ENV GOFLAGS=-v

RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

WORKDIR /build

# grpc pin: caddy-security's own go.mod still floors google.golang.org/grpc
# below the fix for GHSA-hrxh-6v49-42gf. Go's MVS takes the max requirement
# across the build graph, so this --with raises the floor without needing
# an upstream caddy-security release. Verified to compile as of 2026-08-04.
#
# cel-go is NOT bumped the same way: caddy core's own celmatcher.go (not
# caddy-security) uses cel-go's pre-v0.29 interpreter.Interpretable API,
# which v0.29.0 renamed/broke to InterpretableV2. Forcing cel-go@v0.29.0
# (via --replace, since it has no root-level package for --with to import)
# fails the build. Revisit once Caddy core itself updates its cel-go usage.
RUN xcaddy build \
    --with github.com/greenpau/caddy-security@${CADDY_SECURITY_VERSION} \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/WeidiDeng/caddy-cloudflare-ip \
    --with google.golang.org/grpc@v1.82.1

# ---- final stage ----------------------------------------------------------
# Minimal DHI runtime (no shell / no package manager). Binary path matches
# the upstream DHI image layout at /usr/local/bin/caddy.
FROM dhi.io/caddy:${CADDY_VERSION}

COPY --from=builder /build/caddy /usr/local/bin/caddy
