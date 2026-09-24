# Prism

[English](#english) | [中文](#中文)

---

## English

Prism is a single-port proxy router and control plane. It imports proxy nodes
from subscriptions, verifies their real egress, routes client traffic through
per-platform rules with sticky leases, and exposes a management API plus an
embedded web UI on the same listener.

### Features

- **Node pool from subscriptions**: remote URLs and pasted content, parsed from
  share links, Clash, sing-box and Surge formats; deduplicated by node hash.
- **Intelligent routing**: platform rules (regex, region, subscription scope),
  P2C scheduling weighted by measured latency, sticky leases with TTL, and an
  optional scheduled rotation interval per platform.
- **Health and egress verification**: latency probes, passive TLS handshake
  sampling, egress IP/country detection through the node itself, GeoIP database
  updates, circuit breaking and recovery.
- **Single-port ingress**: the primary listener serves the HTTP forward proxy,
  CONNECT, SOCKS5, the URL reverse proxy, the management API and the web UI.
  An optional independent management listener (`PRISM_ADMIN_LISTEN`) serves
  `/ui`, `/api` and `/healthz` only.
- **Metrics and logs**: realtime, historical and snapshot metrics, bounded
  request logs with an SQLite rolling store.
- **Online backup and verified restore**: `prism backup` / `prism restore`.

### Requirements

- Go 1.26 or later (`go.mod` declares `go 1.26.0`).
- Linux with systemd for the service installer (optional).
- Node.js and npm to build the web UI (`make web`).

### Quick start

```bash
# 1. Clone
git clone https://github.com/mycatxl/Prism.git
cd Prism

# 2. Build (web UI + backend). "make backend" skips the UI build.
make build

# 3. Generate .env with fresh admin and proxy tokens (mode 0600)
./bin/prism init

# 4. Install and start the systemd service (creates the prism user,
#    keeps an existing .env, never stops another process)
sudo ./scripts/deploy.sh

# 5. Open the web UI and authenticate with PRISM_ADMIN_TOKEN from .env
#    http://127.0.0.1:2260/ui/
```

`sudo ./scripts/deploy.sh --listen 127.0.0.1 --port 2260 --state-dir /var/lib/prism/state`
rewrites only the matching `PRISM_*` keys after backing the file up.
`--no-service` prepares `.env` and the data directories without installing a
unit, and `--dry-run` prints what would happen without changing anything.
Without a compiled `bin/prism` the script stops and tells you to run
`make build`.

No systemd? Run `./bin/prism run` from the directory that contains `.env`.

Docker instead of systemd:

```bash
cp docker-compose.yml.example docker-compose.yml

# The example passes ${PRISM_ADMIN_TOKEN} and ${PRISM_PROXY_TOKEN} into the
# container and reads them from ./.env — the file `prism init` writes.
./bin/prism init            # or set both tokens in .env by hand

docker compose up -d        # then open http://<host>:2260/ui/
```

The example publishes `2260:2260` and keeps its databases and logs in three
named volumes. Requirements, volume paths, the container user, health checks and
upgrades are in the [Docker section of docs/deployment.md](docs/deployment.md#docker).

### Managing a running instance

```bash
sudo systemctl status prism     # state
sudo journalctl -u prism -f     # logs
sudo systemctl restart prism    # restart
```

### Configuration

Configuration comes from `./.env` (loaded first, never overriding the process
environment) or from real environment variables. `./bin/prism check-config`
prints the effective values with every secret masked.

| Variable | Default | Purpose |
|---|---|---|
| `PRISM_ADMIN_TOKEN` | — (required) | Bearer token for `/api/v1/*` |
| `PRISM_PROXY_TOKEN` | — (required) | Token in proxy credentials and reverse-proxy paths |
| `PRISM_AUTH_VERSION` | `v1` | Identity format: `Platform.Account:Token` |
| `PRISM_LISTEN_ADDRESS` | `127.0.0.1` | Primary listener host |
| `PRISM_PORT` | `2260` | Primary listener port (UI, API, proxy, reverse proxy) |
| `PRISM_ADMIN_LISTEN` | *(disabled)* | Optional `host:port` for a management-only listener |
| `PRISM_STATE_DIR` | `./.local/state` | Databases (`state.db`, `intel.db`) |
| `PRISM_CACHE_DIR` | `./.local/cache` | Rebuildable cache (`cache.db`, GeoIP files) |
| `PRISM_LOG_DIR` | `./.local/logs` | Rolling logs |
| `PRISM_API_MAX_BODY_BYTES` | `1048576` | Request body limit for `/api/` |
| `PRISM_PROBE_CONCURRENCY` | `32` | Parallel probe workers |
| `PRISM_PROBE_TIMEOUT` | `15s` | Per-probe timeout |
| `PRISM_DEFAULT_PLATFORM_STICKY_TTL` | `168h` | Default sticky lease lifetime |
| `PRISM_GEOIP_UPDATE_SCHEDULE` | `0 7 * * *` | GeoIP update cron expression |
| `PRISM_RESOURCE_FETCH_MAX_BYTES` | `33554432` | Download ceiling after decompression |
| `PRISM_ENFORCE_STRONG_TOKENS` | `true` | Reject tokens shorter than 16 characters |
| `PRISM_ALLOW_EMPTY_ADMIN_TOKEN` / `PRISM_ALLOW_EMPTY_PROXY_TOKEN` | `false` | Explicitly disable one authentication scope. Every listener — `PRISM_LISTEN_ADDRESS` **and** `PRISM_ADMIN_LISTEN` — must then stay on loopback unless `PRISM_ALLOW_INSECURE_LISTEN=true` |
| `PRISM_QUALITY_ENABLED`, `PRISM_QUALITY_API_KEY`, `PRISM_ABUSEIPDB_API_KEY` | *(off / unset)* | Optional IP-quality providers |
| `PRISM_TRUSTED_PROXIES` | *(empty)* | CIDR list whose `X-Forwarded-For` is trusted for client-IP limiting |
| `PRISM_PROXY_AUTH_FAIL_LIMIT` | `0` | Failed proxy authentications per minute per IP. Covers the HTTP forward proxy and CONNECT (`Proxy-Authorization`), the reverse-proxy path token and the SOCKS5 username/password check; `0` disables proxy-entry limiting. `/api/*` and `/sub/{token}` keep their own limiters |
| `PRISM_DIRECT_DENY_PRIVATE` | `false` | Refuse loopback, private, link-local, CGNAT, reserved and cloud-metadata targets on every local dial path (reverse-proxy bypass, forward HTTP, CONNECT, SOCKS5). Opt-in: with the default `false` the local dial paths carry no address policy (upstream Resin behaviour) |

### Usage

All management calls need `Authorization: Bearer $PRISM_ADMIN_TOKEN`.

Create a platform (platform IDs are generated by the server):

```bash
curl -X POST http://127.0.0.1:2260/api/v1/platforms \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Platform","sticky_ttl":"1h","scheduled_rotation_enabled":true}'
```

Add nodes through a subscription (a node is never created directly):

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'
```

Refresh it and inspect the pool:

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions/<id>/actions/refresh \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" "http://127.0.0.1:2260/api/v1/nodes?limit=20"
```

Expose a second inbound port:

```bash
curl -X POST http://127.0.0.1:2260/api/v1/endpoints \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"port":2261,"enabled":true,"allow_proxy":true,"allow_http_forward":true,"allow_socks5":true,"allow_http_reverse":false,"allow_management":false}'
```

Route client traffic (proxy credentials use the layout
`Platform.Account:PRISM_PROXY_TOKEN`, with the account optional):

```bash
# HTTP forward proxy
curl -x "http://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# SOCKS5
curl --proxy "socks5h://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# URL reverse proxy: /<token>/<Platform>.<Account>/<http|https>/<host>/<path>
curl "http://127.0.0.1:2260/$PRISM_PROXY_TOKEN/Default/http/example.com/"
```

### What you can do today

These surfaces landed after the first revision of this file. Protocol-level detail, the list of
unsupported types and the known limitations are in [docs/PROTOCOLS.md](docs/PROTOCOLS.md).

**Import a subscription and let the intel batch run.** Every subscription carries an `auto_intel`
flag that defaults to true (the database default, and the value rows created by older revisions are
migrated to), so a refresh queues one intel job for the nodes it imported. It is settable per
subscription with `auto_intel` on `POST /api/v1/subscriptions` and
`PATCH /api/v1/subscriptions/{id}`, is reported back in the subscription response, and has a switch
in the subscription view of the WebUI; the batch is additionally switched off system-wide with
`intel_enabled=false`. With the default
runtime settings (`intel_enabled=true`, `intel_auto_checks=false`) the job runs
`egress → offline evidence → online evidence → via-node lookups → assessment`
(`internal/intel/jobs/jobs.go`), writes into `intel.db`, and resumes at the next step after a
restart. Turning `intel_auto_checks` on adds the unlock checks to the same job.

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'

# manual batch: by subscription, platform, filter or explicit node hashes
curl -X POST http://127.0.0.1:2260/api/v1/intel/jobs \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"kind":"full","scope":{"platform_ids":["<platform-id>"]}}'

# live progress (Server-Sent Events)
curl -N -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/intel/jobs/<job-id>/events
```

Per-node results are on `GET /api/v1/intel/nodes/{hash}`, per-IP evidence on
`GET /api/v1/intel/ip/{ip}`, and `POST /api/v1/intel/jobs/{id}/actions/retry-failed` retries the
failures of a finished job.

**Every drop is explained.** A subscription refresh keeps a parse report: nodes the parser refuses
are grouped by reason with a count and a bounded sample of names. The grouped summary is part of
every subscription response (`parse_report`) and the exact list of the last parse (at most 500
records, plus a `skipped_overflow` count) is on
`GET /api/v1/subscriptions/{id}/parse-report`; the WebUI subscription view shows both. Reasons are
the stable codes from `internal/node/reasons.go` (`ENGINE_NOT_BUILT`, `UNSUPPORTED_PROTOCOL`,
`INVALID`, …) and are listed per protocol in [docs/PROTOCOLS.md](docs/PROTOCOLS.md) §7.

**Purity score.** Purity is `100 - provider risk` (`internal/quality/model.go`, `PurityScore`).
It is a labelled display inversion, not a calibrated probability or a second provider score, and a
node without evidence stays `unknown`/`pending`. Bands: `excellent` (≥95), `clean` (≥90), `fair`
(≥80), `mixed` (≥60), `poor` (<60).

```bash
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  "http://127.0.0.1:2260/api/v1/nodes?purity_band=clean&purity_min=90&limit=50"
```

The same filter vocabulary — `purity_band`, `purity_min`, `purity_max`, `ip_type`, `verdict`,
`confidence_min`, `native`, `asn`, `country`, `checks` — also drives export profiles and the
platform quality policy, which is fail-closed: a policy that requires a score does not admit a node
without an assessment.

**Export formats and `/sub/<token>` subscriptions.** `GET /api/v1/nodes/export` renders `singbox`,
`mihomo`, `v2rayn`, `uri`, `csv` and `json` (`internal/export/types.go`); the response headers
`X-Prism-Export-Exported` / `-Skipped` / `-Truncated` report how many nodes were written, skipped
and truncated (at most 5000 nodes per request). An export profile (`POST /api/v1/export-profiles`)
stores format, name template and a node filter, and mints a public URL:

```bash
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" "http://127.0.0.1:2260/api/v1/nodes/export?format=uri&purity_min=90"
curl -X POST http://127.0.0.1:2260/api/v1/export-profiles \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"clean-uri","format":"uri","filter":{"purity_min":90}}'
# the response carries subscription_url: http://<host>:2260/sub/<token>
curl "http://127.0.0.1:2260/sub/<token>"           # client-side, no admin token
curl -X POST http://127.0.0.1:2260/api/v1/export-profiles/<id>/actions/rotate-token \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"    # the old URL stops working
```

A disabled profile answers `404`. The public endpoint stores only the token's sha256, and it is
served by the management handler on every listener that carries the management surface — the
primary listener (`PRISM_LISTEN_ADDRESS` / `PRISM_PORT`) and the independent management listener
(`PRISM_ADMIN_LISTEN`) — gated by that endpoint's `allow_management` flag, exactly like `/api/*`.
It is rate-limited on its own (60 requests per minute per token, 120 per minute per client IP,
`429` with `Retry-After`) and is independent of `PRISM_PROXY_AUTH_FAIL_LIMIT`
(`internal/api/handler_subscription_token.go`, `internal/api/export_token.go`).

**Data sources and unlock checks.** The UI page *Data Sources & Unlock Checks* (`/intel-settings`)
and the API (`GET|PATCH /api/v1/intel/providers`,
`POST /api/v1/intel/providers/{id}/actions/refresh`, `GET|PATCH /api/v1/intel/checks`) show each
source's enabled/runnable state, key presence, daily budget and offline database age. Offline
databases are not bundled: DB-IP Lite, MaxMind GeoLite2 and IPinfo Lite need one download into
`$PRISM_CACHE_DIR/geo` before they contribute evidence — until then the source answers
`PROVIDER_UNAVAILABLE` (`… database is not installed`) and no ASN/geo evidence is recorded.
Built-in unlock checks: `chatgpt`, `claude`, `gemini`, `google_captcha`, `netflix`, `smtp25`,
`tiktok`, `youtube_premium`.

### Key endpoints

- `GET /healthz` — liveness (no token required).
- `GET /ui/` — web interface; if the frontend was not built it answers `503`.
- `GET /api/v1/system/info|config|config/env`, `PATCH /api/v1/system/config`.
- `GET|POST /api/v1/platforms`, `GET|PATCH|DELETE /api/v1/platforms/{id}`,
  `POST /api/v1/platforms/{id}/actions/reset-to-default`,
  `POST /api/v1/platforms/{id}/actions/rebuild-routable-view`.
- `GET|POST /api/v1/subscriptions`,
  `GET|PATCH|DELETE /api/v1/subscriptions/{id}`,
  `POST /api/v1/subscriptions/{id}/actions/refresh`,
  `POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes`.
- `GET /api/v1/nodes`, `GET /api/v1/nodes/{hash}`,
  `POST /api/v1/nodes/{hash}/actions/probe-egress|probe-latency|probe-quality|review-ippure`.
- `GET|POST /api/v1/endpoints`, `GET|PATCH|DELETE /api/v1/endpoints/{id}`.
- `GET|DELETE /api/v1/platforms/{id}/leases`,
  `GET|DELETE /api/v1/platforms/{id}/leases/{account}`,
  `GET /api/v1/platforms/{id}/ip-load`.
- `GET /api/v1/quality/status|assessments|ip/{ip}`,
  `POST /api/v1/quality/ip/{ip}/actions/probe`.
- `GET|PUT|DELETE /api/v1/account-header-rules[/{prefix...}]`,
  `POST /api/v1/account-header-rules:resolve`.
- `GET /api/v1/geoip/status|lookup`, `POST /api/v1/geoip/actions/update-now`.
- `GET /api/v1/request-logs[/{log_id}[/payloads]]`.
- `GET /api/v1/metrics/realtime/{throughput,connections,leases}`,
  `GET /api/v1/metrics/history/{traffic,requests,access-latency,probes,node-pool,lease-lifetime}`,
  `GET /api/v1/metrics/snapshots/{node-pool,platform-node-pool,node-latency-distribution}`.
- `GET|POST /api/v1/intel/jobs`, `GET /api/v1/intel/jobs/{id}[/items|/events]`,
  `POST /api/v1/intel/jobs/{id}/actions/cancel|retry-failed`, `GET /api/v1/intel/status|nodes/{hash}|ip/{ip}`.
- `GET /api/v1/intel/providers`, `PATCH /api/v1/intel/providers/{id}`,
  `POST /api/v1/intel/providers/{id}/actions/refresh|resume`, `GET /api/v1/intel/checks`,
  `PATCH /api/v1/intel/checks/{id}`.
- `GET /api/v1/nodes/export`, `GET|POST /api/v1/export-profiles`,
  `GET|PATCH|DELETE /api/v1/export-profiles/{id}`,
  `POST /api/v1/export-profiles/{id}/actions/rotate-token`.
- `GET /sub/{token}` — public, no admin token, the path token is the credential. Served by the
  management handler on any listener with the management surface (primary listener and
  `PRISM_ADMIN_LISTEN`), gated by `allow_management`; unknown token, disabled profile and an
  endpoint without management access all answer `404`. It has its own limiter (60/min per token,
  120/min per client IP, then `429`) and is not part of `PRISM_PROXY_AUTH_FAIL_LIMIT`.

There is no OpenAPI document or `/ui/docs` page in this repository yet; the
route table lives in `internal/api/server.go`.

### Backup and restore

```bash
# Snapshot state.db, cache.db and intel.db into backups/<timestamp>/
./scripts/prism-backup.sh backup

# Keep only the newest 7 snapshots
./scripts/prism-backup.sh backup --keep 7

# List them
./scripts/prism-backup.sh list

# Restore (stop the service first: restore refuses to touch a live instance)
sudo systemctl stop prism
./scripts/prism-backup.sh restore --from backups/20260924T101500Z --force
sudo systemctl start prism
```

Both subcommands delegate to `./bin/prism backup --out DIR` and
`./bin/prism restore --from DIR`: the backup is a `VACUUM INTO` snapshot of each
database plus a `manifest.json` with size and sha256 per file, and the restore
verifies that manifest before replacing anything. `.env` is never part of a
backup.

### Development

```bash
make backend          # Go binary only -> bin/prism
make backend-lite     # same tag set today; mihomo is never built (docs/ENGINE_DECISIONS.md)
make build            # web UI + backend
make test             # go test ./cmd/... ./internal/...
make verify           # vet + tests (both tag sets) + protocol matrix
make protocol-matrix  # table-driven protocol build matrix
bash scripts/smoke.sh # end-to-end smoke test against a throw-away environment
```

### Project structure

```text
Prism/
├── cmd/prism/                # entry point, subcommands, listeners, inbound mux
├── internal/
│   ├── api/                  # REST handlers, middleware; api/web/ holds the React UI
│   ├── buildinfo/            # version, commit, build time, build tags
│   ├── config/               # PRISM_* environment configuration
│   ├── geoip/                # country database and lookup
│   ├── inspection/           # quality task queue and provider adapters
│   ├── metrics/              # realtime, history and snapshot metrics
│   ├── model/                # shared domain models (platform, lease, audit, ...)
│   ├── netutil/              # downloader, retry helpers
│   ├── node/                 # node records and hashes
│   ├── outbound/             # sing-box node adapters
│   ├── platform/             # routable platform views and filters
│   ├── probe/                # latency and egress probes
│   ├── proxy/                # HTTP/SOCKS5/reverse entry points and forwarding
│   ├── quality/              # quality evidence and assessment
│   ├── requestlog/           # bounded request log storage
│   ├── routing/              # P2C, leases, scheduled rotation
│   ├── scanloop/             # periodic maintenance loops
│   ├── service/              # control-plane use cases
│   ├── state/                # SQLite state/cache engines and migrations
│   ├── subscription/         # subscription parsers (links, Clash, sing-box, Surge)
│   ├── testutil/             # shared test helpers
│   └── topology/             # subscriptions, nodes and platform relations
├── deploy/                   # backend.env.example for deployments
├── docker/                   # container entrypoint
├── docs/                     # design, deployment, security and plan documents
├── scripts/                  # deploy.sh, prism-backup.sh, smoke.sh
├── Dockerfile, docker-compose.yml.example
└── Makefile
```

### Security

Implemented and test-verified controls are documented in
[docs/SECURITY.md](docs/SECURITY.md): admin API authentication with per-IP
failure limiting (`429` plus `Retry-After`, `X-Forwarded-For` honoured only from
`PRISM_TRUSTED_PROXIES`), management-listener isolation, audit logging of
management writes with 90-day retention, verified online backups and restores,
and the optional `PRISM_DIRECT_DENY_PRIVATE` policy, which refuses loopback,
private, link-local, CGNAT, reserved and cloud-metadata targets on **every**
local dial path (reverse-proxy bypass, forward HTTP proxy, CONNECT and SOCKS5).
The same document lists the known limitations — there is no TLS listener
(terminate TLS in front of Prism), and proxy-entry failure limiting and the
direct-target policy are opt-in. With the default
`PRISM_DIRECT_DENY_PRIVATE=false` the local dial paths carry no address policy,
which is the upstream Resin behaviour: enable it before exposing the forward
proxy, CONNECT or SOCKS5 entrypoints to untrusted clients.

### License

GPL-3.0-or-later — see the `LICENSE` file for the full text.

### Contributing

1. Fork the repository.
2. Create a feature branch.
3. Run `make verify` and `bash scripts/smoke.sh` before opening a pull request.
4. Describe behaviour changes in `docs/`.

### Support

- GitHub Issues: https://github.com/mycatxl/Prism/issues
- Documentation: https://github.com/mycatxl/Prism/tree/main/docs

---

## 中文

Prism 是单端口的代理路由器与控制平面：从订阅导入节点、验证真实出口、按平台规则与会话粘性转发客户端流量，并在同一个监听端口上提供管理 API 与内置 Web 界面。

### 功能特性

- **订阅驱动的节点池**：支持远程 URL 和本地粘贴，解析分享链接、Clash、sing-box、Surge 等格式，按节点哈希去重。
- **智能路由**：平台规则（正则、地区、订阅范围）、按实测延迟加权的 P2C 调度、带 TTL 的粘性租约，以及可选的定时轮换。
- **健康与出口验证**：延迟探测、被动 TLS 握手采样、经节点自身探测出口 IP 与国家、GeoIP 数据库更新、连续失败熔断与恢复。
- **单端口接入**：主监听端口同时提供 HTTP 正向代理、CONNECT、SOCKS5、URL 反向代理、管理 API 与 Web 界面；可选的独立管理监听端口（`PRISM_ADMIN_LISTEN`）只服务 `/ui`、`/api` 和 `/healthz`。
- **指标与日志**：实时、历史与快照指标，以及有界的请求日志（SQLite 滚动存储）。
- **在线备份与可校验恢复**：`prism backup` / `prism restore`。

### 系统要求

- Go 1.26 或更高版本（`go.mod` 声明 `go 1.26.0`）。
- 使用服务安装脚本需要带 systemd 的 Linux（可选）。
- 构建前端需要 Node.js 与 npm（`make web`）。

### 快速开始

```bash
# 1. 克隆仓库
git clone https://github.com/mycatxl/Prism.git
cd Prism

# 2. 构建（前端 + 后端）；只构建后端可用 make backend
make build

# 3. 生成带随机令牌的 .env（权限 0600）
./bin/prism init

# 4. 安装并启动 systemd 服务（创建 prism 用户，保留已有 .env，绝不结束其他进程）
sudo ./scripts/deploy.sh

# 5. 打开 Web 界面，使用 .env 中的 PRISM_ADMIN_TOKEN 登录
#    http://127.0.0.1:2260/ui/
```

`sudo ./scripts/deploy.sh --listen 127.0.0.1 --port 2260 --state-dir /var/lib/prism/state`
只会改写对应的 `PRISM_*` 键，并在修改前备份原文件。`--no-service` 只准备
`.env` 和数据目录，不安装 systemd 单元；`--dry-run` 只打印将要执行的动作。
若缺少已编译的 `bin/prism`，脚本会提示先执行 `make build`。

不使用 systemd 时，在包含 `.env` 的目录里直接运行 `./bin/prism run`。

改用 Docker 时：

```bash
cp docker-compose.yml.example docker-compose.yml

# 示例会把 ${PRISM_ADMIN_TOKEN} 和 ${PRISM_PROXY_TOKEN} 传给容器，
# 并从 ./.env 读取它们——也就是 `prism init` 写出的那个文件。
./bin/prism init            # 或者手动在 .env 里填两个令牌

docker compose up -d        # 然后访问 http://<host>:2260/ui/
```

示例映射 `2260:2260`，数据库与日志放在三个命名卷里。环境要求、卷路径、容器
用户、健康检查与升级方式见
[docs/deployment.md 的 Docker 章节](docs/deployment.md#docker)。

### 服务管理

```bash
sudo systemctl status prism     # 查看状态
sudo journalctl -u prism -f     # 查看日志
sudo systemctl restart prism    # 重启
```

### 配置说明

配置来自 `./.env`（优先加载，但不覆盖进程环境变量）或真实的环境变量。
`./bin/prism check-config` 会打印生效配置，所有密钥以 `***` 显示。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PRISM_ADMIN_TOKEN` | —（必填） | `/api/v1/*` 的 Bearer 令牌 |
| `PRISM_PROXY_TOKEN` | —（必填） | 代理凭据与反向代理路径中的令牌 |
| `PRISM_AUTH_VERSION` | `v1` | 身份格式：`Platform.Account:Token` |
| `PRISM_LISTEN_ADDRESS` | `127.0.0.1` | 主监听地址 |
| `PRISM_PORT` | `2260` | 主监听端口（UI、API、代理、反向代理） |
| `PRISM_ADMIN_LISTEN` | *（关闭）* | 可选的独立管理监听 `host:port` |
| `PRISM_STATE_DIR` | `./.local/state` | 数据库目录（`state.db`、`intel.db`） |
| `PRISM_CACHE_DIR` | `./.local/cache` | 可重建缓存（`cache.db`、GeoIP 文件） |
| `PRISM_LOG_DIR` | `./.local/logs` | 滚动日志目录 |
| `PRISM_API_MAX_BODY_BYTES` | `1048576` | `/api/` 请求体上限 |
| `PRISM_PROBE_CONCURRENCY` | `32` | 探测并发数 |
| `PRISM_PROBE_TIMEOUT` | `15s` | 单次探测超时 |
| `PRISM_DEFAULT_PLATFORM_STICKY_TTL` | `168h` | 默认粘性租约时长 |
| `PRISM_GEOIP_UPDATE_SCHEDULE` | `0 7 * * *` | GeoIP 更新 cron 表达式 |
| `PRISM_RESOURCE_FETCH_MAX_BYTES` | `33554432` | 解压后的下载体积上限 |
| `PRISM_ENFORCE_STRONG_TOKENS` | `true` | 拒绝少于 16 个字符的令牌 |
| `PRISM_ALLOW_EMPTY_ADMIN_TOKEN` / `PRISM_ALLOW_EMPTY_PROXY_TOKEN` | `false` | 显式关闭某一类鉴权。此时**每一个**监听地址（`PRISM_LISTEN_ADDRESS` 与 `PRISM_ADMIN_LISTEN`）都必须保持回环，除非设置 `PRISM_ALLOW_INSECURE_LISTEN=true` |
| `PRISM_QUALITY_ENABLED`、`PRISM_QUALITY_API_KEY`、`PRISM_ABUSEIPDB_API_KEY` | *（关闭 / 未设置）* | 可选的 IP 质量数据源 |
| `PRISM_TRUSTED_PROXIES` | *（空）* | 允许其 `X-Forwarded-For` 作为客户端 IP 的代理 CIDR 列表 |
| `PRISM_PROXY_AUTH_FAIL_LIMIT` | `0` | 每个 IP 每分钟允许的代理鉴权失败次数。覆盖正向 HTTP 代理与 CONNECT（`Proxy-Authorization`）、反代路径令牌以及 SOCKS5 用户名/密码校验；`0` 表示不在代理入口限流。`/api/*` 与 `/sub/{token}` 各自有独立限流 |
| `PRISM_DIRECT_DENY_PRIVATE` | `false` | 在所有本机直连路径（反代 bypass、正向 HTTP、CONNECT、SOCKS5）上拒绝回环、私网、链路本地、CGNAT、保留地址与云元数据地址。默认关闭；关闭时本机直连路径不做地址限制（与 Resin 一致） |

### 使用示例

所有管理接口都需要 `Authorization: Bearer $PRISM_ADMIN_TOKEN`。

创建平台（平台 ID 由服务端生成）：

```bash
curl -X POST http://127.0.0.1:2260/api/v1/platforms \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Platform","sticky_ttl":"1h","scheduled_rotation_enabled":true}'
```

节点通过订阅加入（没有直接创建节点的接口）：

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'
```

刷新订阅并查看节点池：

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions/<id>/actions/refresh \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" "http://127.0.0.1:2260/api/v1/nodes?limit=20"
```

新增接入端口：

```bash
curl -X POST http://127.0.0.1:2260/api/v1/endpoints \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"port":2261,"enabled":true,"allow_proxy":true,"allow_http_forward":true,"allow_socks5":true,"allow_http_reverse":false,"allow_management":false}'
```

客户端接入（代理凭据格式为 `Platform.Account:PRISM_PROXY_TOKEN`，账号可省略）：

```bash
# HTTP 正向代理
curl -x "http://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# SOCKS5
curl --proxy "socks5h://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# URL 反向代理：/<token>/<Platform>.<Account>/<http|https>/<host>/<path>
curl "http://127.0.0.1:2260/$PRISM_PROXY_TOKEN/Default/http/example.com/"
```

### 现在就能用的功能

以下能力是在本文件首次改写之后落地的。协议级细节、不支持的类型清单与已知限制见
[docs/PROTOCOLS.md](docs/PROTOCOLS.md)。

**导入订阅后自动跑 Intel 批量检测。** 每个订阅都有一个 `auto_intel` 标记，默认值为 true
（数据库默认值，旧版本建立的订阅在迁移后也为 true），因此每次刷新会为本次导入的节点排队一个
Intel 任务。该标记可以在 `POST /api/v1/subscriptions` 与 `PATCH /api/v1/subscriptions/{id}` 上用
`auto_intel` 逐订阅设置，订阅响应会原样返回该值，WebUI 的订阅页也提供了开关；批量检测的系统级
开关是 `intel_enabled`。
在默认运行时设置下（`intel_enabled=true`、`intel_auto_checks=false`），任务按
`出口探测 → 离线库 → 在线数据源 → 经节点查询 → 评估` 执行
（`internal/intel/jobs/jobs.go`），结果写入 `intel.db`，中途重启后从下一步继续；
打开 `intel_auto_checks` 会把解锁检测并入同一个任务。

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'

# 手动批量：按订阅、平台、筛选条件或显式节点哈希
curl -X POST http://127.0.0.1:2260/api/v1/intel/jobs \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"kind":"full","scope":{"platform_ids":["<platform-id>"]}}'

# 实时进度（SSE）
curl -N -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/intel/jobs/<job-id>/events
```

单节点结果见 `GET /api/v1/intel/nodes/{hash}`，单 IP 证据见 `GET /api/v1/intel/ip/{ip}`；
`POST /api/v1/intel/jobs/{id}/actions/retry-failed` 可重跑失败项。

**每个被丢弃的节点都有原因。** 每次订阅刷新都会保留解析报告：解析器拒绝的节点按原因分组，给出
数量与有限的名称样例。分组摘要随每个订阅响应返回（`parse_report`），最近一次解析的完整清单
（最多 500 条，超出部分计入 `skipped_overflow`）由
`GET /api/v1/subscriptions/{id}/parse-report` 提供，WebUI 的订阅页会同时展示两者。原因码取自
`internal/node/reasons.go`（`ENGINE_NOT_BUILT`、`UNSUPPORTED_PROTOCOL`、`INVALID` 等），
各协议对应的原因见 [docs/PROTOCOLS.md](docs/PROTOCOLS.md) §7。

**纯净度分数。** 纯净度 = `100 - 数据源风险分`（`internal/quality/model.go`、`PurityScore`）。
它只是一个明确标注的展示用取反值，不是概率，也不是第二个数据源评分；没有证据的节点保持
`unknown`/`pending`。分档：`excellent`（≥95）、`clean`（≥90）、`fair`（≥80）、`mixed`（≥60）、
`poor`（<60）。

```bash
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  "http://127.0.0.1:2260/api/v1/nodes?purity_band=clean&purity_min=90&limit=50"
```

同一套筛选词（`purity_band`、`purity_min`、`purity_max`、`ip_type`、`verdict`、`confidence_min`、
`native`、`asn`、`country`、`checks`）同时用于导出配置和平台质量准入；准入是 fail-closed 的：
要求分数的策略不会放过没有评估结果的节点。

**导出格式与 `/sub/<token>` 订阅。** `GET /api/v1/nodes/export` 支持 `singbox`、`mihomo`、
`v2rayn`、`uri`、`csv`、`json`（`internal/export/types.go`），响应头
`X-Prism-Export-Exported/Skipped/Truncated` 说明导出、跳过与截断数量（单次最多 5000 个节点）。
导出配置（`POST /api/v1/export-profiles`）保存格式、命名模板与节点筛选，并生成公开订阅链接：

```bash
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" "http://127.0.0.1:2260/api/v1/nodes/export?format=uri&purity_min=90"
curl -X POST http://127.0.0.1:2260/api/v1/export-profiles \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"clean-uri","format":"uri","filter":{"purity_min":90}}'
# 响应中的 subscription_url: http://<host>:2260/sub/<token>
curl "http://127.0.0.1:2260/sub/<token>"           # 客户端使用，无需管理令牌
curl -X POST http://127.0.0.1:2260/api/v1/export-profiles/<id>/actions/rotate-token \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"    # 旧链接立即失效
```

配置被停用后该链接返回 404。公开入口只保存链接令牌的 sha256，由管理面处理函数在**所有**承载管理面的监听地址上提供（主监听 `PRISM_LISTEN_ADDRESS` / `PRISM_PORT`，以及独立的 `PRISM_ADMIN_LISTEN`），并与其他管理路由一样受该接入点 `allow_management` 开关约束。它有自己的限流（每令牌 60 次/分钟、每客户端 IP 120 次/分钟，超限返回 `429` 与 `Retry-After`），与 `PRISM_PROXY_AUTH_FAIL_LIMIT` 相互独立
（`internal/api/handler_subscription_token.go`、`internal/api/export_token.go`）。

**数据源与解锁检测设置。** 界面页面「数据源与解锁检测」（`/intel-settings`）与接口
（`GET|PATCH /api/v1/intel/providers`、`POST /api/v1/intel/providers/{id}/actions/refresh`、
`GET|PATCH /api/v1/intel/checks`）展示每个数据源的启用/可运行状态、密钥是否配置、每日额度
与离线库的新鲜度。离线库不随包发布：DB-IP Lite、MaxMind GeoLite2 与 IPinfo Lite 需要先下载到
`$PRISM_CACHE_DIR/geo` 才会提供证据，在此之前数据源返回 `PROVIDER_UNAVAILABLE`
（`… database is not installed`），不会记录 ASN/地区证据。内置解锁检测：`chatgpt`、`claude`、
`gemini`、`google_captcha`、`netflix`、`smtp25`、`tiktok`、`youtube_premium`。

### 主要接口

- `GET /healthz` — 存活检查（无需令牌）。
- `GET /ui/` — Web 界面；未构建前端时返回 `503`。
- `GET /api/v1/system/info|config|config/env`、`PATCH /api/v1/system/config`。
- `GET|POST /api/v1/platforms`、`GET|PATCH|DELETE /api/v1/platforms/{id}`、
  `POST /api/v1/platforms/{id}/actions/reset-to-default`、
  `POST /api/v1/platforms/{id}/actions/rebuild-routable-view`。
- `GET|POST /api/v1/subscriptions`、`GET|PATCH|DELETE /api/v1/subscriptions/{id}`、
  `POST /api/v1/subscriptions/{id}/actions/refresh`、
  `POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes`。
- `GET /api/v1/nodes`、`GET /api/v1/nodes/{hash}`、
  `POST /api/v1/nodes/{hash}/actions/probe-egress|probe-latency|probe-quality|review-ippure`。
- `GET|POST /api/v1/endpoints`、`GET|PATCH|DELETE /api/v1/endpoints/{id}`。
- `GET|DELETE /api/v1/platforms/{id}/leases`、
  `GET|DELETE /api/v1/platforms/{id}/leases/{account}`、
  `GET /api/v1/platforms/{id}/ip-load`。
- `GET /api/v1/quality/status|assessments|ip/{ip}`、
  `POST /api/v1/quality/ip/{ip}/actions/probe`。
- `GET|PUT|DELETE /api/v1/account-header-rules[/{prefix...}]`、
  `POST /api/v1/account-header-rules:resolve`。
- `GET /api/v1/geoip/status|lookup`、`POST /api/v1/geoip/actions/update-now`。
- `GET /api/v1/request-logs[/{log_id}[/payloads]]`。
- `GET /api/v1/metrics/realtime/{throughput,connections,leases}`、
  `GET /api/v1/metrics/history/{traffic,requests,access-latency,probes,node-pool,lease-lifetime}`、
  `GET /api/v1/metrics/snapshots/{node-pool,platform-node-pool,node-latency-distribution}`。
- `GET|POST /api/v1/intel/jobs`、`GET /api/v1/intel/jobs/{id}[/items|/events]`、
  `POST /api/v1/intel/jobs/{id}/actions/cancel|retry-failed`、`GET /api/v1/intel/status|nodes/{hash}|ip/{ip}`。
- `GET /api/v1/intel/providers`、`PATCH /api/v1/intel/providers/{id}`、
  `POST /api/v1/intel/providers/{id}/actions/refresh|resume`、`GET /api/v1/intel/checks`、
  `PATCH /api/v1/intel/checks/{id}`。
- `GET /api/v1/nodes/export`、`GET|POST /api/v1/export-profiles`、
  `GET|PATCH|DELETE /api/v1/export-profiles/{id}`、
  `POST /api/v1/export-profiles/{id}/actions/rotate-token`。
- `GET /sub/{token}` — 公开入口，不需要管理令牌，路径中的令牌即凭据。由管理面处理函数在承载管理面的监听地址（主监听与 `PRISM_ADMIN_LISTEN`）上提供，并受该接入点 `allow_management` 约束；未知令牌、已停用的配置、以及关闭了管理面的接入点都返回 `404`。它有自己的限流（每令牌 60 次/分钟、每客户端 IP 120 次/分钟，超限 `429`），不属于 `PRISM_PROXY_AUTH_FAIL_LIMIT` 的覆盖范围。

仓库中目前没有 OpenAPI 文档，也没有 `/ui/docs` 页面；完整路由表见
`internal/api/server.go`。

### 备份与恢复

```bash
# 备份到 backups/<时间戳>/（state.db、cache.db、intel.db）
./scripts/prism-backup.sh backup

# 只保留最近 7 份
./scripts/prism-backup.sh backup --keep 7

# 列出所有备份
./scripts/prism-backup.sh list

# 恢复（先停止服务：运行中的实例会被拒绝）
sudo systemctl stop prism
./scripts/prism-backup.sh restore --from backups/20260924T101500Z --force
sudo systemctl start prism
```

两个子命令都调用 `./bin/prism backup --out DIR` 与
`./bin/prism restore --from DIR`：备份是对每个数据库执行 `VACUUM INTO`
得到的快照，并附带记录大小与 sha256 的 `manifest.json`；恢复会先校验清单再
替换文件。`.env` 永远不会进入备份。

### 开发

```bash
make backend          # 只构建 Go 二进制 -> bin/prism
make backend-lite     # 目前标签集合相同；不构建 mihomo（见 docs/ENGINE_DECISIONS.md）
make build            # 前端 + 后端
make test             # go test ./cmd/... ./internal/...
make verify           # vet + 两套标签的测试 + 协议矩阵
make protocol-matrix  # 表驱动的协议构建矩阵
bash scripts/smoke.sh # 针对临时环境的端到端冒烟测试
```

### 项目结构

```text
Prism/
├── cmd/prism/                # 入口、子命令、监听器与入站分流
├── internal/
│   ├── api/                  # REST 处理器与中间件；api/web/ 为 React 前端
│   ├── buildinfo/            # 版本、提交、构建时间与标签
│   ├── config/               # PRISM_* 环境配置
│   ├── geoip/                # 国家数据库与查询
│   ├── inspection/           # 质量任务队列与数据源适配
│   ├── metrics/              # 实时、历史与快照指标
│   ├── model/                # 共享领域模型（平台、租约、审计等）
│   ├── netutil/              # 下载器与重试工具
│   ├── node/                 # 节点记录与哈希
│   ├── outbound/             # sing-box 节点适配
│   ├── platform/             # 可路由平台视图与筛选
│   ├── probe/                # 延迟与出口探测
│   ├── proxy/                # HTTP/SOCKS5/反代入口与转发
│   ├── quality/              # 质量证据与评估
│   ├── requestlog/           # 有界请求日志存储
│   ├── routing/              # P2C、租约、定时轮换
│   ├── scanloop/             # 周期性维护循环
│   ├── service/              # 控制平面用例
│   ├── state/                # SQLite 状态/缓存引擎与迁移
│   ├── subscription/         # 订阅解析（链接、Clash、sing-box、Surge）
│   ├── testutil/             # 共享测试辅助
│   └── topology/             # 订阅、节点与平台关系
├── deploy/                   # 部署用 backend.env.example
├── docker/                   # 容器入口脚本
├── docs/                     # 设计、部署、安全与计划文档
├── scripts/                  # deploy.sh、prism-backup.sh、smoke.sh
├── Dockerfile、docker-compose.yml.example
└── Makefile
```

### 安全性

已实现且有测试覆盖的控制措施（管理 API 鉴权与按 IP 的鉴权失败限流：`429`
配合 `Retry-After`，仅当 `RemoteAddr` 属于 `PRISM_TRUSTED_PROXIES` 时才信任
`X-Forwarded-For`；管理监听端口隔离；管理写操作审计与 90 天保留；可校验的
在线备份与恢复；可选的 `PRISM_DIRECT_DENY_PRIVATE` 本机直连地址策略：在**所有**
本机直连路径（反代 bypass、正向 HTTP、CONNECT、SOCKS5）上拒绝回环、私网、
链路本地、CGNAT、保留地址与云元数据地址）见
[docs/SECURITY.md](docs/SECURITY.md)。同一文档列出了已知限制：没有 TLS 监听
（请在 Prism 前面终止 TLS），代理入口限流与本机直连地址策略默认关闭；默认关闭时
本机直连路径不受地址限制，暴露正向 HTTP/CONNECT/SOCKS5 入口时应显式开启。
### 许可证

GPL-3.0-or-later，详见 `LICENSE` 文件。

### 贡献

1. Fork 本仓库。
2. 创建功能分支。
3. 提交 PR 前运行 `make verify` 与 `bash scripts/smoke.sh`。
4. 行为变更请同步更新 `docs/`。

### 支持

- GitHub Issues: https://github.com/mycatxl/Prism/issues
- 文档: https://github.com/mycatxl/Prism/tree/main/docs
