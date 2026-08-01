# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# 可覆盖的构建参数（非破坏、可选）
#   docker build --build-arg CADDY_VERSION=2.11.4 \
#                --build-arg CADDY_SECURITY_VERSION=v1.1.27 .
# ---------------------------------------------------------------------------
# Caddy 版本，对应官方 caddy:<ver>-builder / caddy:<ver> 标签。
# 默认 2 = 浮动大版本标签（caddy:2-builder / caddy:2），每次拉取都是最新 2.x，
# 自动获取上游安全修复。需固定版本时：--build-arg CADDY_VERSION=2.11.4
ARG CADDY_VERSION=2
# caddy-security 插件版本，默认 latest = 编译时拉取上游最新发行版（当前 v1.1.64）。
# 需固定版本时（注意 Go 模块要求 v 前缀）：--build-arg CADDY_SECURITY_VERSION=v1.1.64
ARG CADDY_SECURITY_VERSION=latest

# ---- builder stage --------------------------------------------------------
# 官方 builder 镜像本身即多架构（含 linux/arm64/v8），
# 在 buildx + QEMU 下会以目标架构运行，xcaddy 原生产出对应架构二进制。
FROM caddy:${CADDY_VERSION}-builder AS builder

# FROM 之后 ARG 作用域重置，需重新声明。
ARG CADDY_SECURITY_VERSION

RUN xcaddy build \
    --with github.com/greenpau/caddy-security@${CADDY_SECURITY_VERSION} \
    --with github.com/caddy-dns/cloudflare

# ---- final stage ----------------------------------------------------------
FROM caddy:${CADDY_VERSION}

# 可选、非破坏的安全加固：升级 Alpine 基础镜像中已发布补丁的软件包，
# 修复如 zlib(CVE-2026-22184)/openssl 等基础镜像 CVE。
# 该步骤不改变 Caddy 行为，且每次重建都会自动拉取最新补丁。
RUN apk upgrade --no-cache

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
