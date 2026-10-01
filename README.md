# caddy-with-auth

A custom [Caddy](https://caddyserver.com/) Docker image built from source with [xcaddy](https://github.com/caddyserver/xcaddy), based on [Docker Hardened Images](https://docs.docker.com/dhi/) (`dhi.io/caddy`).

Plugins:

- [`github.com/greenpau/caddy-security`](https://github.com/greenpau/caddy-security) — authentication, authorization, and MFA
- [`github.com/caddy-dns/cloudflare`](https://github.com/caddy-dns/cloudflare) — Cloudflare DNS-01 ACME challenges
- [`github.com/WeidiDeng/caddy-cloudflare-ip`](https://github.com/WeidiDeng/caddy-cloudflare-ip) — Cloudflare IP ranges for `trusted_proxies`, refreshed in the background
- [`github.com/fvbommel/caddy-combine-ip-ranges`](https://github.com/fvbommel/caddy-combine-ip-ranges) — combines several IP range sources (for example built-in `static` + `cloudflare`)
- [`github.com/corazawaf/coraza-caddy/v2`](https://github.com/corazawaf/coraza-caddy) — Coraza WAF with the maintained v2 plugin and current stable Coraza v3

Published to Docker Hub as `${{ secrets.DOCKERHUB_USERNAME }}/caddy-with-auth`.

## Features

- **Docker Hardened Images**: builder and dev variant `dhi.io/caddy:<ver>-debian-dev`; runtime variant `dhi.io/caddy:<ver>` (minimal, no shell / no package manager, non-root uid `65532`)
- **Official Go toolchain**: latest Stable Go from [go.dev/dl](https://go.dev/dl/) (checksum-verified, `amd64` / `arm64`), not Debian `golang-*` packages
- **Dependency updates and checks**: selected dependencies follow stable releases; the resulting binary must contain the maintained Coraza plugin and Coraza >= `v3.3.3`; module-level `govulncheck` blocks findings with published fixes and reports unfixed findings before publication
- **Multi-platform**: `linux/amd64` and `linux/arm64/v8`
- **Tracks upstream releases**: floating `CADDY_VERSION=2` and `CADDY_SECURITY_VERSION=latest` by default; pin with build args when needed

## Runtime notes (DHI non-root, tags without `-dev`)

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
docker build --target final -t caddy-with-auth:latest .
docker build --target final-dev -t caddy-with-auth:latest-dev .
docker buildx build --platform linux/amd64,linux/arm64/v8 -t caddy-with-auth .
```

### Build arguments

| Argument | Default | Description |
| --- | --- | --- |
| `CADDY_VERSION` | `2` | Maps to `dhi.io/caddy:<ver>-debian-dev` and `dhi.io/caddy:<ver>` |
| `CADDY_SECURITY_VERSION` | `latest` | caddy-security version; pinned values need the `v` prefix (for example `v1.1.64`) |
| `CORAZA_CADDY_VERSION` | `latest` | Maintained `coraza-caddy/v2` plugin; optional stable version pin |
| `CORAZA_VERSION` | `latest` | Coraza v3 core; the actual binary must contain a stable version >= `v3.3.3` |
| `GRPC_VERSION` | `v1.83` | Newest patch on the v1.83.x branch; v1.84.0 is affected by GO-2026-6443; override after validating a newer branch |
| `DEPENDENCY_REFRESH` | `manual` | Change this value to refresh cached Go toolchain, tools, dependencies and vulnerability checks; CI supplies a fresh value each run |

```bash
docker build \
  --build-arg CADDY_VERSION=2.11.4 \
  --build-arg CADDY_SECURITY_VERSION=v1.1.64 \
  -t caddy-with-auth .
```

## Dependency security

The old unsuffixed `github.com/corazawaf/coraza-caddy` module resolves to v1.2.2 and embeds a Coraza v3 prerelease from 2023. Use `github.com/corazawaf/coraza-caddy/v2`; the build also requests the latest stable Coraza v3 explicitly. The binary check rejects Coraza below v3.3.3, covering [CVE-2023-40586](https://github.com/corazawaf/coraza/security/advisories/GHSA-c2pj-v37r-2p6h) and [CVE-2025-29914](https://github.com/corazawaf/coraza/security/advisories/GHSA-q9f5-625g-xm39), missing modules, legacy plugin paths and unverifiable replacements.

Every CI run pulls the current DHI base images and refreshes the Go toolchain, xcaddy, selected module versions and the Go vulnerability database. Both variants use the same refresh token, so the dev build can reuse the checked runtime binary. A module-level `govulncheck` finding with a published fix stops the build before publishing. Findings without a published fix remain visible in the build log and complete report; they are not treated as resolved. Scanner failures and malformed or incomplete reports also stop publication. No blanket transitive dependency upgrade is performed; incompatible APIs can still require an upstream fix or a tested version override.

Version arguments are queries used while resolving the complete dependency graph. The explicit gRPC branch can also select an earlier compatible caddy-security release; the exact versions are recorded in `caddy-build-info.txt`.

gRPC follows the latest stable v1.83.x patch rather than unrestricted `latest`: [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443) lists v1.83.2 as patched, while v1.84.0 is affected. Go minimum-version selection can still raise this version when another dependency requires it; the final binary scan checks the selected result.

CI builds and checks both architectures before publishing, then retains the full per-architecture dependency inventory and JSON scan report in the `caddy-dependency-security-reports` artifact for 30 days. Before publishing, it also runs the two Coraza HTTP regression cases against a temporary amd64 runtime container on the runner. Govulncheck JSON mode normally returns success even when vulnerabilities exist; a separate report check enforces the publication policy. Module-level reports can include vulnerabilities in packages not linked into the binary, so they do not establish exploitability by themselves.

For a fresh local dependency check, pass a new refresh value:

```bash
docker build --pull --build-arg DEPENDENCY_REFRESH="$(date -u +%Y%m%d%H%M%S)" --target final -t caddy-with-auth:latest .
```

Go dependency checks do not scan OS packages. The runtime variant has fewer packages than the dev variant; pulling current DHI images reduces stale base-image findings but does not guarantee zero CVEs. CVE counts can also differ between Docker Scout and the Go vulnerability database.

Run the binary-check regression tests locally:

```bash
go test scripts/check-caddy-dependencies/main.go scripts/check-caddy-dependencies/main_test.go
go test scripts/check-caddy-vulnerabilities/main.go scripts/check-caddy-vulnerabilities/main_test.go
python3 tests/test_coraza_cves.py caddy-with-auth:latest
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
| `cloudflare-real-ip-fail2ban` | Behind Cloudflare: log the real visitor IP and write a separate login log for Fail2ban |

## Cloudflare 真实访客 IP

`caddy-cloudflare-ip` 只提供 Cloudflare 的地址段，**提取访客 IP 的是 Caddy 本体**：TCP 来源在 `trusted_proxies` 内时，Caddy 才采信 `client_ip_headers` 指定的请求头（这里是 `CF-Connecting-IP`），把结果写入 `{client_ip}` 和日志的 `request.client_ip`；否则 `client_ip` 就是 TCP 来源。

`caddy-cloudflare-ip` 首次拉取在后台异步进行，完成前或失败时列表为空。所以推荐用 `combine` 把一份静态地址段（启动即生效）和它（自动补充新地址段）合并：

```caddyfile
{
	servers {
		trusted_proxies combine {
			import /etc/caddy/cloudflare-ranges.caddy   # 一行 "static <CIDR>..."
			cloudflare {
				interval 12h
				timeout 15s
			}
		}
		client_ip_headers CF-Connecting-IP
		trusted_proxies_strict
	}
}
```

完整示例（含静态地址段生成脚本、只记录经 Cloudflare 传来的登录请求的单独日志、Fail2ban filter）见 [`examples/cloudflare-real-ip-fail2ban`](examples/cloudflare-real-ip-fail2ban/)。镜像构建时会校验 `http.ip_sources.static`、`http.ip_sources.cloudflare`、`http.ip_sources.combine` 三个模块都已编译进去。

## CI and releases

`.github/workflows/build-and-push.yml` runs on tag push or `workflow_dispatch`. It logs into Docker Hub and `dhi.io` (`DHI_USER` / `DHI_TOKEN`) and builds both multi-platform variants in the same job, allowing the dev build to reuse the builder cache from the runtime build.

| Trigger | Runtime tags (`final`) | Dev tags (`final-dev`) |
| --- | --- | --- |
| Push Git tag `20261001` | `latest`, `20261001` | `latest-dev`, `20261001-dev` |
| Manual, `image_tag=20261001` | `latest`, `20261001` | `latest-dev`, `20261001-dev` |
| Manual, default or empty `image_tag` | `latest`, `manual` | `latest-dev`, `manual-dev` |

For manual runs, enter the base image tag without the `-dev` suffix. The manual input takes precedence even when the workflow is run against a Git tag. Builds without `--target` default to the runtime variant.

```bash
git tag 20260804
git push origin 20260804
```

## License

See the repository `LICENSE` file, if present.
