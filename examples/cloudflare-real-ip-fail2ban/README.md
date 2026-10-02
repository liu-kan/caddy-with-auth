# Cloudflare 橙云之后记录真实访客 IP，并单独写登录日志供 Fail2ban 使用

站点经 Cloudflare 代理（橙云）时，Caddy 收到的 TCP 连接来自 Cloudflare 边缘节点，而不是访客。本示例让 Caddy：

- 在日志的 `request.client_ip` 里记录真实访客 IP，并把它传给上游（`X-Forwarded-For`、`X-Real-IP`）；
- 把**经 Cloudflare 传来**的账号密码登录及密码重置请求额外写入 `login.json`，供宿主机上的 Fail2ban 读取；支持 IPv4 和 IPv6 访客。
- 把 Caddy 的 `ERROR` 及以上级别运行日志单独写入 `error.json`，用于排查反向代理连接失败、证书错误等。

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

默认记录以下请求，成功和失败都记录：

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/api/auth/login` | 账号密码登录 |
| POST | `/api/auth/requestPasswordReset` | 申请重置密码 |
| POST | `/api/auth/resetPassword` | 提交新密码 |
| GET | `/reset-password` | 打开重置页面，查询参数不参与 `path` 匹配 |

`POST /api/auth/refresh` 不再进入认证日志，仍进入 `access.json`。旧日志是追加保存的，更新配置不会删除此前的 refresh 记录。保留了 `LOGIN_PATH` 环境变量覆盖登录路径的能力；若旧部署将它设置为 `/api/auth/*`，请删除该覆盖或改成 `/api/auth/login`，否则仍会匹配 refresh 等 POST 接口。

`access.json` 与 `login.json` 会把 URL 查询参数中的 `token` 替换成 `REDACTED`，并删除可能包含完整重置链接的 `Referer` 请求头；转发给 LibreChat 的请求不受影响。

## 文件

| 文件 | 作用 |
|---|---|
| `Caddyfile` | 可信代理、访客 IP 请求头、访问/登录/运行错误日志、反向代理 |
| `gen-cloudflare-ranges.sh` | 从 Cloudflare 官网生成 `cloudflare-ranges.caddy`；拉取或校验失败时不覆盖原文件 |
| `docker-compose.yml` | bridge 网络、发布 80/443、日志目录挂载到宿主机 |
| `fail2ban/filter.d/caddy-librechat.conf` | 账号密码登录失败过滤规则，`<ADDR>` 同时匹配 IPv4/IPv6 |

## 使用

```bash
# 1. Caddy 与上游共用的专用网络；上游服务（例如 LibreChat 的 api）也要加入它
docker network create caddy-edge

# 2. 生成静态地址段
./gen-cloudflare-ranges.sh

# 3. 日志目录：运行镜像的用户是 uid/gid 65532
sudo install -d -o 65532 -g 65532 -m 0750 /var/log/caddy

# 4. 修改 docker-compose.yml 里的 CADDY_IMAGE、SITE_DOMAIN、UPSTREAM 后启动
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/caddy caddy validate \
  --config /etc/caddy/Caddyfile --adapter caddyfile &&
docker compose up -d
```

验证：从外部访问一次登录接口后，设置实际宿主机日志路径，再执行以下短行命令：

```bash
CADDY_LOGIN_LOG=/var/log/caddy/login.json
# /opt/librechat-a 的部署若挂载在 logs/httpd，改用下面这行：
# CADDY_LOGIN_LOG=/opt/librechat-a/logs/httpd/login.json

sudo tail -n 5 "$CADDY_LOGIN_LOG" |
  jq -c '{status} + (.request | {remote_ip, client_ip, method, uri})'
```

`jq` 对 IPv4 和 IPv6 都按 JSON 字符串处理，无须替换冒号或拆分 IPv6 地址。如果把 `.request.client_ip` 手工断成 `.request.cli` 换行 `ent_ip`，会出现 `unexpected IDENT` 编译错误；这与日志中的 IP 类型无关。上面的写法避免长字段引用被拆断。

### IPv6 访客与 IPv6 回源

下面是正常情况，示例访客地址使用文档专用地址段：

```json
{"status":200,"remote_ip":"104.23.166.76","client_ip":"2001:db8:45d::196","method":"POST","uri":"/api/auth/login"}
```

`remote_ip` 是 Caddy 收到连接的上一跳，可以是 Cloudflare 的 IPv4 或 IPv6；`client_ip` 是访客地址，也可以分别为 IPv4 或 IPv6。IPv6 访客完全可以由 Cloudflare 通过 IPv4 回源，因此看到 IPv6 `client_ip` 不代表必须给 Docker 或源站开启 IPv6。

- 例如 `remote_ip=172.69.34.225` 是 Cloudflare 公网地址，不能按 `172.` 前缀判断成 Docker 私网。IPv4 私网中的 172 段是 `172.16.0.0/12`，即第二段为 16–31。
- 若上一跳实际变成 `172.18.x.x`、`10.x.x.x`、`192.168.x.x` 或 `fc00::/7` 内部地址，先检查 Docker 发布端口、额外代理和回源路径。只凭私网地址不能断定发生了 IPv6 回源；不要为了取访客 IP 而把整个 Docker 私网加入可信代理。
- Cloudflare 的 Pseudo IPv4 建议设为 `Off` 或 `Add Header`。`Overwrite Headers` 会把 `CF-Connecting-IP` 改成伪 IPv4，真实 IPv6 留在 `CF-Connecting-IPv6`，与本示例读取 `CF-Connecting-IP` 的方式不同。若 `client_ip` 已是真实 IPv6，说明该请求的访客地址没有被替换成伪 IPv4。

生成脚本会分别拉取并检查 Cloudflare IPv4、IPv6 两份地址段；任一类下载失败或为空，都不会覆盖原文件。不要把 IPv6 访客地址段加入 `trusted_proxies`：可信列表属于连接到 Caddy 的代理，而不是访客。

参考：[Cloudflare 访客 IP 请求头及 Pseudo IPv4](https://developers.cloudflare.com/fundamentals/reference/http-headers/#cf-connecting-ip)。

## 运行错误日志

顶层全局块中的 `log errors` 写入容器内 `/data/logs/error.json`，通过现有目录挂载对应宿主机 `/var/log/caddy/error.json`。它不使用站点请求的 `log_name`，也不需要 `no_hostname`。

`level ERROR` 筛选的是日志级别；`exclude http.log.access` 排除访问日志，避免把 HTTP 5xx 的访问记录也复制进来。上游正常返回的 401、403、429 或 500 响应仍记录在 `access.json`，符合登录匹配器的请求还会进入 `login.json`；这些状态码本身不保证产生 Caddy 运行错误。LibreChat 应用内部的错误详情需要查看其自身日志。

错误日志沿用访问日志的权限与轮转设置，并在结构化 `request.uri` 中把 `token` 查询参数替换为 `REDACTED`，删除 `request.headers`。这些过滤只影响日志输出，不改写转发请求。

```bash
sudo tail -n 20 /var/log/caddy/error.json |
  jq -c '{ts, level, logger, msg, status, uri: .request.uri}'
```

配置加载之前的启动或解析错误仍需从 `docker compose logs caddy` 查看。全局日志配置说明见 [Caddy 官方文档](https://caddyserver.com/docs/caddyfile/options#log)。

## 更新静态地址段

Cloudflare 很少调整地址段，`caddy-cloudflare-ip` 也会自动补充新增的地址段。以下命令在实际部署的 `compose.yaml`（或 `docker-compose.yml`）所在目录执行，假定服务名为 `caddy`、脚本在当前目录，且启动前已生成 `cloudflare-ranges.caddy`。若启动时使用了 Compose 的 `-f` / `-p` 参数，维护时也要使用相同参数。

原命令与对应的 Docker Compose 命令如下。两条简写都要求生成后容器能读取新文件，例如挂载配置文件所在目录；当前单文件挂载请使用下方的完整步骤。

```bash
# 按实际容器名 caddy 执行
./gen-cloudflare-ranges.sh && docker exec caddy /usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile

# Docker Compose：按服务名 caddy 执行，无须设置 container_name
./gen-cloudflare-ranges.sh && docker compose exec -T caddy /usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

对于 `./cloudflare-ranges.caddy:/etc/caddy/cloudflare-ranges.caddy:ro` 这样的**单文件挂载**，不能直接依赖 `./gen-cloudflare-ranges.sh && ... reload`：脚本用 `mv` 替换输出文件，运行中的容器可能仍挂载旧文件。先生成到另一个文件，再将内容写回已挂载文件，保留它的 inode，最后验证并重载：

```bash
./gen-cloudflare-ranges.sh ./cloudflare-ranges.caddy.next &&
cat ./cloudflare-ranges.caddy.next > ./cloudflare-ranges.caddy &&
rm ./cloudflare-ranges.caddy.next &&
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/caddy caddy validate \
  --config /etc/caddy/Caddyfile --adapter caddyfile &&
docker compose exec -T caddy /usr/local/bin/caddy reload \
  --config /etc/caddy/Caddyfile --adapter caddyfile
```

生成脚本下载或地址段检查失败时，不会执行写回及重载；Caddy 配置验证失败时，不会执行重载。`:ro` 只限制容器写入，宿主机仍可更新文件内容。写回与维护操作应串行执行。

`docker compose exec` 中的 `caddy` 是 **Compose 服务名**，适用于没有设置 `container_name` 的部署；`docker exec caddy` 中的 `caddy` 则必须是实际容器名。Compose 部署使用前者即可，不需要固定容器名。

## Fail2ban IPv4/IPv6 过滤与验证

直接安装随示例提供的文件，避免复制长正则时意外换行：

```bash
sudo install -m 0644 fail2ban/filter.d/caddy-librechat.conf \
  /etc/fail2ban/filter.d/caddy-librechat.conf
```

该规则使用 Fail2ban 的 `<ADDR>` 提取 `request.client_ip`，同时支持 IPv4、完整 IPv6 和压缩 IPv6；不是只匹配数字和点的 IPv4 正则。它还要求 `client_ip` 不等于 `remote_ip`，并限定日志来源为 `http.log.access.login`、方法为 POST、路径为 `/api/auth/login`、状态为 **401、404 或 429**。登录路径可带查询参数；根据实际部署调整失败状态码及自定义登录路径。

LibreChat 的密码错误和用户不存在并不一定返回 401：[`localStrategy.js`](https://github.com/danny-avila/LibreChat/blob/main/api/strategies/localStrategy.js) 在这两种情况下返回无用户的认证结果，[`requireLocalAuth.js`](https://github.com/danny-avila/LibreChat/blob/main/api/server/middleware/requireLocalAuth.js) 将它转换成 **404**。因此本规则把该登录接口的 404 计为失败；它不会把其它路径的 404 一起计入。若登录路由本身未配置正确，也可能返回 404，需先确认该接口能正常登录。

确认 `/etc/fail2ban/fail2ban.local` 的现有配置中允许 IPv6（合并以下设置，不要覆盖其它配置）：

```ini
[Definition]
allowipv6 = yes
```

对应 jail 设置 `filter = caddy-librechat`、`usedns = no`，并把 `logpath` 指向实际宿主机日志路径。没有原生公网 IPv6 的源站也可能收到 IPv6 访客地址，不应因此禁用 IPv6 日志识别。参考：[Fail2ban IPv6 设置](https://github.com/fail2ban/fail2ban/blob/1.1.0/config/fail2ban.conf)、[`<ADDR>` 定义](https://github.com/fail2ban/fail2ban/blob/1.1.0/fail2ban/server/failregex.py)。

只读验证日志匹配：

```bash
sudo fail2ban-regex --usedns=no "$CADDY_LOGIN_LOG" \
  /etc/fail2ban/filter.d/caddy-librechat.conf --print-all-matched
```

例如 `POST /api/auth/refresh`、状态 200 的记录应产生 **0 次登录失败匹配**，不论访客使用 IPv4 还是 IPv6。只有相应登录失败记录才会命中。重置密码请求虽然也进入认证日志，默认不会触发账号密码登录失败规则；不要把打开重置页面的 200 响应计为失败。

### 多次密码错误却没有匹配

一次实际排查的 23 条记录中，6 条是 `POST /api/auth/login` 返回 404，1 条是该接口返回 200；另外 15 条是 refresh（13 条 200、2 条 401），1 条是 logout 返回 200。旧规则只接受登录接口的 401/429，因此得到 **0 matched、23 missed**；补上 404 后，这批日志应得到 **6 matched、17 missed**。refresh 的两条 401 仍不统计，不能把会话刷新失败当成密码猜测。

该次输出的 `Date template hits: [23] "ts":{Epoch}` 表明 23 条记录的时间戳均已识别，原因是状态码漏配。404 记录里 `client_ip` 与 Cloudflare 的 `remote_ip` 不同，访客地址也已提取成功；无需为了这个问题扩大可信代理范围。

先只查看登录接口的状态和地址，不输出请求头、Cookie 或请求体：

```bash
sudo jq -c '
  select(.request.method == "POST" and
    (.request.uri | split("?")[0]) == "/api/auth/login") |
  {ts, status} + (.request | {remote_ip, client_ip, method})
' "$CADDY_LOGIN_LOG"
```

重新安装上面的 filter 文件后，再运行 `fail2ban-regex` 确认命中。现有 `login.json` 中历史 refresh/logout 记录不会被 Caddy 配置更新删除；它们保持 missed 是预期结果。若新产生的日志仍包含这些路径，检查远程 Caddyfile 是否已更新，以及 `LOGIN_PATH` 是否仍覆盖为 `/api/auth/*`。

最后让运行中的 jail 重新加载规则。先查看实际 jail 名称，再修改下面的 `CADDY_JAIL`；filter 文件名不一定等于 jail 名：

```bash
sudo fail2ban-client status
CADDY_JAIL=caddy-librechat  # 改成上一步列出的实际 jail 名
sudo fail2ban-client -t && sudo fail2ban-client reload "$CADDY_JAIL"
sudo fail2ban-client get "$CADDY_JAIL" failregex
sudo fail2ban-client status "$CADDY_JAIL"
```

重新加载后可进行新的失败登录验证。`fail2ban-regex` 对历史日志匹配成功，不代表 jail 会追溯封禁所有旧失败；实际处理还受日志读取位置、`findtime`、`maxretry` 和 `ignoreip` 等设置影响。仅修改本次 filter 无须重载 Caddy。

### 检测与实际封禁

本示例提供日志与 filter，不会自动创建或启用 jail/action。`fail2ban-regex` 成功仅证明检测；还要检查现有 jail 使用的实际封禁动作是否支持 IPv6。

Cloudflare 代理后的 TCP 源地址是 Cloudflare 边缘节点。在源站用 iptables/nftables 封禁提取出的访客 IP，通常拦不住仍经 Cloudflare 到达的请求，IPv4 和 IPv6 都一样。应在 Cloudflare 或能使用已验证 `client_ip` 的应用层执行封禁；不能改成封禁 `remote_ip`，否则会封 Cloudflare 边缘节点。

Fail2ban 官方 `cloudflare` action 含 `[Init?family=inet6]`，IPv6 使用 `cftarget = ip6`，IPv4 使用 `ip`。如果沿用该动作，检查所装版本是否有这个分支；自己的 Cloudflare 动作也必须区分正确的 API target，不能将所有地址硬编码为 `ip`。现有示例没有配置 Cloudflare 凭证或执行真实封禁。参考：[官方 Cloudflare action](https://github.com/fail2ban/fail2ban/blob/1.1.0/config/action.d/cloudflare.conf)。

修改 Fail2ban 配置后，先 `sudo fail2ban-client -t`，再按现有服务流程重启并验证对应 jail 状态、Cloudflare 规则以及来自被封访客的实际请求。不要将服务已运行或 filter 命中当作封禁生效的证明。

## 更新运行中的 Caddy

修改的是实际部署目录中挂载到容器的 `Caddyfile`；仓库文件变化不会自动更新远程部署。在 Compose 文件所在目录执行，示例服务名为 `caddy`，实际部署若叫 `httpd`，请替换服务名。确认运行中的容器能读到新文件后，验证成功才重载：

```bash
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/caddy caddy validate \
  --config /etc/caddy/Caddyfile --adapter caddyfile &&
docker compose exec -T caddy /usr/local/bin/caddy reload \
  --config /etc/caddy/Caddyfile --adapter caddyfile
```

`--entrypoint /usr/local/bin/caddy` 显式选择 Caddy 程序，兼容运行版和 `latest-dev` 等开发版镜像，不依赖基础镜像的默认入口。`run --rm --no-deps` 创建临时验证容器，继承服务的挂载和环境变量，结束后删除，且不会因为 `depends_on: api` 启动上游；`exec -T` 在运行中的服务容器内执行，关闭 TTY 后也可用于脚本。命令说明见 Docker 官方 [`run`](https://docs.docker.com/reference/cli/docker/compose/run/) / [`exec`](https://docs.docker.com/reference/cli/docker/compose/exec/) 文档。

若单文件挂载的 `Caddyfile` 被编辑器替换，或已直接执行生成脚本替换了 `cloudflare-ranges.caddy`，可在验证成功后重新创建 Caddy 容器，让挂载重新指向当前宿主机文件：

```bash
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/caddy caddy validate \
  --config /etc/caddy/Caddyfile --adapter caddyfile &&
docker compose up -d --no-deps --force-recreate caddy
```

重新创建会停止旧 Caddy 容器并启动新容器，有短暂中断；只处理 `caddy` 服务，保留现有命名卷。它也适用于修改了 Compose 中镜像、挂载或环境变量的情况，单独 reload 不会应用这些 Compose 变化。

若改变的是同一日志文件的权限或轮转设置，Caddy 要重启才能应用；修改匹配器或日志格式可以 reload。先完成配置验证，再按部署维护流程决定是否重启。

## 本地回归验证

在仓库根目录执行以下测试；生成脚本用模拟下载，不访问 Cloudflare，也不修改防火墙：

```bash
python3 tests/test_cloudflare_logging.py
```

安装了 jq 时验证手册命令；安装了可被 Python 导入的 Fail2ban 时，通过真实 `Filter.processLine` 验证时间戳解析、IPv4/IPv6 `<ADDR>` 提取，以及上述 23 条日志的状态/路径分布（6 条命中）。缺少对应依赖的测试会显示 skipped。也可以用官方源码运行 Fail2ban 部分：

```bash
FAIL2BAN_SOURCE=/path/to/fail2ban python3 tests/test_cloudflare_logging.py
```

## 注意

- Docker 发布的端口不经过宿主机防火墙的 INPUT 链。"源站只允许 Cloudflare 访问"要用云防火墙/安全组，或者 `DOCKER-USER` 链来实现。
- 只对橙云（代理）的主机名生效；灰云（仅 DNS）的流量不经过 Cloudflare。
