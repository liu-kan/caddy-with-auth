# caddy-with-auth

A custom [Caddy](https://caddyserver.com/) Docker image **built from source** with [xcaddy](https://github.com/caddyserver/xcaddy), including the following plugins:

- [`github.com/greenpau/caddy-security`](https://github.com/greenpau/caddy-security) — authentication, authorization, and MFA (multi-factor authentication)
- [`github.com/caddy-dns/cloudflare`](https://github.com/caddy-dns/cloudflare) — Cloudflare DNS-01 ACME challenges for wildcard certificates

The image is published to Docker Hub at `${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth`.

## Features

- **Multi-platform**: `linux/amd64` and `linux/arm64/v8`
- **Tracks upstream releases**: Uses the floating `caddy:2` tag by default, so every build automatically picks up the latest Caddy 2.x release and upstream security fixes; `caddy-security` uses `latest` by default
- **Non-breaking security hardening**: Runs `apk upgrade --no-cache` in the final stage to apply published CVE patches for Alpine base-image packages, such as zlib CVE-2026-22184 and OpenSSL, without changing Caddy's behavior
- **Reproducible builds**: All versions can be pinned with build arguments

## Usage

```bash
docker pull ${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth:latest
```

`docker-compose.yml`:

```yaml
services:
  caddy:
    image: remnawave/caddy-with-auth:latest
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

## Building Locally

```bash
# Default build (tracks the latest versions)
docker build -t caddy-with-auth .

# Multi-platform build (requires buildx and QEMU)
docker buildx build --platform linux/amd64,linux/arm64/v8 -t caddy-with-auth .
```

### Build Arguments (Optional and Non-Breaking)

| Argument | Default | Description |
| --- | --- | --- |
| `CADDY_VERSION` | `2` | Maps to the `caddy:<ver>-builder-alpine` and `caddy:<ver>` tags. Example pinned version: `2.11.4` |
| `CADDY_SECURITY_VERSION` | `latest` | The caddy-security version. Pinned versions must include the `v` prefix, for example `v1.1.64` |

Pin versions for reproducible builds:

```bash
docker build \
  --build-arg CADDY_VERSION=2.11.4 \
  --build-arg CADDY_SECURITY_VERSION=v1.1.64 \
  -t caddy-with-auth .
```

> The build stage sets `GOFLAGS=-v`, causing `go build` to print progress for each package so that long builds do not appear to be stuck.

## Configuration Examples

The `examples/` directory provides several ready-to-use `Caddyfile` and `docker-compose.yml` configurations:

| Example | Description |
| --- | --- |
| `minimal-security-setup-with-mfa` | Minimal MFA authentication setup |
| `minimal-security-setup-with-mfa-with-api-without-auth` | MFA authentication with unauthenticated API routes |
| `minimal-security-setup-with-mfa-with-opened-api-sub` | MFA authentication with a public API subpath |
| `custom-webpath-with-auth-and-protected-api-route` | Custom web path with a protected API route |
| `custom-webpath-with-auth-with-api-without-auth` | Custom web path with unauthenticated API routes |
| `custom-webpath-with-auth-with-opened-api-sub` | Custom web path with a public API subpath |

## CI and Releases

`.github/workflows/build-and-push.yml` runs when a **tag is pushed**. It uses buildx and QEMU to build a multi-platform image, pushes it to Docker Hub, and tags it with both `latest` and the Git tag:

```bash
git tag v2.11.4-1
git push origin v2.11.4-1
```

The workflow can also be started manually from the GitHub Actions page using `workflow_dispatch`.

## License

See the repository's `LICENSE` file, if present.
