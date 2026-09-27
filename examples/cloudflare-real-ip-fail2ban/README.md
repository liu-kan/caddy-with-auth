# Cloudflare 橙云之后记录真实访客 IP，并单独写登录日志供 Fail2ban 使用

站点经 Cloudflare 代理（橙云）时，Caddy 收到的 TCP 连接来自 Cloudflare 边缘节点，而不是访客。本示例让 Caddy：

- 在日志的 `request.client_ip` 里记录真实访客 IP，并把它传给上游（`X-Forwarded-For`、`X-Real-IP`）；
- 把**经 Cloudflare 传来**的登录请求（默认 `/api/auth/*`，即 LibreChat 的登录接口）额外写入一份单独的登录日志，供宿主机上的 Fail2ban 读取。

## 原理

| 信息 | 能否伪造 | 日志字段 |
|---|---|---|
| TCP 连接来源（上一跳） | 不能：TCP 要完成三次握手 | `request.remote_ip` |
| 请求头 `CF-Connecting-IP`（Cloudflare 写入的访客 IP） | 能：直连源站的人可以自己写 | 取值后记为 `request.client_ip` |

Caddy 本体按下面的规则确定 `client_ip`：TCP 来源在 `trusted_proxies` 列表内时，采信 `client_ip_headers` 指定的请求头；否则忽略请求头，`client_ip` 就等于 TCP 来源。所以"知道 Cloudflare 的地址段"是判断请求头能否采信的依据，**提取访客 IP 的是 Caddy 本体，不是插件**。

`trusted_proxies` 的地址段由 `combine` 合并两份各自独立的列表：

1. **静态列表**：`cloudflare-ranges.caddy` 文件里那一行 `static <地址段…>`，由 Caddyfile 在 `combine` 块里 `import`。Caddy 启动时就生效，只有重新生成文件并 reload 后才变化。
2. **`caddy-cloudflare-ip` 的列表**：这个插件在内存里维护，每 `interval`（这里是 12 小时）从 Cloudflare 官网刷新一次。它**不会**读写 `cloudflare-ranges.caddy`。

两份列表没有共享变量；`combine` 在每个请求到达时把它们拼在一起使用。`caddy-cloudflare-ip` 首次拉取是在后台异步进行的，拉取完成前、或拉取失败时，它的列表为空，这时靠静态列表保底；Cloudflare 以后新增地址段，由它自动补上。

登录日志只接收满足 `{client_ip} != {remote_host}` 的请求，也就是 Caddy 确实从可信连接里取到了访客 IP 的请求。绕过 Cloudflare 直连源站的请求、以及地址段没能识别的 Cloudflare 请求（此时 `client_ip` 等于 Cloudflare 的 IP），都不会写进登录日志，Fail2ban 也就不会拿 Cloudflare 的 IP 去封。

## 文件

| 文件 | 作用 |
|---|---|
| `Caddyfile` | 可信代理、访客 IP 请求头、两份日志、反向代理 |
| `gen-cloudflare-ranges.sh` | 从 Cloudflare 官网生成 `cloudflare-ranges.caddy`；拉取或校验失败时不覆盖原文件 |
| `docker-compose.yml` | bridge 网络、发布 80/443、日志目录挂载到宿主机 |

## 使用

```bash
# 1. Caddy 与上游共用的专用网络；上游服务（例如 LibreChat 的 api）也要加入它
docker network create caddy-edge

# 2. 生成静态地址段
./gen-cloudflare-ranges.sh

# 3. 日志目录：运行镜像的用户是 uid/gid 65532
sudo install -d -o 65532 -g 65532 -m 0750 /var/log/caddy

# 4. 修改 docker-compose.yml 里的 CADDY_IMAGE、SITE_DOMAIN、UPSTREAM 后启动
docker compose run --rm caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker compose up -d
```

验证：从外部访问一次登录接口后，

```bash
sudo tail -n 5 /var/log/caddy/login.json | jq -c '{remote_ip: .request.remote_ip, client_ip: .request.client_ip, uri: .request.uri, status}'
```

`remote_ip` 应是 Cloudflare 的 IP，`client_ip` 应是访客 IP。如果 `remote_ip` 是 `172.` 或 `fd` 开头的 Docker 内部地址，说明 Cloudflare 走 IPv6 回源，而 Docker 用户态代理改写了来源：让回源只用 A 记录，或者给 Docker 开启 IPv6。

## 更新静态地址段

Cloudflare 很少调整地址段，`caddy-cloudflare-ip` 也会自动补充新增的地址段。需要更新静态文件时：

```bash
./gen-cloudflare-ranges.sh && docker exec caddy /usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

## Fail2ban filter 示例

在 filter 里再加一道保险：先捕获 `remote_ip`，要求 `client_ip` 与它不同。把 `401|429` 换成你实际看到的登录失败状态码。

```ini
[Definition]
failregex = ^\{.*"remote_ip":"(?P<edge>[^"]+)".*"client_ip":"(?!(?P=edge)")<ADDR>".*"method":"POST".*"uri":"/api/auth/login".*"status":(?:401|429)\b
ignoreregex =
datepattern = "ts":{Epoch}
```

## 注意

- Docker 发布的端口不经过宿主机防火墙的 INPUT 链。"源站只允许 Cloudflare 访问"要用云防火墙/安全组，或者 `DOCKER-USER` 链来实现。
- 只对橙云（代理）的主机名生效；灰云（仅 DNS）的流量不经过 Cloudflare。
