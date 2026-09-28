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
#
# otlplog 导出器下限：依赖图里 go.opentelemetry.io/otel/log 已被拉到 v0.22.0
# （sdk/log、stdoutlog 要求），而 caddy、caddy-security、autoexport 只要求
# otlploggrpc/otlploghttp v0.20.0，二者 API 不兼容（undefined: api.KeyValue），
# 原配置在 2026-09-26 已无法编译。把导出器抬到配套的 v0.22.0 即可，
# linux/amd64 与 linux/arm64 均已验证可以编译。
#
# caddy-combine-ip-ranges：把内置 static（启动即生效的静态地址段）与
# caddy-cloudflare-ip（后台定期刷新的 Cloudflare 地址段）合并给 trusted_proxies，
# 避免 cloudflare 模块首次拉取完成前或拉取失败时，可信代理列表为空。
RUN xcaddy build \
    --with github.com/greenpau/caddy-security@${CADDY_SECURITY_VERSION} \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/WeidiDeng/caddy-cloudflare-ip \
    --with github.com/fvbommel/caddy-combine-ip-ranges \
    --with google.golang.org/grpc@v1.83.2 \
    --with github.com/klauspost/compress \
    --with golang.org/x/text \
    --with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc \
    --with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp 


    #\
    #--with google.golang.org/grpc@v1.83.2 
    #--with github.com/klauspost/compress@v1.19.1 \
    #--with golang.org/x/text@v0.40.0 \
    #--with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc@v0.22.0 \
    #--with go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp@v0.22.0

# 构建期断言：真实访客 IP 依赖的三个地址段模块必须都编译进去，缺一个就让构建失败。
RUN set -eu; \
    /build/caddy list-modules > /tmp/modules.txt; \
    for module in http.ip_sources.static http.ip_sources.cloudflare http.ip_sources.combine; do \
      grep -Fqx "$module" /tmp/modules.txt || { echo >&2 "error: missing Caddy module $module"; exit 1; }; \
    done

# ---- final stage ----------------------------------------------------------
# Minimal DHI runtime (no shell / no package manager). Binary path matches
# the upstream DHI image layout at /usr/local/bin/caddy.
FROM dhi.io/caddy:${CADDY_VERSION}

COPY --from=builder /build/caddy /usr/local/bin/caddy
