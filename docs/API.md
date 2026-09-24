# Prism 管理 API 参考（`/api/v1`）

**中文摘要**：本文件按模块列出树里真实注册的**全部 91 条 `/api/v1` 路由**（其中 90 条需要管理员令牌，1 条 SSE 端点另有 `?access_token=` 通道），
以及鉴权方式、错误响应形状、分页约定与 5 类容易踩到的行为。事实来源只有一处：`internal/api/server.go` 的路由注册表（`authed.Handle(...)`
与未鉴权的 `mux.Handle(...)`），每条语义都回到对应 handler 与 service 实现核对，凡是代码没写死的都不写。

This document is the endpoint reference of the current tree, derived from `internal/api/server.go` and the
handlers under `internal/api/handler_*.go`. It is not generated from the plan documents (`docs/plan/`), whose
route tables are older than the implementation. Where the two disagree, this file follows the code and says so.

复现方式：

```sh
# 1. 路由表本身（唯一真相）
grep -n '\.Handle(' internal/api/server.go

# 2. 一次真实调用（令牌来自 <deploy-dir>/.env）
curl -sS -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/system/capabilities

# 3. 未鉴权端点的对照：换一个错误令牌，观察 401
curl -sS -i -H "Authorization: Bearer wrong" http://127.0.0.1:2260/api/v1/system/info
```

代码引用写到文件与符号；测试引用写到测试名。

## 1. 鉴权与两个监听面

| 项 | 行为 | 出处 |
|---|---|---|
| 凭据 | `Authorization: Bearer <PRISM_ADMIN_TOKEN>`。令牌用 `crypto/subtle.ConstantTimeCompare` 比较，长度不同也走同一路径 | `internal/api/rate_limiter.go` `AuthMiddleware` |
| 失败响应 | 缺头 → `401 UNAUTHORIZED`（`missing Authorization header`）；前缀不是 `Bearer ` → `401`（`invalid Authorization header format`）；令牌不符 → `401`（`invalid admin token`） | 同上 |
| 登录失败限流 | 每个客户端 IP 在 60 秒内累计 **10** 次失败后封禁 **5 分钟**，期间所有管理请求先答 `429 RATE_LIMITED` 并带 `Retry-After`。成功请求永不计数，`X-Forwarded-For` 只在 peer 命中 `PRISM_TRUSTED_PROXIES` 时被采纳 | `internal/api/rate_limiter.go` 顶部的常量块；`docs/SECURITY.md` §1.2 |
| 空令牌 | `PRISM_ADMIN_TOKEN` 为空表示管理员鉴权被**有意关闭**，中间件直接放行（`AuthMiddleware` 的首个分支）。启动时的空令牌/弱令牌策略见 `docs/MIGRATION_FROM_RESIN.md` 偏差 X1、X2 | `internal/api/rate_limiter.go`；`docs/SECURITY.md` |
| 管理专用监听面 | `PRISM_ADMIN_LISTEN=host:port` 起第二个 listener，**只**服务 `/`、`/healthz`、`/api`、`/api/*`、`/ui`、`/ui/*`、`/sub`、`/sub/*`；其余路径一律 404，CONNECT 一律拒绝 | `cmd/prism/admin_runtime.go` `newAdminOnlyHandler`、`isManagementPath` |
| 主监听面上的管理面 | 主 listener 的 `/api/*`、`/ui/*`、`/sub/*` 先经过**接入点** `allow_management` 判定：为 false 时直接 404；**`/healthz` 是唯一例外**，任何接入点都放行 | `cmd/prism/inbound_mux.go` `shouldRouteControlPlane` 与 `newInboundMuxWithGuard` 的调用点 |
| SSE 的第二种凭据 | `GET /api/v1/intel/jobs/{id}/events` 额外接受 `?access_token=<管理员令牌>`（浏览器 `EventSource` 不能设请求头）。它不经过 `AuthMiddleware`，而是走自己的常量时间比较与同一个失败限流器 | `internal/api/handler_intel.go` `intelEventAuthorized`、`accessTokenFromRequest` |

## 2. 错误响应形状

所有错误都是同一个信封（`internal/api/response.go` `WriteError`）：

```json
{"error":{"code":"INVALID_ARGUMENT","message":"limit: must be <= 100000"}}
```

`code` 与 HTTP 状态的映射在 `internal/api/errors.go` `writeServiceError`：

| code | HTTP |
|---|---|
| `INVALID_ARGUMENT` | 400 |
| `NOT_FOUND` | 404 |
| `CONFLICT` | 409 |
| 其他（含 `INTERNAL`、`RATE_LIMITED`） | **500** |

最后一行是真实行为，不是笔误：`writeServiceError` 的 `default` 分支固定 500，却把原样 `code` 写进响应体。
因此 `GET /api/v1/intel/jobs/{id}/events` 的订阅数超限（`jobs.ErrTooManySubscribers`）会收到
**HTTP 500 + `{"error":{"code":"RATE_LIMITED",...}}`**（`internal/service/control_plane_intel.go` `mapIntelError`）。

其他固定状态码：

| 状态 | 何时 |
|---|---|
| 400 `PAYLOAD_TOO_LARGE` | 请求体超过 `PRISM_API_MAX_BODY_BYTES`（`errors.go` `writePayloadTooLarge`） |
| 405 `METHOD_NOT_ALLOWED` | `/sub/{token}` 只接受 GET/HEAD（`handler_subscription_token.go`） |
| 429 `RATE_LIMITED` | 管理面登录失败限流（§1）或 `/sub/{token}` 自己的限流（§4） |
| 502 / 429 | 仅 `POST /api/v1/nodes/{hash}/actions/review-ippure`：IPPure 供应商错误为 502，`IPPURE_LIMIT` 为 429 并带 `Retry-After`（`handler_quality.go` `HandleReviewIPPure`） |
| 503 `UNAVAILABLE` | 指标端点里运行时统计尚未就绪（`handler_metrics.go`） |

## 3. 请求体、查询参数与分页约定

**请求体**：所有 `POST`/`PUT`/部分 `PATCH` 走 `internal/api/api_helpers.go` `DecodeBody`，它同时满足三件事：
`DisallowUnknownFields`、只允许**单个** JSON 值（多余内容 → `invalid request body: must contain a single JSON value`）、
超限体转成 413。`DisallowUnknownFields` 由 `encoding/json` 递归生效，**嵌套对象里的未知字段同样 400**——
多传一个字段和拼错一个字段名都是硬错误，不会被忽略。

平台/订阅/系统配置的 `PATCH` 不用 `DecodeBody`，而是「受限合并补丁」：`readRawBodyOrWriteInvalid` 读原始体后走
`parseMergePatch` + `validateFields`（`internal/service/patch_helpers.go`）。它**不是** RFC 7396 JSON Merge Patch：

- 体必须是**非空对象**，否则 400 `empty patch`；
- 字段必须命中白名单，否则 400 `field "x" is read-only or unknown`；
- **`null` 值是错误**（`null value not allowed for field: "x"`），不能用 null 清字段。

**分页**：默认是 `limit`/`offset`（`api_helpers.go` `ParsePagination`）：`limit` 缺省 50、`limit=0` 视为缺省、
上限 100000（超出 400）、`offset` 必须 ≥ 0。响应信封是 `{"items":[…],"total":N,"limit":L,"offset":O}`
（`response.go` `WritePage`）。三个例外：

| 端点 | 分页方式 |
|---|---|
| `GET /api/v1/request-logs` | **游标**：`limit`（默认 50）+ `cursor`（base64url 的 `tsNs:id`）；带 `offset` 直接 400 `offset: not supported for request-logs; use cursor`；响应为 `{items, limit, has_more, next_cursor?}` |
| `GET /api/v1/audit-logs` | 反向游标：`before_id` + `limit`（默认 100，**上限 200**）；响应为 `{items, limit}`，按 id 降序 |
| `GET /api/v1/nodes/export`、`/sub/{token}` | 导出专用：`limit` 默认 `export.MaxItems`=**5000**，显式传更大的值直接 400；`offset` 是选块游标，被截掉的条数通过 `X-Prism-Export-Truncated` 头暴露 |

**查询参数**：布尔有两套实现，别混用——`ParseBoolQuery`（`strconv.ParseBool`，接受 `1/0/t/T/TRUE` 等）用于
`/nodes`、`/subscriptions`、`/nodes/export` 的 `enabled`、`circuit_open`、`has_outbound`、`native`、`healthy_only`；
`parseStrictBoolQuery`（只认 `true`/`false`）用于 `/platforms/{id}/leases` 的 `fuzzy`、`/request-logs` 的 `net_ok`/`fuzzy`。
`sort_by`/`sort_order` 走白名单（`ParseSorting`），非法值 400。时间戳一律 RFC3339Nano，时长一律 Go duration 字符串（如 `"24h"`）。

## 4. 不需要管理员令牌的入口

这些路径**不**经过 `AuthMiddleware`，必须单独识别：

| 方法 | 路径 | 凭据 / 门控 | 行为 |
|---|---|---|---|
| GET | `/healthz` | 无 | `{"status":"ok"}`。任何接入点、任何监听面都可用（`handler_healthz.go`；`inbound_mux.go` 对 `/healthz` 豁免 `allow_management`） |
| GET | `/` | 无 | 302 → `/ui/`（`webui.go` `newRootRedirectHandler`） |
| GET | `/ui` | 无 | 302 → `/ui/` |
| GET | `/ui/…` | 无 | 嵌入的 SPA；资源不存在且带扩展名 → 404，无扩展名的路径回落 `index.html`。**前端未构建时整个 `/ui/` 返回 503** + `WebUI not built. Run: make web`（`webui.go`、`docs/deployment.md`） |
| GET/HEAD | `/sub/{token}` | 路径里的令牌**就是**凭据（SHA-256 摘要查表） | 命中且 `enabled=true` 时直接返回导出文件；未知令牌、被停用的 profile、`allow_management=false` 的接入点、存储不可读——**全部统一 404**，不可用于探测令牌是否存在。响应带 `Cache-Control: no-store`、`X-Prism-Export-Exported/Skipped`，`v2rayn` 加 `Profile-Update-Interval: 12`，`mihomo` 加 `Content-Disposition`。限流：**60 次/分钟/令牌 + 120 次/分钟/客户端 IP**，超出 429 + `Retry-After`（`handler_subscription_token.go`、`export_token.go`） |
| GET | `/api/v1/intel/jobs/{id}/events` | 管理员令牌（头或 `?access_token=`） | SSE 进度流；不经过 `AuthMiddleware`，但有自己的常量时间比较与失败限流 |

另外两类「非管理员鉴权」入口，属于代理面而不是管理面，本文件只登记不展开：

- `POST /{proxyToken}/api/v1/{platform}/actions/inherit-lease`——路径里的代理令牌是凭据，由 `cmd/prism` 的 inbound mux
  路由到 `internal/api/handler_token_action.go`（`shouldRouteTokenAPI`）。
- HTTP 正向代理、CONNECT、SOCKS5、URL 反代——见 `README.md` 的用法章节与 `docs/PROTOCOLS.md`。

## 5. 系统与配置

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/system/info` | 版本与运行信息 | `version`、`git_commit`、`build_time`、`build_tags[]`、`started_at`（`internal/service/interfaces.go` `SystemInfo`） |
| GET | `/api/v1/system/config` | 当前运行时配置 | 直接序列化 `RuntimeConfig`；控制面未初始化时返回 `null` |
| GET | `/api/v1/system/config/default` | 默认运行时配置 | `config.NewDefaultRuntimeConfig()` |
| GET | `/api/v1/system/config/env` | 环境配置快照（只读） | 只暴露 `admin_token_set` / `proxy_token_set` / `admin_token_weak` / `proxy_token_weak`，**永不返回令牌本身**；含 `api_max_body_bytes`、各 `metric_*` 保留窗口、`request_log_*`、`auth_version` 等（`handler_system.go` `systemEnvConfigSnapshot`） |
| GET | `/api/v1/system/capabilities` | 引擎/协议能力 | `engines[]`、`build_tags[]`、`share_link_schemes[]`、`file_formats[]`。mihomo 恒为 `built:false`（`docs/ENGINE_DECISIONS.md` D-1） |
| PATCH | `/api/v1/system/config` | 修改运行时配置 | 白名单 `runtimeConfigAllowedFields`（`internal/service/control_plane_system.go`）：`request_log_enabled`、`reverse_proxy_log_detail_enabled`、`reverse_proxy_log_req_headers_max_bytes`、`reverse_proxy_log_req_body_max_bytes`、`reverse_proxy_log_resp_headers_max_bytes`、`reverse_proxy_log_resp_body_max_bytes`、`max_consecutive_failures`、`max_latency_test_interval`、`max_authority_latency_test_interval`、`max_egress_test_interval`、`latency_test_url`、`egress_trace_url`、`latency_authorities`、`p2c_latency_window`、`latency_decay_window`、`cache_flush_interval`、`cache_flush_dirty_threshold`、`intel_enabled`、`intel_node_workers`、`intel_check_concurrency_per_check`、`intel_max_running_jobs`、`intel_auto_checks`、`intel_refresh_schedule`；未知/null 字段 400 |

## 6. 订阅

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/subscriptions` | 列表 | `enabled` 可选过滤；`keyword` 子串匹配 `id`/`name`/`url`/`source_type`；`sort_by` ∈ `name`,`created_at`,`last_checked`,`last_updated`（默认 `created_at` asc） |
| POST | `/api/v1/subscriptions` | 创建 | 体：`name`、`source_type`(`remote` 默认 / `local`)、`url`、`content`、`update_interval`（≥30s）、`enabled`、`ephemeral`、`incremental_alive_nodes`、`ephemeral_node_evict_delay`（默认 72h）、`auto_intel`（默认 true）。`remote` 带 `content` 或 `local` 带 `url` → 400。**201** |
| GET | `/api/v1/subscriptions/{id}` | 详情 | `{id}` 必须是规范小写 UUID，否则 400 `subscription_id: must be a valid UUID`；响应含 `node_count`/`healthy_node_count`/`parse_report?`/`last_error?` |
| PATCH | `/api/v1/subscriptions/{id}` | 局部更新 | 白名单：`name`、`url`、`content`、`update_interval`、`enabled`、`ephemeral`、`incremental_alive_nodes`、`ephemeral_node_evict_delay`、`auto_intel`；`source_type` 是只读 |
| DELETE | `/api/v1/subscriptions/{id}` | 删除订阅及其节点 | **204**；运行时状态只在库删除成功后才改 |
| POST | `/api/v1/subscriptions/{id}/actions/refresh` | 立即拉取并解析 | 200 `{"status":"ok"}`；同步执行，没有进度流 |
| POST | `/api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes` | 清理熔断/构建失败的节点 | 200 `{"cleaned_count":N}` |
| GET | `/api/v1/subscriptions/{id}/parse-report` | 上次解析的丢弃明细 | `{subscription_id, summary, skipped[], stats, truncated?, original_bytes?}`；从未解析过时返回空报告（`skipped:[]`）而不是 404；`stats.skipped` 是真实总数，`stats.skipped_overflow` 记未保留的条数 |

## 7. 节点

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/nodes` | 节点列表 | 响应在标准分页信封上多两个字段：`unique_egress_ips`、`unique_healthy_egress_ips`。过滤参数见下 |
| GET | `/api/v1/nodes/{hash}` | 节点详情 | `NodeSummary`；hash 非十六进制 → 400 `node_hash: invalid format`，超过 128 字符 → 400，节点不存在 → 404 |
| GET | `/api/v1/nodes/export` | 导出节点文件 | 见 §17 |
| POST | `/api/v1/nodes/{hash}/actions/probe-egress` | 同步出口探测（阻塞） | 200 `{"egress_ip","region?","latency_ewma_ms"}`。失败分支：hash 非十六进制 → 400 `node_hash: invalid format`；节点不存在 → 404；探测本身失败（出站未就绪、无 fetcher 等）→ **500** `INTERNAL`「egress probe failed」（`control_plane_nodes.go` 用 `internal()` 包装，不区分 5xx 原因） |
| POST | `/api/v1/nodes/{hash}/actions/probe-latency` | 同步延迟探测（阻塞） | 200 `{"latency_ewma_ms"}`；失败分支与 probe-egress 相同（400 / 404 / 500） |
| POST | `/api/v1/nodes/{hash}/actions/probe-quality` | 按节点当前出口 IP 请求质量检测 | `{"quality":{…},"queued":bool,"warnings?":[…]}`；`queued=true` 时返回 **202**（`handler_quality.go`）；出口 IP 缺失或超过 15 分钟会先做一次同步出口探测 |
| POST | `/api/v1/nodes/{hash}/actions/review-ippure` | 用该节点**当前**出口发起一次 IPPure 复核 | `Cache-Control: no-store`；结果含 `matched_node_ip`，只有匹配时才写入缓存；供应商错误 502、`IPPURE_LIMIT` 429 + `Retry-After`、IPPure 未配置或出站未就绪 → 409 |

`GET /api/v1/nodes` 的查询参数（全部可选，非法值 400，除注明外）：

- `ip_type` ∈ `unknown`,`residential`,`non_residential`,`datacenter`,`business`,`wireless`,`mobile`,`conflicting`
- `quality_state` ∈ `unobserved`,`pending`,`partial`,`valid`,`stale`,`conflicting`,`unsupported`
- `risk_grade` ∈ `unknown`,`low`,`moderate`,`high`,`severe`,`review`；`purity_band` ∈ `unknown`,`excellent`,`clean`,`fair`,`mixed`,`poor`,`review`
- `protocol`（小写标识，`[a-z0-9-]` 且 ≤32 字符，如 `vless`、`wireguard`、`openvpn-client`、`ssr`）
- `platform_id`、`subscription_id`（UUID）、`region`、`egress_ip`、`tag_keyword`
- `circuit_open`、`has_outbound`、`enabled`、`native`（布尔）、`probed_since`（RFC3339Nano）
- intel 侧：`purity_min`/`purity_max`（0..100）、`verdict`（逗号分隔，≤8 个值）、`confidence_min` ∈ `low`,`medium`,`high`（**不接受 `none`**）、`asn`（正整数）、`country`（ISO 码，自动大写）、`check=<id>:<outcome>`（可重复，≤20 个）
- 排序：`sort_by` ∈ `tag`,`created_at`,`failure_count`,`region`,`purity_score`,`latency`,`assessed_at`（默认 `tag` asc）。缺少排序键的节点（无纯度分/无延迟/无评估）**永远排在最后**，且不受 `sort_order` 反转；同键用 `node_hash` 兜底，翻页稳定

## 8. 平台

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/platforms` | 列表 | `keyword` 匹配 `id`/`name`/`region_filters`；`sort_by` ∈ `name`,`id`,`updated_at`；`Default` 平台恒排第一 |
| POST | `/api/v1/platforms` | 创建 | 必填 `name`；可选 `sticky_ttl`、`regex_filters`、`region_filters`、`reverse_proxy_miss_action`、`reverse_proxy_empty_account_behavior`、`reverse_proxy_fixed_account_header`、`allocation_policy`、`passive_circuit_breaker_disabled`、`scheduled_rotation_interval`、`scheduled_rotation_enabled`、`rotation_avoid_previous_ip`、`quality_policy`。保留名 `Default` → 409。**201** |
| GET | `/api/v1/platforms/{id}` | 详情 | UUID 校验；响应含 `routable_node_count` |
| PATCH | `/api/v1/platforms/{id}` | 局部更新 | 白名单与创建字段基本相同（`name`,`sticky_ttl`,`regex_filters`,`region_filters`,`reverse_proxy_*`,`allocation_policy`,`passive_circuit_breaker_disabled`,`scheduled_rotation_*`,`rotation_avoid_previous_ip`,`quality_policy`）。`Default` 平台改名 → 409 |
| DELETE | `/api/v1/platforms/{id}` | 删除 | **204**；`Default` 平台 → 409 |
| POST | `/api/v1/platforms/{id}/actions/reset-to-default` | 把平台配置重置为环境默认值 | 200 返回重置后的平台 |
| POST | `/api/v1/platforms/{id}/actions/rebuild-routable-view` | 强制重建可路由视图 | 200 `{"status":"ok"}` |
| POST | `/api/v1/platforms/preview-filter` | 用一组过滤器预演命中与排除原因 | 体：`platform_id?`、`platform_spec{regex_filters,region_filters}?`、`quality_policy?`（仅本次预演生效）。响应是标准分页信封 **加** `excluded_by`（`regex`、`region` 与 `QUALITY_*` 计数） |
| GET | `/api/v1/platforms/{id}/nodes/{hash}/explain` | 说明某节点为何被该平台收录/排除 | 200 一组逐条判定（`node.*`、regex/region/quality 判据） |

## 9. 租约

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/platforms/{id}/leases` | 租约列表 | `account`（精确；`fuzzy=true` 时子串、大小写不敏感）、`fuzzy`（严格布尔）、`sort_by` ∈ `account`,`expiry`,`last_accessed`（默认 `expiry` asc） |
| DELETE | `/api/v1/platforms/{id}/leases` | 清空该平台全部租约 | **204** |
| GET | `/api/v1/platforms/{id}/leases/{account}` | 单条租约 | `account` 去空白后为空 → 400；不存在 → 404 |
| DELETE | `/api/v1/platforms/{id}/leases/{account}` | 删除单条租约 | **204** |
| POST | `/api/v1/platforms/{id}/leases/{account}/actions/rotate` | 轮换该账号的出口 | 删除租约并写轮换墓碑（避免立刻回到旧 IP），**204** |
| GET | `/api/v1/platforms/{id}/ip-load` | 每个出口 IP 的租约分布 | `sort_by` ∈ `egress_ip`,`lease_count`（默认 `lease_count` desc）；按 `lease_count` 排序时同数按 IP 兜底 |

## 10. 接入点（endpoints）

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/endpoints` | 列表 | 含环境定义的默认接入点（`read_only: true`，不可改不可删）与每个接入点的运行时 `status`/`last_error` |
| POST | `/api/v1/endpoints` | 创建并尝试监听 | 必填 `port`；可选 `enabled`、`allow_management`、`allow_proxy`、`require_proxy_auth_info`、`allow_http_forward`、`allow_http_reverse`、`allow_socks5`。监听失败会回滚持久化。**201** |
| GET | `/api/v1/endpoints/{id}` | 详情 | — |
| PATCH | `/api/v1/endpoints/{id}` | 局部更新 | 白名单：`enabled`、`port`、`allow_management`、`allow_proxy`、`require_proxy_auth_info`、`allow_http_forward`、`allow_http_reverse`、`allow_socks5` |
| DELETE | `/api/v1/endpoints/{id}` | 删除并释放端口 | **204**；环境定义的默认接入点（`id=default`，`source: environment`、`read_only: true`）受保护：`GET` 能查，`PATCH`/`DELETE` → **409** `default endpoint is read-only`。端口冲突 → 409 `endpoint port already exists` |

`allow_management=false` 的接入点同时失去 `/api/*`、`/ui/*` 与 **`/sub/*`**（`handler_subscription_token.go` 注释与
`subscriptionsEnabled`）：只要树里还存在一个 `allow_management=true` 的接入点，`/sub` 就仍然可用。

## 11. 请求头规则（account-header-rules）

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/account-header-rules` | 列表 | `keyword` 匹配 `url_prefix` 与 `headers[]`；`limit`/`offset`。响应 `{url_prefix, headers[], updated_at}` |
| PUT | `/api/v1/account-header-rules/{prefix...}` | 插入或整体替换一条规则 | 体的 `url_prefix` **必须为空**：前缀只从路径取（`url_prefix must be provided in path, not body` → 400）。新建 **201**，覆盖 **200**。`headers[]` 需通过 RFC 7230 token 校验 |
| POST | `/api/v1/account-header-rules:resolve` | 用一条 URL 预演命中哪条规则、取哪些头 | 体 `{"url":"https://…"}`（必须 http/https 绝对 URL）；响应 `{"matched_url_prefix","headers":[]}`，未命中时为空对象 |
| DELETE | `/api/v1/account-header-rules/{prefix...}` | 删除规则 | **204**；兜底规则 `*` 不可删除 → 400 |

## 12. 请求日志

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/request-logs` | 分页查询请求日志 | 见 §3 的游标约定（**`offset` 会 400**）。过滤：`from`/`to`（RFC3339Nano，`from` 必须早于 `to`）、`platform_id`、`platform_name`、`account`、`target_host`、`egress_ip`、`proxy_type`（1..3）、`http_status`（100..599）、`net_ok`（严格布尔）、`fuzzy`（严格布尔） |
| GET | `/api/v1/request-logs/{log_id}` | 单条日志 | 不存在 → 404 |
| GET | `/api/v1/request-logs/{log_id}/payloads` | 抓取的请求/响应头与正文 | 四段 base64（`req_headers_b64`、`req_body_b64`、`resp_headers_b64`、`resp_body_b64`）+ `truncated{req_headers,req_body,resp_headers,resp_body}` 截断标记；先查日志行存在，再取 body |

只有 `requestlogRepo != nil` 时这三条才会注册（`server.go`），否则路径落到 `authed` 的 404。

## 13. 质量（quality）

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/quality/status` | 检测子系统状态 | 与 WebUI 契约一致：`enabled`、`known_ips`/`checked_ips`/`low_risk_ips`/`high_risk_ips`/`stale_ips`、`queue_capacity`、`dropped_observations`、`storage_error`、`sources[]`、`manual_sources[]`、`registry_sources[]`。**空列表永远是 `[]`，不是 `null`**（`internal/inspection/manager.go` `Status` 注释 + `ControlPlaneService.QualityStatus`） |
| GET | `/api/v1/quality/assessments` | 质量评估列表 | `q` 子串匹配 IP、ASN、组织；按最近一次证据时间降序，同时间按 IP 升序；`limit`/`offset` |
| GET | `/api/v1/quality/ip/{ip}` | 某个 IP 的质量摘要 | IP 非法 → 400；返回 `quality.Summary`（含 `assessment`） |
| POST | `/api/v1/quality/ip/{ip}/actions/probe` | 手动请求该 IP 的质量检测 | `{"quality":{…},"queued":bool,"warnings?":[…]}`；`queued=true` → **202**。状态码分支见 §21 第 4 条 |

## 14. intel 任务与节点情报

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| POST | `/api/v1/intel/jobs` | 创建手动批量检测任务 | 体即 `jobs.Request`：`kind`、`scope{all?,subscription_ids?,platform_ids?,node_hashes?,filter?}`、`providers[]`、`checks[]`、`force`。**202**，响应 `{"job":{…}}`；队列满/单次节点数超限 → 400；`intel_enabled=false` → 409 |
| GET | `/api/v1/intel/jobs` | 任务列表 | `status` 过滤见 §21 第 5 条（非法值静默变「全部」）；`limit`/`offset`；响应 `{items,total,limit,offset}`（空列表为 `[]`） |
| GET | `/api/v1/intel/jobs/{id}` | 任务详情 | `{"job":{…},"progress":{…}}`；不存在 → 404 |
| GET | `/api/v1/intel/jobs/{id}/items` | 任务内的节点条目 | `status` 过滤（非法值同样静默变「全部」）+ `limit`/`offset`；响应 `{items,total,limit,offset}` |
| POST | `/api/v1/intel/jobs/{id}/actions/cancel` | 取消任务 | **202** `{"job":{…}}` |
| POST | `/api/v1/intel/jobs/{id}/actions/retry-failed` | 失败条目重排 | **202** `{"job":{…},"retried":N}` |
| GET | `/api/v1/intel/jobs/{id}/events` | SSE 进度流 | 见 §1 与 §4；事件 `progress`/`end`，15 秒心跳注释帧；订阅数超限 → HTTP 500 + `RATE_LIMITED`（§2） |
| GET | `/api/v1/intel/status` | 子系统总览 | `enabled`、`database{db_bytes,assessments,node_egress}`、`executor`、`discarded{egress_observations,sse_frames}`、`providers[]`（**只给 `has_key`，不给凭据**）、`jobs_by_status{}` |
| GET | `/api/v1/intel/nodes/{hash}` | 单节点情报 | `{node_hash,egress,history[],checks[],ipv4_assessment?,ipv6_assessment?,ipv4_projection?,ipv6_projection?}` |
| GET | `/api/v1/intel/ip/{ip}` | 单 IP 情报 | `{ip,evidence[],assessment?,projection?,nodes[]}`；`nodes[]` 上限 `maxIntelIPNodes`=100 |

## 15. intel 数据源与检测规则

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/intel/providers` | 数据源列表 | 带 `Cache-Control: no-store`；返回有效设置与今日额度/队列状态（`used`、`remaining`（-1 表示不限量）、`exhausted`、`next_allowed_at_ns`、`paused`、`error_code`、`has_key` 等），永不返回密钥 |
| PATCH | `/api/v1/intel/providers/{id}` | 修改数据源 | 体 `ProviderPatch`：`enabled`、`api_key`（写专用：显式空串清除，`null` 保留）、`daily_limit`、`qps`、`ttl`（正 duration）、`config`（合并，值为 null 的键被删除）。改完立即对运行中的 registry 生效，无需重启；未知 provider → 404 |
| POST | `/api/v1/intel/providers/{id}/actions/resume` | 清除暂停与 429 冷却 | 200 返回该数据源的新状态 |
| POST | `/api/v1/intel/providers/{id}/actions/refresh` | 立刻下载该数据源的离线库 | 200 逐文件结果；不带可下载库的数据源 → 409 CONFLICT；未配置的库是「记一次 skip」而不是错误 |
| GET | `/api/v1/intel/checks` | 检测规则列表 | 响应 `{items,total,limit,offset,load_errors[]}`；`load_errors` 报告 `$PRISM_STATE_DIR/checks.d` 下加载失败的用户规则文件。`path` 只出现在用户规则上，内置规则没有路径 |
| PATCH | `/api/v1/intel/checks/{id}` | 开关一条检测规则 | 体只接受 `{"enabled":bool}`；持久化为 `check:<id>`，立即生效；规则不存在 → 404，intel 未装配 → 409 |

## 16. GeoIP

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/geoip/status` | 数据库状态 | `{db_mtime,next_scheduled_update}`（RFC3339Nano；未知时为空串） |
| GET | `/api/v1/geoip/lookup` | 单个 IP 查地区 | 必须带 `?ip=`，缺失 → 400；IP 非法 → 400 |
| POST | `/api/v1/geoip/lookup` | 批量查地区 | 体 `{"ips":[…]}`；任一 IP 非法 → 400 `ips[i]: invalid IP address`；响应 `{"results":[{"ip","region"}]}` |
| POST | `/api/v1/geoip/actions/update-now` | 立刻更新数据库（阻塞） | 200 `{"status":"ok"}`；失败 → 500 |

## 17. 导出与订阅令牌

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/nodes/export` | 导出节点，管理员直接下载 | 必填 `format` ∈ `singbox`,`mihomo`,`v2rayn`,`uri`,`csv`,`json`（`internal/export/types.go`）。过滤词表与 `/api/v1/nodes` 完全一致（含 intel 过滤器）。`name_template` ≤256 字节，`healthy_only` 布尔；响应是**文件本体**（不是 JSON），带 `Content-Type`/`Content-Disposition` 与 `X-Prism-Export-Exported`、`X-Prism-Export-Skipped`、`X-Prism-Export-Truncated` 头。跳过明细只有 JSON 格式会写进正文 |
| GET | `/api/v1/export-profiles` | 订阅档案列表 | 标准分页信封 |
| POST | `/api/v1/export-profiles` | 创建档案并一次性给出订阅 URL | 体：`name`（1..128）、`format`（必填）、`platform_id?`（UUID）、`filter?`、`name_template?`、`enabled?`（默认 true）。**201**，**明文 URL 只在这一个响应里出现**（`url` 字段） |
| GET | `/api/v1/export-profiles/{id}` | 档案详情 | **永远不含 `url`**（服务端只存 SHA-256 摘要，`token_sha256` 是 `json:"-"`） |
| PATCH | `/api/v1/export-profiles/{id}` | 修改档案 | 可改 `name`、`format`、`platform_id`、`name_template`、`enabled`、`filter`；**不返回 `url`**，也不会换令牌 |
| DELETE | `/api/v1/export-profiles/{id}` | 删除档案 | **204**；订阅 URL 立即失效且不可恢复 |
| POST | `/api/v1/export-profiles/{id}/actions/rotate-token` | 轮换订阅令牌 | 200，**新的明文 URL 只在这里出现一次**，旧令牌立刻失效。`url` 的 scheme/host 取自请求（`X-Forwarded-Proto`、`Host`），因此反代后也正确 |
| （见 §4） | `GET /sub/{token}` | 公开订阅入口 | 用摘要查表 + 限流 + 统一 404 |

## 18. 审计日志

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/audit-logs` | 管理写操作审计 | `before_id`（游标）+ `limit`（默认 100，上限 200）；响应 `{items,limit}`（**没有 total**），按 id 降序；条目含 `at_ns`、`actor`、`remote_addr`、`action`、`target`、`detail` |

只有 `cp != nil && cp.Engine != nil` 时注册。写入规则（`internal/api/audit.go`）：

- 只有 **POST/PUT/PATCH/DELETE** 才可能落记录；
- **只有 2xx 才落记录**，失败的写操作不记（「nothing changed」）；
- `detail` 只记请求体的**顶层键名**（形如 `{"keys":["name","format"]}`），不记值；
- `actor` 是管理员令牌的 SHA-256 前 8 个十六进制字符，令牌本身永不落库；
- 路径参数里带凭据的名字（`token`、`secret`、`password`、`apikey`、`api_key`）不会写进 `target`；
- `GET /sub/{token}` 由公开订阅处理器自己追加一条审计（`actor` 为 `export:<profileID>`），**不**经过审计中间件；
- 保留策略：90 天或最多 100000 条，由后台清理任务执行（`PruneAuditLogs`）。

## 19. 指标

| 方法 | 路径 | 用途 | 备注 |
|---|---|---|---|
| GET | `/api/v1/metrics/realtime/throughput` | 实时吞吐环 | `from`/`to`（RFC3339Nano，缺省 `to=now`、`from=to-1h`）；响应 `{step_seconds,items[{ts,ingress_bps,egress_bps}]}`。**不接受 `platform_id`**，传了 → 400 |
| GET | `/api/v1/metrics/realtime/connections` | 实时连接数环 | 同上，`items[{ts,inbound_connections,outbound_connections}]`；不接受 `platform_id` |
| GET | `/api/v1/metrics/realtime/leases` | 实时租约数环 | 可选 `platform_id`（不存在 → 404）；`items[{ts,active_leases}]` |
| GET | `/api/v1/metrics/history/traffic` | 历史流量桶 | `{bucket_seconds,items[{bucket_start,bucket_end,ingress_bytes,egress_bytes}]}`；不接受 `platform_id` |
| GET | `/api/v1/metrics/history/requests` | 历史请求桶 | 可选 `platform_id`；`items[…,total_requests,success_requests,success_rate]` |
| GET | `/api/v1/metrics/history/access-latency` | 历史访问延迟直方图 | 可选 `platform_id`；响应含 `bin_width_ms`/`overflow_ms`，`items[…,sample_count,buckets[{le_ms,count}],overflow_count]` |
| GET | `/api/v1/metrics/history/probes` | 历史探测计数桶 | `items[…,total_count]`；不接受 `platform_id` |
| GET | `/api/v1/metrics/history/node-pool` | 历史节点池桶 | `items[…,total_nodes,healthy_nodes,egress_ip_count]`；不接受 `platform_id` |
| GET | `/api/v1/metrics/history/lease-lifetime` | 历史租约存活分位 | **`platform_id` 必填**（缺失 400，未知 404）；`items[…,sample_count,p1_ms,p5_ms,p50_ms]` |
| GET | `/api/v1/metrics/snapshots/node-pool` | 当前节点池快照 | `{generated_at,total_nodes,healthy_nodes,egress_ip_count,healthy_egress_ip_count}`；统计未就绪 → 503；不接受 `platform_id` |
| GET | `/api/v1/metrics/snapshots/platform-node-pool` | 单平台快照 | **`platform_id` 必填**；`{generated_at,platform_id,routable_node_count,egress_ip_count}` |
| GET | `/api/v1/metrics/snapshots/node-latency-distribution` | 节点**权威域名 EWMA** 延迟分布 | 可选 `platform_id`（此时 `scope="platform"`，否则 `"global"`）。**这不是上表 `history/access-latency` 的每请求访问延迟**，两者不可混用 |

时间窗口一律会被静默裁剪到该指标家族的保留窗口（`from` 被夹到 `参考时刻 - maxWindow`，参考时刻取 `to` 与当前时间的较早者），
所以请求 24 小时而 `PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS=3600` 时返回保留的 1 小时，**而不是报错**；
只有 `from` ≥ `to` 或时间格式错误才是 400。保留窗口与 `metrics.db` 的清理见 `docs/deployment.md` 的
「Metrics retention」。**没有 Prometheus 导出器**，也没有 `/metrics`。

## 20. 备份与恢复（没有 HTTP 接口）

`/api/v1` 下**不存在**任何备份/恢复端点。备份与恢复是 CLI 子命令：

```sh
./bin/prism backup  --out DIR        # 对 state.db / cache.db / intel.db 做 VACUUM INTO 快照
./bin/prism restore --from DIR --force
./scripts/prism-backup.sh backup --out /var/backups/prism --keep 7
```

细节见 `docs/backup-restore.md` 与 `docs/deployment.md`；`prism restore` 会拒绝在实例仍在运行时执行
（判活方式与 `prism import-resin` 相同）。请勿把本文件当作备份 API 的入口。

## 21. 附录：容易踩到的行为（逐条核对结论）

1. **未知字段 = 400，且递归生效。** `DecodeBody` 用 `DisallowUnknownFields`（`api_helpers.go`），
   `encoding/json` 会把这个设置带进嵌套结构：多传一个顶层字段、或嵌套对象里拼错一个键，都是 400
   `invalid request body: json: unknown field "x"`。`PATCH` 用白名单达到同样效果（`field "x" is read-only or unknown`）。
   另一条容易忘的规则：`PATCH` **不接受 `null`**（`parseMergePatch`/`validateFields` 直接 400）。
2. **未鉴权路径必须单独记：** `/healthz`、`/`、`/ui`、`/ui/`、`/sub/{token}`（以及代理入口本身）。
   其中只有 `/healthz` 不受接入点 `allow_management` 影响；`/ui/*`、`/api/*`、`/sub/*` 在主监听面上都先过
   `allow_management`，为 false 时统一 404（`cmd/prism/inbound_mux.go`）。`PRISM_ADMIN_LISTEN` 的 listener
   只服务这些管理路径（`cmd/prism/admin_runtime.go`）。
3. **`GET /api/v1/intel/jobs/{id}/events` 不走 `AuthMiddleware`**：它接受 `Authorization: Bearer` 或
   `?access_token=`，有自己的常量时间比较与失败计数（`handler_intel.go`）。它的「订阅数过多」错误是
   HTTP **500** + `code=RATE_LIMITED`（§2）。
4. **`POST /api/v1/quality/ip/{ip}/actions/probe` 的真实分支**（与「私网地址返回 400」的直觉不同）：

   | 条件 | 结果 |
   |---|---|
   | `ControlPlaneService.Inspection == nil`（**当前树里就是这个状态**：`internal/service/control_plane_quality.go` 只判断 nil，而全仓库没有任何生产代码给该字段赋值） | **409 CONFLICT**「quality inspection is disabled」——**与 IP 是公网还是私网无关** |
   | manager 已装配且 `enabled=false`，或 `enabled=true` 但**没有任何已配置的数据源**（`inspection.Manager.Request` 循环里 `Configured` 全为 false → `quality.ErrDisabled`） | 409 CONFLICT「quality inspection is disabled」 |
   | manager 已装配且 enabled、有已配置数据源，IP 是私网/回环/链路本地/保留段（`quality.PublicIP`） | **400 INVALID_ARGUMENT**「quality inspection requires a public IP address」 |
   | IP 不是合法地址（`netip.ParseAddr` 失败） | 400 INVALID_ARGUMENT「ip: invalid address」 |

   因此隔离实例（无任何 provider 凭据）实测到的 409 来自「无已配置数据源 / 未装配 manager」这条分支，
   而不是 provider 可用性判定本身；私网 400 只在 manager 真正启用且有数据源时才可能出现。
   该端点在树里**没有单测**（`internal/api/handler_quality_test` 未移植，见 `docs/MIGRATION_FROM_RESIN.md` 文末），
   上表是代码路径结论。
5. **`status` 过滤参数非法值被静默归一成「全部」。** `internal/service/control_plane_intel.go` 的
   `normalizeJobStatus` / `normalizeJobItemStatus` 对任何不在枚举里的值返回空串，空串等于「不过滤」：

   | 端点 | 合法值 |
   |---|---|
   | `GET /api/v1/intel/jobs?status=` | `queued`,`running`,`succeeded`,`partial`,`failed`,`canceled` |
   | `GET /api/v1/intel/jobs/{id}/items?status=` | `queued`,`running`,`done`,`failed`,`skipped`,`canceled` |

   写成 `?status=success` 不会 400，而是安静地返回**全部**任务/条目。相关但**相反**的例子：`GET /nodes` 的
   `ip_type`/`quality_state`/`risk_grade`/`purity_band`/`protocol` 非法值是硬 400（`handler_node.go`）。
6. **明文订阅 URL 只出现一次。** `POST /api/v1/export-profiles` 与
   `POST /api/v1/export-profiles/{id}/actions/rotate-token` 的响应带 `url`；`GET`/`PATCH`/列表**永不**返回它
   （服务端只存 SHA-256 摘要，`model.ExportProfile.TokenSHA256` 是 `json:"-"`）。轮换后旧 URL 立刻失效。
   `/sub/{token}` 对未知令牌与已停用档案同样答 404，无法用来探测令牌存在性。
7. **审计只记成功的写操作。** 只有 POST/PUT/PATCH/DELETE 且响应 2xx 才落一条记录；读操作和失败的写操作都不落
   （`internal/api/audit.go` 的 `auditWriteMethods` 与 `recorder.status` 判定）。`detail` 只有顶层键名。
8. **两套布尔解析器不通用。** `enabled=true` 用 `ParseBoolQuery`（`1`/`t`/`TRUE` 都行），
   `/request-logs?net_ok=1` 与 `/leases?fuzzy=1` 用严格版（只认 `true`/`false`），混用会被 400 拒绝。
9. **`/api/v1/nodes/export` 与 `/sub/{token}` 返回的是文件不是 JSON。** 别把它们塞进只解析 JSON 的客户端；
   跳过明细只在 `format=json` 时进正文，其余格式靠 `X-Prism-Export-Skipped` 头。
10. **指标端点对 `platform_id` 的支持不一致**：`realtime/throughput`、`realtime/connections`、`history/traffic`、
    `history/probes`、`history/node-pool`、`snapshots/node-pool` 传 `platform_id` 会 400；
    `history/lease-lifetime` 与 `snapshots/platform-node-pool` 反过来**必填**。

## 22. 相关文档

- `README.md` 的 **Key endpoints**（端点速查与请求示例，这里不重复）与部署/使用章节。
- `docs/deployment.md`：`prism init`、systemd/`scripts/deploy.sh`、Docker、反向代理 + TLS、`PRISM_ADMIN_LISTEN`
  的用法、指标保留窗口，以及本版本**未实现**的清单（Prometheus 导出器、TLS 监听、OpenAPI 文档与 `/ui/docs` 页面）。
- `docs/PROTOCOLS.md`：协议支持矩阵、解析报告与 `auto_intel` 如何到达 API/UI。
- `docs/SECURITY.md`：令牌策略、登录失败限流、可信代理、`/sub` 公开入口的威胁模型。
- `docs/backup-restore.md`、`docs/MIGRATION_FROM_RESIN.md`（偏差清单 X1–X6 与 `prism import-resin`）。

没有 OpenAPI 文档、没有 `/ui/docs` 页面：路由的唯一真相是 `internal/api/server.go`（`docs/deployment.md`
「Not implemented」章节同样声明了这一点）。