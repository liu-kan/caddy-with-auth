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
# release at build time. Pin with (Go modules need the v
# prefix): --build-arg CADDY_SECURITY_VERSION=v1.1.64
ARG CADDY_SECURITY_VERSION=latest
# Follow stable releases within the maintained module major versions.
# Optional overrides allow a tested release to be pinned without editing code.
ARG CORAZA_CADDY_VERSION=latest
ARG CORAZA_VERSION=latest
# Track patched v1.83.x releases: v1.84.0 is flagged by GO-2026-6443.
# Override this query after validating a newer branch; the binary scan gates it.
ARG GRPC_VERSION=v1.83

# ---- builder stage --------------------------------------------------------
# DHI Caddy debian-dev image: shell + apt for installing the Go toolchain.
# Requires: docker login dhi.io
FROM dhi.io/caddy:${CADDY_VERSION}-debian-dev AS builder

# ARG scope resets after FROM, so re-declare it here.
ARG CADDY_SECURITY_VERSION
ARG CORAZA_CADDY_VERSION
ARG CORAZA_VERSION
ARG GRPC_VERSION
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

# CI supplies one refresh token per workflow run and reuses it for both
# image variants. This invalidates floating Go/tool/module/scanner layers once
# per run while allowing the second variant to reuse the compiled binary.
# For local rebuilds, pass a fresh DEPENDENCY_REFRESH value when updating deps.
ARG DEPENDENCY_REFRESH=manual

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

RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest \
    && go install golang.org/x/vuln/cmd/govulncheck@latest

WORKDIR /build

# plugins/coraza-ipset makes Coraza's @ipMatchFromFile (and @ipMatchF) find
# an address by binary search over sorted ranges instead of comparing every
# network of the list. Test it against the Coraza release this build links:
# the tests compare it with Coraza's own matching and fail when Coraza
# changes the code it reproduces.
COPY plugins/ /build/plugins/
RUN set -eu; \
    go -C /build/plugins/coraza-ipset get "github.com/corazawaf/coraza/v3@${CORAZA_VERSION}"; \
    go -C /build/plugins/coraza-ipset test ./...

# Upgrade selected dependencies within their module major versions. Avoid a
# blanket `go get -u`: unrelated API changes (for example cel-go) can break
# Caddy before upstream updates its integration.
# Coraza's maintained Caddy plugin is /v2. The unsuffixed module still resolves
# to the obsolete v1.2.2 release and embeds a vulnerable Coraza v3 prerelease.
# Explicitly select the latest Coraza v3 so fixes do not wait for plugin releases.
# caddy-combine-ip-ranges：把内置 static（启动即生效的静态地址段）与
# caddy-cloudflare-ip（后台定期刷新的 Cloudflare 地址段）合并给 trusted_proxies，
# 避免 cloudflare 模块首次拉取完成前或拉取失败时，可信代理列表为空。
# Resolve the minor-version query first: xcaddy requires a full version.
RUN set -eu; \
    grpc_version="$(go list -m -f '{{.Version}}' "google.golang.org/grpc@${GRPC_VERSION}")"; \
    xcaddy build \
    --with github.com/greenpau/caddy-security@${CADDY_SECURITY_VERSION} \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/WeidiDeng/caddy-cloudflare-ip \
    --with github.com/fvbommel/caddy-combine-ip-ranges \
    --with "google.golang.org/grpc@${grpc_version}" \
    --with github.com/klauspost/compress \
    --with golang.org/x/text \
    --with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc \
    --with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp \
    --with github.com/corazawaf/coraza-caddy/v2@${CORAZA_CADDY_VERSION} \
    --with github.com/corazawaf/coraza/v3@${CORAZA_VERSION} \
    --with github.com/liu-kan/caddy-with-auth/plugins/coraza-ipset=/build/plugins/coraza-ipset

# Check the actual binary, including the minimum patched Coraza version.
# Module-level scanning also catches vulnerable dependencies whose affected
# functions may not be reachable; this matches dependency inventory scanners.
# Govulncheck JSON mode does not fail on findings; the report checker below
# enforces publication policy while retaining the complete JSON report.
COPY scripts/check-caddy-dependencies/main.go /build/check-caddy-dependencies.go
COPY scripts/check-caddy-vulnerabilities/main.go /build/check-caddy-vulnerabilities.go
RUN go run /build/check-caddy-dependencies.go /build/caddy \
    && go version -m /build/caddy > /build/caddy-build-info.txt \
    && cat /build/caddy-build-info.txt \
    && govulncheck -mode=binary -scan=module -json /build/caddy > /build/vulnerabilities.json \
    && go run /build/check-caddy-vulnerabilities.go /build/vulnerabilities.json

# 构建期断言：真实访客 IP 地址段模块、WAF 和 coraza-ipset 插件必须都编译进去。
RUN set -eu; \
    /build/caddy list-modules > /tmp/modules.txt; \
    for module in http.ip_sources.static http.ip_sources.cloudflare http.ip_sources.combine http.handlers.waf; do \
      grep -Fqx "$module" /tmp/modules.txt || { echo >&2 "error: missing Caddy module $module"; exit 1; }; \
    done; \
    grep -Fq "github.com/liu-kan/caddy-with-auth/plugins/coraza-ipset" /build/caddy-build-info.txt \
      || { echo >&2 "error: missing the coraza-ipset plugin"; exit 1; }

# Export the complete inventory and scan report as CI artifacts.
FROM scratch AS security-reports
COPY --from=builder /build/caddy-build-info.txt /caddy-build-info.txt
COPY --from=builder /build/vulnerabilities.json /vulnerabilities.json

# ---- final stages ---------------------------------------------------------
# Development variant: keeps the shell and package manager from the dev base.
FROM dhi.io/caddy:${CADDY_VERSION}-debian-dev AS final-dev

# Refresh installed Debian packages on each CI run before copying Caddy.
ARG DEPENDENCY_REFRESH=manual
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get upgrade -y --no-install-recommends \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/caddy /usr/local/bin/caddy

# Minimal DHI runtime (no shell / no package manager). Kept last so builds
# without --target default to the runtime variant.
FROM dhi.io/caddy:${CADDY_VERSION} AS final

COPY --from=builder /build/caddy /usr/local/bin/caddy
