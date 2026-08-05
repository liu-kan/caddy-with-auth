# caddy-with-auth

A custom [Caddy](https://caddyserver.com/) Docker image built from source with [xcaddy](https://github.com/caddyserver/xcaddy), based on [Docker Hardened Images](https://docs.docker.com/dhi/) (`dhi.io/caddy`).

Plugins:

- [`github.com/greenpau/caddy-security`](https://github.com/greenpau/caddy-security) — authentication, authorization, and MFA
- [`github.com/caddy-dns/cloudflare`](https://github.com/caddy-dns/cloudflare) — Cloudflare DNS-01 ACME challenges
- [`github.com/WeidiDeng/caddy-cloudflare-ip`](https://github.com/WeidiDeng/caddy-cloudflare-ip) — trusted Cloudflare client IP restoration

Published to Docker Hub as `${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth`.

## Features

- **Docker Hardened Images**: builder `dhi.io/caddy:<ver>-debian-dev`, runtime `dhi.io/caddy:<ver>` (minimal, no shell / no package manager, non-root uid `65532`)
- **Official Go toolchain**: latest Stable Go from [go.dev/dl](https://go.dev/dl/) (checksum-verified, `amd64` / `arm64`), not Debian `golang-*` packages
- **Dependency CVE floor-raising** at build time via `xcaddy --with` (`grpc`, `klauspost/compress`, `golang.org/x/text`)
- **Multi-platform**: `linux/amd64` and `linux/arm64/v8`
- **Tracks upstream releases**: floating `CADDY_VERSION=2` and `CADDY_SECURITY_VERSION=latest` by default; pin with build args when needed

## Runtime notes (DHI non-root)

The runtime user is **uid/gid `65532`**. Writable paths in the image are `/data`, `/config`, and `/srv`. Host or named volumes mounted there must be owned by `65532`, for example:

```bash
docker run --rm -v caddy_data:/data busybox chown -R 65532:65532 /data
```

Prefer log paths under `/data/...` (for example `/data/caddy/logs/access.log`). Creating `/logs/...` on `/` will fail with `permission denied`.

## Usage

```bash
docker pull ${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth:latest
```

```yaml
services:
  caddy:
    image: ${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth:latest
    restart: always
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
      - caddy_config:/config

volumes:
  caddy_data:
  caddy_config:
```

## Building locally

Requires authentication to the DHI registry:

```bash
docker login dhi.io
docker build -t caddy-with-auth .
docker buildx build --platform linux/amd64,linux/arm64/v8 -t caddy-with-auth .
```

### Build arguments

| Argument | Default | Description |
| --- | --- | --- |
| `CADDY_VERSION` | `2` | Maps to `dhi.io/caddy:<ver>-debian-dev` and `dhi.io/caddy:<ver>` |
| `CADDY_SECURITY_VERSION` | `latest` | caddy-security version; pinned values need the `v` prefix (for example `v1.1.64`) |

```bash
docker build \
  --build-arg CADDY_VERSION=2.11.4 \
  --build-arg CADDY_SECURITY_VERSION=v1.1.64 \
  -t caddy-with-auth .
```

## Configuration examples

| Example | Description |
| --- | --- |
| `minimal-security-setup-with-mfa` | Minimal MFA authentication setup |
| `minimal-security-setup-with-mfa-with-api-without-auth` | MFA with unauthenticated API routes |
| `minimal-security-setup-with-mfa-with-opened-api-sub` | MFA with a public API subpath |
| `custom-webpath-with-auth-and-protected-api-route` | Custom web path with a protected API route |
| `custom-webpath-with-auth-with-api-without-auth` | Custom web path with unauthenticated API routes |
| `custom-webpath-with-auth-with-opened-api-sub` | Custom web path with a public API subpath |

## CI and releases

`.github/workflows/build-and-push.yml` runs on tag push or `workflow_dispatch`. It logs into Docker Hub and `dhi.io` (`DHI_USER` / `DHI_TOKEN`), builds multi-platform images, and pushes `latest` plus the Git tag (or the manual tag input).

```bash
git tag 20260804
git push origin 20260804
```

## License

See the repository `LICENSE` file, if present.
