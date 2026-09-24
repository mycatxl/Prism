# Prism 数据源、纯净度与解锁检测

**中文摘要**：本文件描述 Prism 的 intel 子系统**当前的实际行为**，取代 `docs/plan/09-intel-providers-checks.md`
里的计划描述。内容分四块：13 个内置数据源（5 个离线库、5 个主机侧在线查询、3 个经节点查询）及其额度语义与条款、
纯净度算法 `prism-purity-v2`（公式、权重、判定表与 fail-closed 准入）、解锁检测规则的编写方法（含 8 条内置规则的校准状态）、
以及自动检测的开关方式与手动批量检测的 `scope`/`filter` 语义。文中每一处事实都对应代码里的文件与符号；凡是代码与计划文档
不一致的地方，本文以代码为准并注明差异。

如何自证本文的结论：

```sh
# 1. 数据源的 spec、生效设置、今日用量与队列状态（需要 admin token）：
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" http://127.0.0.1:2260/api/v1/intel/providers
# -> {"id":"ippure","category":"via-node","via_node":true,"default_daily_limit":500,…}

# 2. 解锁规则、来源（builtin/user）与校准日期：
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" http://127.0.0.1:2260/api/v1/intel/checks
# -> {"items":[{"id":"netflix","source":"builtin","calibrated":"calibrated 2026-09-25 …"}],"load_errors":[]}

# 3. 一个节点的完整证据链（出口、证据、检测结果、评分与投影）：
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" http://127.0.0.1:2260/api/v1/intel/nodes/<hash>
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" http://127.0.0.1:2260/api/v1/intel/ip/<ip>
```

代码引用写文件名与符号，不写行号（它们与本文件在同一批工作包中改动）。

## 1. 三个子系统与数据流

### 1.1 任务种类决定跑哪些步骤

一个节点的工作被拆成 6 个编号步骤（`internal/intel/jobs/jobs.go`，`Step` 常量
`StepEgress=1` … `StepAssess=6`），任务种类（`Kind`）决定步骤计划（同文件 `kindPlans`）：

| kind | 步骤序列 | 用途 |
|---|---|---|
| `egress` | 1 | 只探出口 IP |
| `intel` | 1 → 2 → 3 → 4 → **6** | 数据源与评分，**不含解锁检测** |
| `checks` | 1 → **5** → 6 | 只跑解锁检测与评分 |
| `full` | 1 → 2 → 3 → 4 → **5** → 6 | 全流程，**只有它含解锁检测** |

数据流：**出口探测 → 离线库 → 联网队列 → via-node → 解锁检测 → 评分**。步骤是顺序的，一个节点的
item 记录 `step_index`（最后完成的步骤号），重启后从下一步续跑（`PendingSteps`）。

### 1.2 每一步的触发条件与实际动作

| 步骤 | 实现 | 触发条件与行为 |
|---|---|---|
| 1 出口探测 | `internal/intel/service.go` `pipelineRunner.RunStep` + `internal/intel/egress/egress.go` `Probe.Run` | 只要 handler 表里没有覆盖 `StepEgress` 就走内置探测器：先请求 `https://1.1.1.1/cdn-cgi/trace`（`DefaultTraceURLv4`），再请求 `https://[2606:4700:4700::1111]/cdn-cgi/trace`（`DefaultTraceURLv6`），解析 `ip`/`colo`/`loc` 写入 intel.db 的 `node_egress`，随后调用 `ProbeEgressSync` 同步 cache.db 的 `egress_ip`。**IPv4 失败是致命错误**（`EGRESS_PROBE_FAILED`，item 重试）；**没有 IPv6 是正常结果**。探测器缺失时整步 skip（`egress probe unavailable`）。运行设置里的 `egress_trace_url` 作用于 cache.db 侧的探测器，不是这里的 v4/v6 常量。 |
| 2 离线库 | `internal/intel/steps.go` `offlineStep` | 需要有本节点的出口行（否则 skip `no egress observation for this node yet`）和至少一个**已启用**的 `offline` 数据源。对每个出口地址逐个查本地库，`len(ips)×len(specs) > 64`（`DefaultMaxEvidencePerStep`）时整步报 `STEP_LIMIT_EXCEEDED`。 |
| 3 联网队列 | 同上 `enqueueOnlineStep` | 需要有出口行和至少一个**已启用且可运行**（`Runnable()`，即 Key 齐全）的 `online-ip` 数据源。它本身不发请求：只把 (provider, ip) 写进 provider 队列，`force=false` 时跳过仍有有效证据的组合。真正的 HTTP 由队列 worker 经 `NewOnlineLookup` → `onlineLookup` 执行。 |
| 4 via-node | 同上 `viaNodeStep` | 需要有出口行、至少一个已启用的 `via-node` 数据源，并且**节点 outbound 已就绪**（否则 `NODE_OUTBOUND_UNAVAILABLE`，item 退避重试）。每个数据源先过额度闸门：闸门关闭时若等待时间 ≤30 分钟（`DefaultViaNodeDeferral`）就把 item 挂到那个时刻，否则记一条带原因的 skip。 |
| 5 解锁检测 | 同上 `checksStep`（handler 只在 `store`+`checks engine`+`outbound` 都非空时注册，见 `NewStepHandlers`） | 需要节点 outbound 就绪；选中的规则数超过 32（`DefaultMaxChecksPerStep`）报 `STEP_LIMIT_EXCEEDED`。规则按 `internal/intel/checks/engine.go` `Engine.Run` 执行，结果写 `node_checks`。**只有 `kind=checks`/`kind=full` 的 item 会走到这一步**。 |
| 6 评分 | `internal/intel/service.go` `Service.assessStep`（`NewService` 里在 `handlers.Assess == nil` 时自动挂上） | 需要出口行；只有**已经有至少一条 `status=ok` 证据行**的出口地址会被重新评分，其余地址写进 summary 的 `without_evidence` 并给出 `no stored evidence for these addresses yet`。它**不检查 `intel_enabled`**。 |

步骤的统一失败/跳过语义（`internal/intel/jobs/jobs.go`）：步骤返回 `DeferUntilNs` 表示不改状态地挂起，
`ErrorCode` 表示失败（按 `backoff` 退避，最多 5 次尝试），否则记录 `result_json`（服务端截到 4 KiB）并前进。

### 1.3 结果落在哪里

- `evidence`：每个数据源每个地址一行，**失败也写**（`status=error`/`unsupported` + `error_code`），
  所以评分能区分"数据源说没有"与"从没问过"（`persistResult`）。
- `node_egress` / `egress_history`：出口观测。
- `node_checks`：解锁检测结果（`outcome`、`region`、`latency_ms`、`detail_json`、`valid_until_ns`）。
- `ip_assessment`：评分结果，`profile` 为 `prism-purity-v2`；内存里还有一份按出口 IP 索引的投影（`internal/intel/snapshot.go`），
  选路准入只读投影，不查库。

## 2. 数据源清单

### 2.1 `Kind` 的含义

`internal/intel/providers/provider.go` 的 `Kind` 有三个取值，它们的区别**不是"在哪里查"，而是"谁的额度被消耗"**：

| Kind | 线上名称 | 含义 | 额度记在谁头上 |
|---|---|---|---|
| `KindOffline` | `offline` | 只查本地数据库，**完全不联网** | 无（本地文件） |
| `KindOnlineIP` | `online-ip` | 由 **Prism 主机**直接发出（严格 HTTP 客户端，不走节点、不继承 `HTTP_PROXY`、禁止重定向） | **Prism 主机的公网 IP** |
| `KindViaNode` | `via-node` | 请求**经被测节点自己的 outbound 发出**（`internal/intel/providers/vianode.go` `fetchViaNode`：不走环境代理、不走路由、不走 bypass、不直连回退） | **那个节点自己的出口 IP** |

因为额度归属不同，via-node 的日额度按节点计，而主机侧在线查询的日额度按主机计。

### 2.2 内置数据源（`internal/intel/providers/builtin.go` `RegisterBuiltins`，共 13 个）

| id | 名称 | Kind | 需要密钥 | 默认启用 | 默认日额度 | 默认 QPS | 默认 TTL |
|---|---|---|---|---|---|---|---|
| `geo_country` | country.mmdb | offline | 否 | 是 | 不限 | 不限 | 30d |
| `dbip_lite` | DB-IP Lite | offline | 否 | 是 | 不限 | 不限 | 30d |
| `maxmind_geolite2` | MaxMind GeoLite2 | offline | 是（`account_id`+`license_key`） | 有密钥时 | 不限 | 不限 | 30d |
| `ipinfo_lite` | IPinfo Lite | offline | 是（`token`） | 有密钥时 | 不限 | 不限 | 30d |
| `torproject` | Tor Project Onionoo | offline | 否 | 是 | 不限 | 不限 | 6h |
| `proxycheck` | proxycheck.io | online-ip | 可选 | **否** | 无 Key 80/天，有 Key 900/天（上限 1,000,000） | 1 | 24h |
| `abuseipdb` | AbuseIPDB | online-ip | 是 | 有密钥时 | 900/天（上限 100,000） | 1 | 24h |
| `ipqs` | IPQualityScore | online-ip | 是 | 有密钥时 | 150/天（上限 1,000,000） | 1 | 72h |
| `ipapi_is` | ipapi.is | online-ip | 可选 | **否** | 500/天（上限 1,000,000） | 1 | 72h |
| `dnsbl` | DNS blocklists | online-ip | 否 | 是 | 不限 | **5（每个 zone）** | 24h |
| `ippure` | IPPure | via-node | 否 | 是 | **每节点 500/天** | 2（provider 级） | 7d |
| `ip_api` | ip-api.com | via-node | 否 | 是 | **不限**（免费版真正限的是每源 IP 45 次/分钟） | 5（provider 级） | 7d |
| `proxycheck_node` | proxycheck.io (via node) | via-node | 否 | 是 | **每节点 100/天** | 2（provider 级） | 24h |

- 只有 `dnsbl` 不支持 IPv6（`SupportsIPv6: false`）。
- `BatchSize` 全部为 1：`proxycheck` 的 v3 批量能力未经厂商确认，代码里按单个地址查询。
- "有密钥时启用"来自 `internal/intel/providers/settings.go` `DefaultSetting`：`RequiresKey=true` 的数据源，
  生效 `Enabled` 等于"密钥是否齐全"，与 `DefaultEnabled` 无关。所以 `maxmind_geolite2`/`ipinfo_lite`/`abuseipdb`/`ipqs`
  在首次启动填入密钥后就是启用状态，而 `proxycheck`/`ipapi_is`（`RequiresKey=false`，`DefaultEnabled=false`）
  **即使有密钥也保持关闭**，必须在设置页手动打开——`proxycheck` 关闭是刻意的，见 §2.4。
- 生效设置由三层合成：`Spec` 默认值 → `state.db` 的 `intel_provider_settings` → 首次启动时把旧
  `PRISM_QUALITY_API_KEY` / `PRISM_QUALITY_DAILY_LIMIT` / `PRISM_ABUSEIPDB_API_KEY` /
  `PRISM_IPQS_API_KEY` / `PRISM_IPAPI_IS_API_KEY` / `PRISM_MAXMIND_ACCOUNT_ID` / `PRISM_MAXMIND_LICENSE_KEY` /
  `PRISM_IPINFO_TOKEN` 写进数据库一次（`envBindings`）。之后**以数据库为准**，改环境变量不再生效。
  负数表示"显式不限"。

### 2.3 via-node 的额度语义（这一节是全文最容易误解的地方）

- **日额度按节点计**：`internal/intel/store/repo_provider.go` `ConsumeViaNodeBudget` 在一个事务里同时结算两行——
  `provider_node_state`（`provider`+`node_hash`+`day`+`used`，见迁移 `internal/intel/store/migrations/000002_via_node_budget.up.sql`）
  和 provider 级的 `provider_state`。节点的日计数耗尽时返回 `ErrBudgetExhausted`，最早重试时间是**下一个 UTC 日 0 点**。
  迁移文件的注释记下了为什么必须这样改：把整池的计数放在 provider 级，`ippure` 就变成"所有节点一共 500 次/天"，
  而不是"每个节点 500 次/天"。
- **QPS 是 provider 级的总阀门**：`provider_state.next_request_at_ns` 由 `req.GlobalQPS` 推进
  （`next = now + 1s/QPS`），与节点无关。它存在的意义不是节流某个节点，而是**防止整池节点同时打到上游**——
  这也是厂商唯一能得到的保护，因为厂商根本无法把一个 via-node 请求归因到 Prism 主机。
- **`paused` / `blocked_until`（401/403/429）也是 provider 级的**：换 Key 会自动解除 pause（`CredentialID` 变化）。
- 三个 via-node 数据源的默认值：`ippure` **每节点 500/天**；`ip_api` **无日限**（`DefaultDailyLimit: 0`）；
  `proxycheck_node` **每节点 100/天且不需要密钥**。
- 闸门关闭时 worker 不阻塞：等待时间在 30 分钟内就把 item 挂到那个时刻，超过就记一条
  `budget or quota exhausted for <时长>` 的 skip（`internal/intel/steps.go` `consumeViaNodeBudget`）。

### 2.4 `proxycheck` 与 `proxycheck_node` 的关系

同一个厂商的两条完全独立的接入路径：

| | `proxycheck` | `proxycheck_node` |
|---|---|---|
| Kind | online-ip（从 Prism 主机发出） | via-node（经被测节点发出，并**指名该节点自己的地址**） |
| 默认启用 | **否**（`DefaultEnabled: false`，配了 Key 才建议开启） | 是 |
| 密钥 | 可选；有 Key 时额度 900/天 | **永不携带密钥**，`RequiresKey: false` |
| 额度归属 | 主机公网 IP（无 Key 时 80/天） | 每个节点自己的出口 IP（100/天） |
| Profile | `proxycheck-v3-2` | `proxycheck-via-node-v3-2` |

代码注释说明了为什么 via-node 版本可行：proxycheck.io 没有匿名"查询我自己"模式，不带地址的请求会被
HTTP 400 `No valid IP Addresses supplied.` 拒绝；可用的是**在请求里明确写出地址，同时让请求从同一个节点出去**，
厂商于是把这个查询记在该节点的出口地址上。另一条硬性约束是：**密钥绝不经节点发送**（不可信 outbound），
要用 Key 就填在主机侧的 `proxycheck` 上。

**评分层把两者折叠成同一个分量**：`internal/intel/assess/assess.go` 的 `ProviderAliases` 把 `proxycheck_node`
映射到 `SourceProxycheck`，`lookup()` 先找精确匹配、再找别名，所以一个地址只会产生一个 `proxycheck` 分量和一个分数，
不会因为两条路径都有数据而重复计算。主机侧的精确匹配优先于别名。

### 2.5 `Terms` 字段（原样转述，中文概括）

`Spec.Terms` 是设置页显示的厂商条款/额度提示（`internal/intel/providers/settings_api.go` `ProviderStatus.Terms`
→ 前端 `internal/api/web/src/features/intelSettings/IntelSettingsPage.tsx`）。原文都是英文，以下是如实概括：

| id | 条款要点 |
|---|---|
| `geo_country` | 随 Prism 打包，来自 MetaCubeX meta-rules-dat 的发布；无外部额度；地区过滤继续用同一个库。 |
| `dbip_lite` | CC BY 4.0，可免费再分发，每月更新；**界面必须署名 DB-IP**。 |
| `maxmind_geolite2` | 需要 MaxMind 账号（`account_id` + `license_key`），按 GeoLite2 EULA 授权：**必须署名，且数据库必须 30 天内刷新**。 |
| `ipinfo_lite` | 需要 token，**免费仅限非商业用途**，每周刷新；以 ipinfo.io/developers 的现行条款为准。 |
| `torproject` | 公开 CC0 中继名录；Prism 最多每小时下载一次，且**从不上传任何节点地址**。 |
| `proxycheck` | 匿名查询共享 Prism 主机公网 IP 的额度（80/天）且**不得商用**；Key 可把额度提到套餐上限（Prism 默认 900/天，最多配到 1,000,000）。 |
| `abuseipdb` | 免费账号 Key 1000 次/天，Prism 默认 900 留余量；举报数据按 CC BY 4.0 授权，**不得用该 API 构建竞品黑名单**。 |
| `ipqs` | 需要付费或试用 Key；免费档 5000 次/月，**没有付费计划时不允许商用**；Prism 默认 150/天、1 QPS。 |
| `ipapi_is` | 匿名用量约 1000 次/天，**无计划或自建数据库时不允许商用**；Prism 默认 500/天、1 QPS。 |
| `dnsbl` | Spamhaus 与 SpamCop 都把公开 zone 限制在**非商业、低流量、且必须从自己的递归解析器发起**的查询；经公共解析器的查询会被拒绝（记为 `DNSBL_REFUSED`，**不算命中**）。Prism 默认每 zone 5 QPS，且不查 IPv6。 |
| `ippure` | IPPure 条款可能限制批量或系统性使用。查询经节点发出，厂商只看到该节点地址，**额度属于该节点**；Prism 按节点计默认 500/天，另加 provider 级 QPS 阀（默认 2/s）避免整池同时到达。**提高额度前请自行确认厂商条件。** |
| `ip_api` | 免费端点仅 HTTP，每源地址 45 次/分钟，且**不允许商用**。因为经节点查询，这个 45/分钟属于节点自己的出口地址，所以 Prism 统计的日额度按节点计；provider 级 5 QPS 只用来避免整池同时到达。 |
| `proxycheck_node` | 匿名查询**不得商用**，且共享发起地址的额度（100/天）。因为请求经节点发出并指名同一节点，额度按节点计——这才让大量节点在无 Key 的情况下可行。Prism 按节点计默认 100/天，另加 provider 级 QPS 阀（默认 2/s）。Key 可以提高额度，但**密钥永不经节点发送**：要填 Key 请填在主机侧的 proxycheck.io 数据源上。 |

这些是厂商侧的条件、不是 Prism 的许可承诺；用户自行承担按条款使用的责任。

### 2.6 数据源设置 API（要点）

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/intel/providers` | 每个数据源的 `spec`、生效设置（`enabled`/`runnable`/`daily_limit`/`qps`/`ttl`/`config`、只有 `has_key`，**绝不含 Key**）、今日用量 `usage`。 |
| `PATCH /api/v1/intel/providers/{id}` | 请求体 `{"enabled":bool,"api_key":string\|null,"daily_limit":int,"qps":number,"ttl":"24h","config":{...}}`；`api_key` 为 `""` 表示清除、`null` 表示不修改。改动立即作用于运行中的 registry，无需重启。 |
| `POST /api/v1/intel/providers/{id}/actions/resume` | 解除 `paused` 与 429 冷却。 |
| `POST /api/v1/intel/providers/{id}/actions/refresh` | 立刻下载该数据源的离线库；没有可下载数据库的数据源返回 409。 |

## 3. 纯净度算法 `prism-purity-v2`

全部逻辑在 `internal/intel/assess/assess.go`（纯函数，无库、无网络、无时钟依赖）。`Profile` 常量即
`ip_assessment.profile` 的值；profile 与代码不一致的行会被 `Assessor.RecomputeForeignProfiles` 重算。

### 3.1 输入与证据选择（`validEvidence`）

- 只取 `status=ok` 的行；**过期**（`valid_until` 早于现在）和 IP 不匹配的行被丢弃；
  **每个 provider 只保留 `observed_at` 最新的一行**。
- `Enabled` 是覆盖度分母：`nil` 表示"所有评分源都启用"，空 map 表示"一个都没启用"。
- `Failed` 是启用过、但最近一次查询失败的数据源 → 用来区分 `pending` 与 `unsupported`。

### 3.2 权重与组件洁净分（`scoringWeights`、`componentScore`）

| 数据源 | 权重 | 组件洁净分 `clean` 的算法 |
|---|---|---|
| `proxycheck` | 3 | 需要 `RiskScore`；`raw = clamp(RiskScore,0,100)`，`clean = 100 - raw` |
| `ippure` | 3 | 需要 `FraudScore` **且 IP 是 IPv4**（IPPure 不给 IPv6 打分）；`clean = 100 - raw` |
| `ipqs` | 3 | 需要 `FraudScore`；`clean = 100 - raw` |
| `abuseipdb` | 2 | 需要 `AbuseConfidence`；`clean = 100 - raw` |
| `ipapi_is` | 1 | `compromised=true` → 40；`proxy`/`vpn`/`tor` 任一为真 → 60；否则 100 |
| `ip_api` | 1 | 需要 `Signals.Proxy`（缺字段则**不产生分量**）；`proxy=true` → 60；否则 100 |
| `dnsbl` | 1 | 需要 `DNSBLChecked` 非空；`raw` = 已查询且命中的 zone 数，`clean = 100 - 34×命中数`（下限 0） |

`torproject`（Tor 角色）与 `geo_country`（国家）**不是评分源**，只参与旗标、IP 类型与身份字段。

公式：

```
base  = round( Σ(weight × clean) / Σweight )        # 只对"有分量"的数据源求和
score = clamp(base + penaltyPoints, 0, 100)
```

**"缺数据 ≠ 满分"**：`componentScore` 在数据源没有给出评分值时返回 `ok=false`，该数据源被整个丢出加权平均——
既不加一颗"100 分"，也不扣分。后果是 `coverage`（覆盖度）下降、`confidence` 下降；如果**一个分量都没有**，
`purity_score` 保持 `NULL`、`purity_band` 为 `unknown`、`verdict` 为 `pending`，并且**平台准入会拒绝该节点**（§3.6）。

### 3.3 旗标与扣分

10 个旗标位（`Flags`，位值属于 `ip_assessment.flags` 契约，不可改）：`compromised`、`tor`、`vpn`、`proxy`、
`scraper`、`abuse`、`anonymous`、`hosting`、`dnsbl`、`attack_history`。

扣分（每个旗标**最多扣一次**，不因多个数据源重复报告而叠加）：

| 旗标 | 扣分 | 触发条件 |
|---|---|---|
| `compromised` | −40 | 任一数据源 `Signals.Compromised` |
| `tor` | −30 | 任一数据源 `Signals.Tor`，或 `torproject` 名录里带 `exit` 角色 |
| `vpn` | −10 | 任一数据源 `Signals.VPN` |
| `proxy` | −10 | 任一数据源 `Signals.Proxy` |
| `scraper` | −10 | 任一数据源 `Signals.Scraper` |
| `abuse` | −10 | `abuseipdb` 的 `AbuseConfidence ≥ 25` |

注意：`anonymous`、`hosting`、`attack_history`、`dnsbl` **不直接扣分**（DNSBL 的惩罚在它自己的组件分里，
每个命中 zone −34）。它们通过下面第 3 步的判定表把节点送进 `review`。
`AbuseConfidence ≥ 75` 且 `TotalReports > 0` 时额外记 `RECENT_ABUSE` 并把判定拉到 `high_risk`。

### 3.4 IP 类型投票（`voteIPType`）

投票者与取票方式：`proxycheck`/`proxycheck_node`（规范化后的 `ip_type`）、`ipqs`（先看 `connection_type`，
再看 `ip_type`）、`ipapi_is`（先看 `ip_type`，再看 `source_type` 的 `isp`/`business`/`hosting`）、
`ip_api`（`ip_type`）、`ippure`（`isResidential` 为真 → residential，否则 non_residential）。

规则：

- `non_residential` **只有它是唯一票源时才获胜**，否则被剔除后重新计票。
- 一张票都没有时：ASN 命中内置云厂商表（`internal/intel/assess/hosting_asn.go` `hostingASNs`，AWS/GCP/Azure/
  Oracle/阿里/腾讯/华为/DigitalOcean/Linode/Vultr/Hetzner/OVH/Cloudflare）→ `datacenter` 并记
  `ASN_HOSTING_HEURISTIC`；否则 `unknown` 并记 `IP_TYPE_UNKNOWN`。
- 平票时的确定性顺序是固定数组（residential → mobile → business → wireless → datacenter），
  且**优先非 datacenter 一侧**（避免把住宅/移动 IP 判成机房）。
- **冲突判定**：`residential+mobile` 与 `datacenter` 都拿到票，且最高票数 < 总票数的 2/3 → `conflicting`
  并记 `IP_TYPE_CONFLICTING`。

`native` 维度：优先用 `ippure` 的 `isBroadcast` 取反；否则用离线库
（`maxmind_geolite2`/`dbip_lite`/`ipinfo_lite`/`geo_country`）的 `country_code == registered_country` 比较，
命中时记 `NATIVE_HEURISTIC`；两者都拿不到就是 `unknown`（不是 `false`）。

### 3.5 置信度（`coverageAndAgreement` + `confidence`）

```
coverage  = Σ(已产生分量的权重) / Σ(已启用评分源的权重)     # 上限 1
agreement = 1 − min(1, 标准差(各分量 clean) / 50)
```

| 条件 | confidence |
|---|---|
| 分量数 = 0 | `none` |
| 分量数 ≥ 2 且 `coverage ≥ 0.6` 且 `agreement ≥ 0.7` | `high` |
| 分量数 ≥ 2 **或** `coverage ≥ 0.4` | `medium` |
| 其余 | `low` |

`state`：有分量 → `valid`；没有分量时由 `noEvidenceState` 决定——没有启用任何评分源 → `unsupported`
（`NO_SCORING_SOURCE`）；启用的评分源**全部**有失败记录 → `unsupported`（`ALL_ENABLED_SOURCES_FAILED`）；
其余 → `pending`（`NO_VALID_EVIDENCE`）。`valid_until` 取所有有效证据里**最早**的过期时间；一条证据都没有时
默认是 `now + 1h`。

### 3.6 分档与判定

**分档**（`internal/quality/model.go` `PurityBand`，也是 `intel.BandName` 的取值）：

| 分数 | band |
|---|---|
| ≥ 95 | `excellent` |
| ≥ 90 | `clean` |
| ≥ 80 | `fair` |
| ≥ 60 | `mixed` |
| 其余（含 `NULL`） | `poor` / `unknown`（`NULL` 或越界 → `unknown`） |

**判定**（`verdict`，**按下列顺序取第一个命中**；取值与 `internal/intel/snapshot.go` `verdictNames` 一致：
`pending`/`favorable`/`caution`/`incomplete`/`review`/`conflicting`/`high_risk`）：

1. `high_risk`：`compromised` 旗标；或 `torproject` 角色含 `exit`；或 `tor` 旗标；或
   `AbuseConfidence ≥ 75` 且 `TotalReports > 0`；或 **分数 < 60**（附 `LOW_PURITY`）。
2. `conflicting`：IP 类型为 `conflicting`。
3. `review`：`proxy`/`vpn`/`scraper`/`anonymous` 任一旗标；或 `attack_history`；或 `dnsbl`；
   或 `torproject` 有任意角色（非 exit 的中继/守卫也是匿名信号）。
4. `pending`：没有分数。
5. `incomplete`：`confidence=low` 且 `coverage < 0.3`。
6. `caution`：分数 < 80。
7. `favorable`：其余。

### 3.7 fail-closed 准入（平台侧）

准入在 `internal/platform/quality_policy.go` `AdmitQuality`/`qualityRules`：**空策略永远放行**（复刻上游 Resin 行为，
偏差 X5）；**非空策略是 fail-closed 的**，"拿不准"一律拒绝。规则按序求值，第一条失败就是拒绝原因：

| # | 规则 | 拒绝原因 | fail-closed 之处 |
|---|---|---|---|
| 1 | `quality.egress` | `QUALITY_EGRESS_UNKNOWN` | 没有观测到的出口 IP 直接拒绝（后续规则不再求值） |
| 2 | `quality.unknown` | `QUALITY_UNKNOWN` | state 不是 `valid`/`conflicting` 时，**只有 `unknown_action: "allow"` 才放行**；空值等价于 exclude |
| 3 | `quality.exclude_tor` | `QUALITY_TOR` | `exclude_tor` 为 nil 视为 true |
| 4 | `quality.exclude_high_risk` | `QUALITY_HIGH_RISK` | `exclude_high_risk` 为 nil 视为 true |
| 5 | `quality.allowed_verdicts` | `QUALITY_VERDICT` | 判定不在白名单即拒绝 |
| 6 | `quality.min_purity` | `QUALITY_MIN_PURITY` | **`NULL` 分数永不满足配置的最低分**（detail 写 `score=unknown<min`） |
| 7 | `quality.ip_types` | `QUALITY_IP_TYPE` | — |
| 8 | `quality.min_confidence` | `QUALITY_CONFIDENCE` | — |
| 9 | `quality.require_native` | `QUALITY_NATIVE` | **缺证据永不满足**，必须显式 `native=true` |
| 10 | `quality.check.<id>` | `QUALITY_CHECK:<id>` | 结果缺失或过期即拒绝 |
| 11 | `quality.max_assessment_age` | `QUALITY_STALE` | **没有时间戳的评分无法证明新鲜，判失败** |
| 12 | `quality.max_egress_age` | `QUALITY_EGRESS_STALE` | 同上 |

准入的失败原因由 `GET /api/v1/platforms/{id}/nodes/{hash}/explain` 逐条列出（规则名 + detail），
所以"某节点为什么被排除"是可解释的。

一句话总结：**没有评分不是通过，而是拒绝**——除非策略里显式写了 `unknown_action: "allow"`。

## 4. 解锁检测规则怎么写

### 4.1 YAML 结构

内置规则 embed 在 `internal/intel/checks/builtin/*.yaml`，解码器在 `internal/intel/checks/rule.go`（`Rule` 结构体）。
**未知字段会被拒绝**（`decoder.KnownFields(true)`），打错字不会静默忽略。

```yaml
id: claude                 # 必填，^[a-z0-9_]+$，且用户规则必须与文件名同名
version: 3                 # 必填，正整数；与已存结果的版本不同即视为过期
name: Claude               # 必填
category: ai               # 必填：ai|video|music|social|search|mail|other
enabled: true              # 内置默认；可被 PATCH /api/v1/intel/checks/{id} 覆盖
ttl: 24h                   # 结果有效期，默认 24h
timeout: 10s               # 整条规则的超时，默认 10s
calibrated: 'calibrated 2026-09-25 …'   # 校准说明，纯元数据，会出现在 GET /api/v1/intel/checks
steps:
  - id: home               # 步骤 id，同样受 ^[a-z0-9_]+$ 约束，规则内唯一
    request:               # HTTP 步骤
      method: GET
      url: https://claude.ai/     # 必须是绝对 http(s) URL
      follow_redirects: false     # 默认 false；false 时 Location 头保留在响应里
      headers: {Accept-Language: "en-US,en;q=0.9"}
      max_body_bytes: 262144      # 0 表示默认 256 KiB，上限也是 256 KiB
  - id: smtp
    tcp: {host: smtp.gmail.com, port: 25}    # 每个步骤二选一：request 或 tcp，不能都给也不能都不给
outcomes:                  # 按顺序匹配，第一条命中即采用
  - when: {step: home, header_contains: {Location: "app-unavailable-in-region"}}
    outcome: blocked
  - when: {step: home, status_in: [403, 503], body_contains: "challenge-platform"}
    outcome: captcha
  - when: {step: home, status_in: [200, 301, 302, 307]}
    outcome: available
default: unknown           # 可选，默认 unknown
region:                    # 可选
  step: home
  body_regex: '"countryCode":"([A-Z]{2})"'
  header_regex: {Location: "/([a-z]{2})-en/"}    # 二选一即可，至少给一个
```

`outcome` 的取值只有 6 个（`ValidOutcomes`）：`available`、`blocked`、`region_limited`、`captcha`、`error`、`unknown`。

边界（`rule.go` 的常量与 `Validate`）：步骤 1–8 个（`MaxSteps`）；`outcomes` 至多 32 条（`MaxOutcomes`）；
`default` 必属 6 个取值之一；`when.error` 只能是 `timeout|refused|tls|any`；`when.step` 与 `region.step`
引用的步骤必须存在。执行侧的固定行为：每个步骤都通过**绑定被测节点的 HTTP client** 发出（`nodeClient`），
默认不跟随重定向、响应体最多 256 KiB（超限截断并在 detail 标 `truncated`）、整条规则共用一个 `timeout`；
同一节点同一时刻只跑 1 个检测，同一条规则全局并发默认 2（`intel_check_concurrency_per_check`）。

### 4.2 `when` 的匹配器

| 匹配器 | 语义 |
|---|---|
| `step` | 这条条件针对哪个步骤 |
| `status_in` / `status_not_in` | 状态码属于 / 不属于集合 |
| `header_contains: {Name: 子串}` | 头存在，且值**大小写不敏感地包含**子串 |
| `header_regex: {Name: 正则}` | 头值与正则匹配 |
| `body_contains` / `body_not_contains` | 响应体**大小写敏感**地包含 / 不包含 |
| `body_regex` | 响应体匹配正则 |
| `redirect_host` | `Location` 头的 host 与给定值**大小写不敏感地全等**（去 scheme、去端口） |
| `connected: true\|false` | 仅 tcp 步骤：连上 / 没连上 |
| `error: timeout\|refused\|tls\|any` | 拨号/传输错误分类；`any` 表示"任何错误或不可用" |

两条必须记住的语义：

1. **一个 `when` 只能引用一个 step**。同一条条件里的所有匹配器是 AND，但它们共享同一个 `step` 字段——
   想用"步骤 A 的 403 **且** 步骤 B 的正文命中"是做不到的（`When` 只有一个 `Step`）。
   Netflix 规则就是因为这个约束拆成"先用 original 排除彻底不可用，再用 licensed 区分 available 与 region_limited"，
   并把这一点写在文件头注释里。
2. `step` **留空**时，条件会对**每一个**步骤求值，任一命中即为真（`matches`）。这是给 tcp 的
   `connected`/`error` 准备的兜底写法，不适合表达"两个步骤都要满足"。

### 4.3 `region`：地区码怎么取

- `region.step` 指定从哪个步骤取；**至少要给 `body_regex` 或 `header_regex` 之一**，否则规则校验失败。
- **提取规则**：正则必须有**至少一个捕获组**；引擎只读**第一个捕获组**（`regionFromMatch` 取 `match[1]`），
  多余的捕获组被忽略（向后兼容旧规则）；结果会被转成大写并去空白，**必须恰好是两个 A–Z 字母**，否则视为没取到。
- **优先级**：先 `body_regex`，再 `header_regex`（多个头时按头名排序后依次尝试，保证确定性）。
- `header_regex` 是给"地区只出现在响应头里"的场景准备的。**Netflix 就是这种情况**：301 的响应体是 0 字节，
  地区编码在 `Location: https://www.netflix.com/jp-en/title/<id>` 的 `/jp-en/` 前缀里，
  所以规则用 `header_regex: {Location: "/([a-z]{2})-en/"}`，而 `body_regex` 无法命中。
- 已知限制：如果 `region.step` 留空，引擎取不到该 key 时会**任选 map 里的一个步骤**（Go map 迭代无序），
  结果不确定。写规则时请始终显式给出 `region.step`。

### 4.4 用户自定义规则放哪里

- 目录：`$PRISM_STATE_DIR/checks.d/`（`internal/intel/checks/engine.go` `UserRuleDir`）。
- 文件名约束：必须是 `<id>.yaml` 或 `<id>.yml`，其中 `<id>` 匹配 `^[a-z0-9_]+$`（`ValidateUserFileName`）；
  并且 YAML 里的 `id` **必须等于文件名**，否则该文件被跳过并出现在 `GET /api/v1/intel/checks` 的
  `load_errors` 里（`LoadDir`）。
- 与内置规则同 id 时**用户规则覆盖内置规则**（`Reload`）。
- 热加载：最多每 60 秒检查一次文件变化（`DefaultHotReload`），不需要重启。
- 一个写坏的文件只报错并跳过，不会让整个引擎失效。

### 4.5 8 条内置规则与校准状态

`calibrated` 字段的原值直接取自各 YAML（`internal/intel/checks/builtin/`），依据来自文件头注释里的实测结论。
校准日期均为 **2026-09-25**，采集方式是"本机经日本数据中心代理"。

| id | `calibrated` 字段 | 校准依据与当前判定 |
|---|---|---|
| `netflix` | `calibrated 2026-09-25 (real 301 with an empty body; region read from the Location "/jp-en/" prefix)` | 两个 title（自制剧 80100172、非自制剧 70143836）**都返回 301 + Location `/jp-en/`，响应体 0 字节**。先按 `original` 排除 403/451 与非 200/301/302/307，再看 `licensed`：200/301/302/307 → available，403/404 → region_limited。地区走 `header_regex`。 |
| `tiktok` | `calibrated 2026-09-25 (real 200 with a 1462-byte SlardarWAF challenge page)` | `https://www.tiktok.com/` 返回 **200 但正文只有 1462 字节的 WAF 挑战页**（`SlardarWAF`、`_wafchallengeid`、"Please wait..."）。两条挑战标记**必须排在 `status_in: [200]` 之前**，否则可用节点会被误判成 available。地区取自正文 `"region":"XX"`（校准注释只记录了挑战页标记，没有说明该页是否含 `region` 字段，所以这条取地区的能力未被本轮实测证实）。 |
| `chatgpt` | `calibrated 2026-09-25 (real 403 with "type":"dc" on ios.chat.openai.com; trace 200 with loc=US)` | `trace` 步骤请求 `chatgpt.com/cdn-cgi/trace` 取 `loc=`；`probe` 步骤请求 `ios.chat.openai.com/`，实测 403 且正文含 **`"type":"dc"`（数据中心 IP 被拒）**，该条排在裸 `status_in: [403]` 之前。`unsupported_country` → blocked；`"VPN"` → captcha（**本轮未观测到，保留为兜底**）。地区用 `body_regex: '(?m)^loc=([A-Z]{2})$'`。 |
| `claude` | `calibrated 2026-09-25 (real 403 with a 5656-byte challenge-platform body on claude.ai)` | `https://claude.ai/` 实测返回 403、正文 5656 字节，含 Cloudflare 挑战脚本 URL 片段 `challenge-platform`；所以 `status_in: [403,503]` + `body_contains: "challenge-platform"` → captcha 成立（不跟随重定向）。`Location: app-unavailable-in-region` 分支**本轮未观测到，保留为兜底**。 |
| `youtube_premium` | `calibrated 2026-09-25 (real 200 with "countryCode":"JP" on youtube.com/premium)` | `https://www.youtube.com/premium`（`Accept-Language: en`）实测 200 且正文含 `"countryCode":"JP"`，地区提取成立（`body_regex` 取第一个捕获组）。`"Premium is not available in your country"` 分支**本轮未观测到，保留为兜底**；403/429 → blocked，200 → available。 |
| `google_captcha` | `calibrated 2026-09-25 (real 200 without a /sorry/ redirect on google.com/search)` | `google.com/search?q=prism&hl=en` **不跟随重定向**，实测 200 且 `Location` 里没有 `/sorry/`。429 → captcha；`header_regex: {Location: "/sorry/"}` → captcha；200 → available（**本轮未观测到 429/挑战页**）。 |
| `gemini` | `calibrated 2026-09-25 reachability only (no region marker in the real 200 response)` | `https://gemini.google.com/` 实测 200、正文 848704 字节，**既没有 `countryCode` 也没有任何地区提示**，因此**只做可达性判定**：403/451 → blocked，429 → captcha，200 → available。**该规则没有 `region` 块，也不判地区**——这是核过的事实，不是遗漏。 |
| `smtp25` | `structural (connectivity only)` | tcp 连接 `smtp.gmail.com:25`：`connected: true` → available，`false` → blocked，`timeout` → error。只看 25 端口通不通，**不判地区**，与 HTTP 响应体无关。 |

`GET /api/v1/intel/checks` 会返回每条规则的 `source`（`builtin` 或 `user`）、`steps`、`ttl`、`timeout` 与 `calibrated`，
这是判断"某条规则到底校准过没有"的权威来源。开/关某条规则用
`PATCH /api/v1/intel/checks/{id}`，体为 `{"enabled":bool}`，持久化为 `intel_provider_settings` 里
`provider_id = "check:<id>"` 的行，改动立即生效、无需重启。

## 5. 如何开关自动检测

两个运行时设置（`internal/config/runtime.go`，默认值见 `NewDefaultRuntimeConfig`）：

| 设置 | 默认 | 效果 |
|---|---|---|
| `intel_enabled` | **true** | intel 子系统的总开关。为 false 时：`jobs.Manager.Create` 返回 `ErrDisabled`，`POST /api/v1/intel/jobs` 直接 409 `intel jobs are disabled (intel_enabled=false)`；自动入队返回 `ErrAutoEnqueueDisabled`；已经在跑的 item 的离线/联网/via-node/解锁检测步骤各自返回具名 skip。注意第 1 步（出口探测）与第 6 步（评分）本身不查这个开关。 |
| `intel_auto_checks` | **false** | 决定订阅触发的自动任务种类。`false` → `jobs.SubscriptionKindOf(false)` = **`kind=intel`**，即**只采集数据源与评分，不跑解锁检测**；`true` → **`kind=full`**，才包含第 5 步解锁检测。 |

自动任务还需要**该订阅自己的 `auto_intel` 为真**（`subscriptions.auto_intel`，迁移 `000011`，默认 true，
可用 `POST /api/v1/subscriptions` 或 `PATCH /api/v1/subscriptions/{id}` 的 `auto_intel` 字段修改；
`internal/intel/autoenqueue.go` `AutoEnqueueSubscriptions`）。`auto_intel=false` 的订阅永不产生自动 intel 任务。
三个开关的关系：`intel_enabled=false` 一票否决；`auto_intel=false` 让该订阅出局；两者都为真时，
任务种类由 `intel_auto_checks` 决定。

用 curl 打开（`PATCH /api/v1/system/config`，允许字段见 `internal/service/control_plane_system.go`
的 `runtimeConfigAllowedFields`，intel 相关的是 `intel_enabled`、`intel_node_workers`、
`intel_check_concurrency_per_check`、`intel_max_running_jobs`、`intel_auto_checks`、`intel_refresh_schedule`）：

```sh
# 打开解锁检测（会和数据源采集一起跑）
curl -X PATCH http://127.0.0.1:2260/api/v1/system/config \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"intel_auto_checks": true}'

# 完全停掉自动检测（不再创建任何自动任务）
curl -X PATCH http://127.0.0.1:2260/api/v1/system/config \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"intel_enabled": false}'
```

这个 PATCH **不是 RFC 7396 的 JSON Merge Patch**：请求体必须是非空对象，`null` 值被拒绝，
未知或只读字段被拒绝（`unknown or read-only field: "…"`），改动立即作用于运行中的服务并写进 state.db。
只关掉**一条**检测规则则用 `PATCH /api/v1/intel/checks/{id}` 配 `{"enabled": false}`，与上面的开关互不影响。

设置页面的路径是 `/#/intel-settings`（`internal/api/web/src/app/routes.tsx`），
在那里可以看到每个数据源的条款原文（`terms`）与今日用量，并能直接开关数据源与检测规则。

## 6. IPPure 条款提示（原文照录要点）

`ippure` 是 **via-node** 数据源：请求经被测节点自己的出口发出，IPPure 只看到**那个节点的地址**，
所以它的匿名额度属于**节点**而不是 Prism 主机。Prism 因此：

- 按**节点**计日额度，默认 **500/天**（`Spec.DefaultDailyLimit`）；
- 另有一个 **provider 级** QPS 阀，默认 **2/s**，只用来防止整池节点同时到达上游；
- 把完整条款写进 `Spec.Terms`，运行设置页会原样显示：

> IPPure's terms may restrict bulk or systematic use. The query runs through the node, so the vendor sees
> that node's address and the quota belongs to it: Prism counts a per-node daily budget (default 500) and
> keeps a provider-wide QPS valve (default 2/s) so the whole inventory never arrives at once. Check the
> vendor conditions yourself before raising these limits.

即：**IPPure 的条款可能限制批量或系统性使用；Prism 默认低频调用，提高额度前请自行确认厂商条件。**
另外 IPPure 的 `fraudScore` 只对 IPv4 有效：IPv6 响应里的分数会被显式忽略（`DecodeIPPure`），
评分层也不会给 IPv6 生成 IPPure 分量——不会因此变成"满分"。

## 7. 手动批量检测

端点：`POST /api/v1/intel/jobs`（`internal/api/server.go`）。请求体是 `jobs.Request`：

```json
{
  "kind": "full",
  "scope": {
    "all": true,
    "subscription_ids": ["sub-1"],
    "platform_ids": ["plat-1"],
    "node_hashes": ["<32 位 hex>"],
    "filter": {"region": "jp", "purity_band": "clean", "healthy": "true"}
  },
  "providers": ["ippure", "proxycheck_node"],
  "checks": ["netflix", "chatgpt"],
  "force": false
}
```

- `kind` 必填（`egress`/`intel`/`checks`/`full`）；`providers`/`checks` 为空表示"全部已启用"。
- `force: true` 表示即使已有有效证据也重新查询（联网队列阶段）。
- 优先级由服务端固定（手动任务 = `PriorityManual` 100），请求体里没有 `priority` 字段。
- 作用域是**并集**（`internal/intel/jobs/jobs.go` `Scope`）：`all`、`subscription_ids`、`platform_ids`、
  `node_hashes`、`filter` 每个选择器贡献的节点合在一起、去重、排序。上限 `MaxNodesPerJob = 200000`，
  超过时**报错而不是截断**（`job scope exceeds the node limit`）。展开在服务端完成
  （`cmd/prism/intel_runtime.go` `resolveIntelScope`）。

### 7.1 `filter` 的作用范围（容易搞错的一点）

`filter` 用来**收窄** `all` / `subscription_ids` / `platform_ids` 三种选择器选出来的节点：

- 带 `filter` 的 `subscription_ids` 含义是"该订阅里**满足过滤条件**的节点"，而不是"全池过滤结果 ∪ 该订阅全部节点"。
- 只给 `filter`（不给任何其他选择器）也会触发**全池遍历**，等价于 `all: true` + 过滤条件。
- **`filter` 不作用于显式 `node_hashes`**：按 hash 点名的节点直接采用，不参与过滤。

### 7.2 `filter` 支持的键

键名大小写不敏感（值会被 trim + 转小写后再比较），**未列出的键会被忽略**：

| 键 | 取值 | 说明 |
|---|---|---|
| `protocol` | 节点 outbound 类型 | 与节点列表同一个判定 |
| `engine` | 引擎 | — |
| `region` | 地区代码 | 优先用显式探测到的地区，回退到 GeoIP 查询；**地区未知的节点永远不匹配** |
| `ip_type` | `residential`/`mobile`/`business`/`wireless`/`datacenter`/`non_residential` | 读内存里的评分投影 |
| `purity_band` | `excellent`/`clean`/`fair`/`mixed`/`poor`/`unknown`，另接受遗留名 **`review`**（匹配 `review` 与 `conflicting` 两种判定） | 读投影 |
| `verdict` | `favorable`/`caution`/`review`/`conflicting`/`high_risk`/`incomplete`/`pending`，**逗号分隔多值**（"任一"） | 读投影 |
| `healthy` | 只有 `"true"` 生效，其余值等于不加约束 | 用节点自身的健康判定 |

三个 purity 键（`ip_type`/`purity_band`/`verdict`）**必须能拿到该节点出口 IP 的评分投影**；
节点还没有出口观测、或没有评分时**一律不匹配**（与节点列表的 fail-closed 规则一致）。
`filter` 的求值只读内存状态（NodeEntry 的原子字段与按出口 IP 索引的评分投影），**不查数据库**。

任务创建后可以 `GET /api/v1/intel/jobs/{id}` 看进度、`GET /api/v1/intel/jobs/{id}/items` 看每个节点到了第几步、
`POST /api/v1/intel/jobs/{id}/actions/cancel` 取消、`POST /api/v1/intel/jobs/{id}/actions/retry-failed` 重试失败项，
`GET /api/v1/intel/jobs/{id}/events` 是 SSE 实时进度（每秒最多一帧）。

## 8. 已知限制与未确定的点

- `dnsbl` 不查 IPv6（`SupportsIPv6: false`），IPv6 地址记为 unsupported。
- `proxycheck` 与 `proxycheck_node` 一律逐个地址查询（`BatchSize = 1`）：v3 的批量能力未经厂商确认。
- `gemini` 规则**只判可达性、不判地区**，因为实测响应里没有任何地区判据；`smtp25` 同理只判 25 端口连通性。
  这两条规则的"地区"字段为空是当前事实，不是待办。
- `region.step` 留空时取地区的结果不确定（任选一个步骤），见 §4.3。
- 评分的覆盖度分母取自**主机侧** `proxycheck` 数据源的启用状态（`cmd/prism/intel_runtime.go`
  `intelEnabledSources` 用 `assess.ScoringSources()`，里面只有 `proxycheck`，不含别名 `proxycheck_node`）。
  因此"只开 `proxycheck_node`、关掉 `proxycheck`"时，别名分量仍会以 3 的权重出现在分子里，
  而分母不包含这 3 ——`coverage` 会被 1.0 的上限截断，可能高估覆盖度。这是一处**代码内部的不一致**
  （评分层的别名折叠与覆盖度分母的口径没对齐），**未确定**它是否为有意行为；不影响分数本身，只影响
  `coverage` 与由它推导的 `incomplete` 判定和 `min_confidence` 准入。
- 第 1 步（出口探测）与第 6 步（评分）不检查 `intel_enabled`：开关只拦"创建新任务"与各 provider 步骤。
  一个在 `intel_enabled=false` 之前就已排队的 item 仍可能完成出口探测与评分。
- 本文不覆盖：intel.db 的表结构与清理策略、SSE 的背压细节、离线库的下载地址与校验流程
  （后者在 `internal/intel/providers/geo_db.go`，本文只列 TTL 与条款）。