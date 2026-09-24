# WP08 · intel.db 持久化与批量任务系统

**前置**：WP03。**目标**：让出口信息、各数据源证据、经节点检测、解锁结果、评估结果、数据源预算和任务全部持久化到 `intel.db`，并提供可续跑、可取消、可观察进度的批量任务。旧的 `internal/inspection` 内存方案由本 WP 与 WP09 替换。

## 1. 包结构

```text
internal/intel/
  store/        intel.db 打开、迁移（embed SQL）、仓储方法
  jobs/         任务创建、节点工作池、数据源队列工作者、SSE 广播、清理
  egress/       出口探测增强（v4/v6/colo），复用 outbound.Manager.FetchWithOptions
  providers/    数据源实现（WP09）
  checks/       解锁检测规则引擎（WP09）
  assess/       评估算法（WP10）
  snapshot.go   内存投影（从 intel.db 加载，路由与列表只读它）
  service.go    对外门面：给 service/api 与 cmd 使用
```

## 2. intel.db 迁移 `000001_intel_base.up.sql`

文件路径为 `$PRISM_STATE_DIR/intel.db`。打开时设置：`journal_mode=WAL`、`synchronous=NORMAL`、`busy_timeout=5000`，并且 `SetMaxOpenConns(1)`。

```sql
CREATE TABLE node_egress (
    node_hash      TEXT PRIMARY KEY,
    ipv4           TEXT NOT NULL DEFAULT '',
    ipv6           TEXT NOT NULL DEFAULT '',
    colo           TEXT NOT NULL DEFAULT '',   -- Cloudflare 接入点，如 NRT
    loc            TEXT NOT NULL DEFAULT '',   -- trace 的 loc，如 JP
    v4_observed_ns INTEGER NOT NULL DEFAULT 0,
    v6_observed_ns INTEGER NOT NULL DEFAULT 0,
    v6_checked_ns  INTEGER NOT NULL DEFAULT 0  -- 最近一次尝试 v6 的时间（无 v6 时 ipv6=''）
);
CREATE INDEX idx_node_egress_v4 ON node_egress(ipv4);
CREATE INDEX idx_node_egress_v6 ON node_egress(ipv6);

CREATE TABLE egress_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    node_hash      TEXT NOT NULL,
    family         INTEGER NOT NULL,           -- 4|6
    ip             TEXT NOT NULL,
    observed_at_ns INTEGER NOT NULL
);
CREATE INDEX idx_egress_history_node ON egress_history(node_hash, observed_at_ns DESC);

CREATE TABLE evidence (
    ip             TEXT NOT NULL,
    provider       TEXT NOT NULL,
    profile        TEXT NOT NULL,              -- 标准化版本，如 proxycheck-v3-2
    via_node_hash  TEXT NOT NULL DEFAULT '',   -- 经节点数据源记录所用节点
    status         TEXT NOT NULL,              -- ok|error|unsupported
    observed_at_ns INTEGER NOT NULL,
    valid_until_ns INTEGER NOT NULL,
    normalized_json TEXT NOT NULL,             -- quality.Evidence，≤ 8 KiB
    raw_json       TEXT,                       -- 原始响应，≤ 32 KiB；数据源设置 store_raw=false 时为 NULL
    error_code     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (ip, provider)
);
CREATE INDEX idx_evidence_valid ON evidence(valid_until_ns);

CREATE TABLE node_checks (
    node_hash      TEXT NOT NULL,
    check_id       TEXT NOT NULL,
    check_version  INTEGER NOT NULL,
    egress_ip      TEXT NOT NULL,
    outcome        TEXT NOT NULL,              -- available|blocked|region_limited|captcha|error|unknown
    region         TEXT NOT NULL DEFAULT '',
    detail_json    TEXT NOT NULL DEFAULT '{}', -- ≤ 4 KiB
    latency_ms     INTEGER NOT NULL DEFAULT 0,
    observed_at_ns INTEGER NOT NULL,
    valid_until_ns INTEGER NOT NULL,
    PRIMARY KEY (node_hash, check_id)
);
CREATE INDEX idx_node_checks_ip ON node_checks(egress_ip);

CREATE TABLE ip_assessment (
    ip              TEXT PRIMARY KEY,
    profile         TEXT NOT NULL,             -- prism-purity-v2
    state           TEXT NOT NULL,             -- valid|partial|pending|stale|unsupported
    verdict         TEXT NOT NULL,
    purity_score    INTEGER,                   -- NULL = 未知
    purity_band     TEXT NOT NULL,
    confidence      TEXT NOT NULL,             -- none|low|medium|high
    coverage        REAL NOT NULL,
    ip_type         TEXT NOT NULL,
    native          INTEGER,                   -- NULL 未知 / 0 / 1
    flags           INTEGER NOT NULL DEFAULT 0,
    asn             INTEGER,
    as_org          TEXT NOT NULL DEFAULT '',
    country         TEXT NOT NULL DEFAULT '',
    city            TEXT NOT NULL DEFAULT '',
    reasons_json    TEXT NOT NULL,
    components_json TEXT NOT NULL,
    computed_at_ns  INTEGER NOT NULL,
    valid_until_ns  INTEGER NOT NULL
);
CREATE INDEX idx_ip_assessment_valid ON ip_assessment(valid_until_ns);

CREATE TABLE provider_state (
    provider           TEXT PRIMARY KEY,
    day                TEXT NOT NULL,          -- UTC YYYY-MM-DD
    used               INTEGER NOT NULL DEFAULT 0,
    next_request_at_ns INTEGER NOT NULL DEFAULT 0,
    blocked_until_ns   INTEGER NOT NULL DEFAULT 0,
    paused             INTEGER NOT NULL DEFAULT 0,
    error_code         TEXT NOT NULL DEFAULT '',
    credential_id      TEXT NOT NULL DEFAULT '' -- sha256(provider+key)，Key 变化时自动解除 paused
);

CREATE TABLE provider_queue (
    provider       TEXT NOT NULL,
    ip             TEXT NOT NULL,
    priority       INTEGER NOT NULL,
    job_id         TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL,              -- queued|running|done|failed
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_run_at_ns INTEGER NOT NULL,
    lease_owner    TEXT NOT NULL DEFAULT '',
    lease_until_ns INTEGER NOT NULL DEFAULT 0,
    error_code     TEXT NOT NULL DEFAULT '',
    enqueued_at_ns INTEGER NOT NULL,
    PRIMARY KEY (provider, ip)
);
CREATE INDEX idx_provider_queue_claim ON provider_queue(provider, status, next_run_at_ns, priority DESC);

CREATE TABLE jobs (
    id             TEXT PRIMARY KEY,           -- uuid
    kind           TEXT NOT NULL,              -- egress|intel|checks|full
    status         TEXT NOT NULL,              -- queued|running|succeeded|partial|failed|canceled
    priority       INTEGER NOT NULL,
    request_json   TEXT NOT NULL,              -- 原始请求（scope/providers/checks/force）
    total          INTEGER NOT NULL DEFAULT 0,
    done           INTEGER NOT NULL DEFAULT 0,
    failed         INTEGER NOT NULL DEFAULT 0,
    skipped        INTEGER NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL,              -- admin|system:subscription:<id>|system:refresh
    created_at_ns  INTEGER NOT NULL,
    started_at_ns  INTEGER NOT NULL DEFAULT 0,
    finished_at_ns INTEGER NOT NULL DEFAULT 0,
    error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_jobs_status ON jobs(status, priority DESC, created_at_ns);

CREATE TABLE job_items (
    job_id         TEXT NOT NULL,
    node_hash      TEXT NOT NULL,
    status         TEXT NOT NULL,              -- queued|running|done|failed|skipped|canceled
    step_index     INTEGER NOT NULL DEFAULT 0, -- 流水线断点，用于续跑
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_run_at_ns INTEGER NOT NULL,
    lease_owner    TEXT NOT NULL DEFAULT '',
    lease_until_ns INTEGER NOT NULL DEFAULT 0,
    result_json    TEXT NOT NULL DEFAULT '{}', -- 每步摘要，≤ 4 KiB
    error_code     TEXT NOT NULL DEFAULT '',
    updated_at_ns  INTEGER NOT NULL,
    PRIMARY KEY (job_id, node_hash)
);
CREATE INDEX idx_job_items_claim ON job_items(status, next_run_at_ns);
```

## 3. 任务模型

### 3.1 创建：`POST /api/v1/intel/jobs`

```json
{
  "kind": "full",
  "scope": {
    "all": false,
    "subscription_ids": [],
    "platform_ids": [],
    "node_hashes": [],
    "filter": {"region":"jp","protocol":"vless","engine":"singbox","purity_band":"unknown"}
  },
  "providers": ["proxycheck","ippure"],
  "checks": ["chatgpt","netflix"],
  "force": false
}
```

- `scope` 中各项取**并集**。`filter` 的键与 `GET /api/v1/nodes` 的查询参数一致，在服务端展开为节点列表。
- 单个任务最多 200,000 个节点，超过时返回 `400 INVALID_ARGUMENT`。
- 如果运行中和排队中的任务合计已达 `intel_max_running_jobs`（默认 2），新任务照常创建，但状态为 `queued`。
- 成功时返回 `202 {job}`。

### 3.2 各 kind 的节点流水线

每个 kind 按下表顺序执行步骤，`step_index` 用于断点续跑：

| kind | 步骤 |
|---|---|
| egress | 1 出口探测 |
| intel | 1 出口探测 → 2 离线补全 → 3 在线数据源入队 → 4 经节点数据源 → 6 评估 |
| checks | 1 出口探测（15 分钟内已探测则跳过） → 5 解锁检测 → 6 评估 |
| full | 1 → 2 → 3 → 4 → 5 → 6 |

各步骤说明：

1. **出口探测**：通过节点请求以下地址（`outbound.Manager.FetchWithOptions`，超时 15 秒）：
   - `https://1.1.1.1/cdn-cgi/trace`，得到 IPv4；
   - `https://[2606:4700:4700::1111]/cdn-cgi/trace`，得到 IPv6（失败时记为无 v6，属于正常情况）。

   解析 `ip=`、`loc=`、`colo=` 三个字段。同时调用现有的 `probeMgr.ProbeEgressSync(hash)`，保持 Resin 的 `egress_ip` 语义（cache.db）不变。实现时要验证 1.1.1.1 的证书包含上面两个 IP SAN；如果不包含，改用 `https://cloudflare.com/cdn-cgi/trace`，并按 A 或 AAAA 强制 IP 族。结果写入 `node_egress`，IP 发生变化时追加 `egress_history`。
2. **离线补全**：对 v4 和 v6 地址，依次调用所有 offline 类型的数据源（WP09），同步写入证据。
3. **在线数据源入队**：对每个 IP 和每个已启用的 online-ip 类型数据源，如果没有有效证据或者 `force=true`，就 `INSERT … ON CONFLICT(provider, ip) DO UPDATE SET priority=max(priority, 新优先级), status='queued'（若原状态为 done 或 failed 且需要刷新）`。**本步骤不等待结果**。
4. **经节点数据源**（例如 IPPure、ip-api-self）：
   - 预算或 QPS 不足时，**不要阻塞工作协程**：把 item 设回 `queued`，`next_run_at` 设为该数据源的 `next_request_at`，并保存 `step_index`，然后释放。
   - 数据源返回的 IP 必须与本次出口 IP 一致才写入证据；不一致时记为 `error_code=EGRESS_MISMATCH`。
5. **解锁检测**（WP09）：执行本任务选定的检测规则，结果写入 `node_checks`。
6. **评估**（WP10）：对 v4 和 v6 各计算一次，写入 `ip_assessment`，然后更新 snapshot 并调用 `pool.NotifyEgressIPDirty(ip)`。

### 3.3 数据源队列工作者

每个 online-ip 数据源一个协程，循环执行：

- 检查 `provider_state`：处于 paused、blocked 状态，或当日 used 已达 daily_limit，或还没到 `next_request_at` 时，睡到最早可用时间（最多 30 秒）后再检查。
- 按 `priority DESC, enqueued_at_ns` 领取不超过 BatchSize 条到期记录（设置租约，时长 2 分钟）。
- 调用 `Lookup`。成功时写入证据并把队列记录标为 done，然后触发该 IP 的评估。
- 失败时 `attempts+1`，按 `30s × 2^attempts` 退避（上限 6 小时），5 次后标为 failed。
- 收到 429 时设置 `blocked_until`；收到 401 或 403 时设置 `paused`。**同一事务**中更新 `provider_state.used` 和 `next_request_at`（按 QPS 推算）。

### 3.4 并发与上限

放在运行时配置中，可以通过 `PATCH /api/v1/system/config` 修改：

| 配置 | 默认值 | 说明 |
|---|---|---|
| `intel_enabled` | true | 为 false 时不自动建任务，手动创建返回 409 |
| `intel_node_workers` | 16 | 节点流水线并发（1–128） |
| `intel_check_concurrency_per_check` | 2 | 每条检测规则的全局并发，防止被目标站点封禁 |
| `intel_max_running_jobs` | 2 | 同时运行的任务数上限 |
| `intel_auto_checks` | false | 导入时自动任务是否包含解锁检测 |
| `intel_refresh_schedule` | `"0 4 * * *"` | cron；刷新已过期的证据与检测 |

- 单个 item 超时 90 秒，租约 3 分钟。

### 3.5 续跑、取消与重试

- **启动时**：把 `status='running'` 的 job_items 和 provider_queue 记录改回 `queued` 并清空租约；把 running 状态的 job 保持为 running，由工作池继续执行。
- **取消**：job 置为 canceled；queued 状态的 items 置为 canceled；正在运行的 item 在完成当前步骤后停止（每步开始前检查 job 状态）。
- **重试失败项**：把 failed 状态的 items 改回 queued，`attempts` 归零，job 改回 running。
- **完成判定**：所有 items 都处于终态（done、failed、skipped、canceled）时，job 置为 succeeded；如果存在 failed，则置为 partial。provider_queue 中等待额度的查询**不计入任务完成**，但在任务详情中显示 `pending_online_lookups`（按 `job_id` 统计）。

### 3.6 自动任务

- **订阅刷新新增节点**：如果订阅的 `auto_intel=1`，就创建任务 `{kind: intel_auto_checks ? "full" : "intel", scope: {node_hashes: 新增节点}, created_by: "system:subscription:<id>"}`，优先级 50。一次刷新新增的节点合并成一个任务。
- **定时刷新**：按 `intel_refresh_schedule` 创建一个 `created_by: "system:refresh"`、优先级 10 的任务，范围是符合以下条件的节点：存在已过期证据、已过期检测，或出口观测超过 48 小时。
- **手动任务**：优先级 100。

## 4. 周期探测的接线（由 WP03 的组装第 8 步调用）

```go
probeMgr.SetOnEgressObserved(func(ip netip.Addr) { intelSvc.ObserveEgress(ip) })
```

- 现有回调只传 IP。为了记录是哪个节点，改为传入 `(hash, ip, loc)`：把 `probe.SetOnEgressObserved` 的签名扩展为 `func(node.Hash, netip.Addr, string)`，在 `performEgressProbe` 中调用。
- 回调**只做非阻塞入队**：写入一个容量 4096 的 channel，满了就丢弃并计数。由单独的写协程批量写入 `node_egress` 和 `egress_history`。
- IP 发生变化时，为新 IP 在所有 online 数据源上入队（优先级 10），并在已有证据的范围内立即评估。

## 5. 内存投影（`snapshot.go`）

```go
type AssessmentLite struct {
    Score      int8   // -1 表示未知
    Band       uint8
    Verdict    uint8
    IPType     uint8
    Confidence uint8
    Native     int8   // -1 未知，0 否，1 是
    Flags      uint16
    ValidUntil int64
    ComputedAt int64
}
type Snapshot struct { /* map[netip.Addr]AssessmentLite + map[node.Hash]map[checkID]outcomeLite，读写锁保护 */ }
```

- 启动时从 `ip_assessment` 和 `node_checks` 全量加载，每条记录约 32 字节，10 万个 IP 约占 3 MB。
- 之后每次写库成功后同步更新；投影**只是缓存**，重启后可以完整重建。
- 路由的质量准入（WP10）和节点列表只读这份投影。

## 6. 清理（每天一次）

- 完成超过 7 天的任务：删除其 job_items；jobs 表保留 30 天。
- `egress_history`：每个节点只保留最近 50 条。
- `provider_queue`：删除 done 超过 1 天、failed 超过 7 天的记录。
- `evidence`、`node_checks`、`ip_assessment`：不再被任何现存节点使用、并且过期超过 30 天的 IP 或节点记录，直接删除。
- 每周执行一次 `PRAGMA optimize`；如果空闲页超过 20%，执行 `VACUUM`，用于控制库体积。

## 7. API

以下接口均需管理员鉴权：

| 方法与路径 | 说明 |
|---|---|
| `POST /api/v1/intel/jobs` | 创建任务（§3.1） |
| `GET /api/v1/intel/jobs?status=&limit=&offset=` | 任务列表 |
| `GET /api/v1/intel/jobs/{id}` | 任务详情，含计数和 `pending_online_lookups` |
| `GET /api/v1/intel/jobs/{id}/items?status=&limit=&offset=` | 任务项，含 `result_json` 和错误 |
| `GET /api/v1/intel/jobs/{id}/events` | SSE：每秒最多推送一次 `{"done":..,"failed":..,"skipped":..,"total":..,"status":..}`；任务结束时推送 `event: end`。只在内存中广播，断线后客户端重新拉取 GET 即可 |
| `POST /api/v1/intel/jobs/{id}/actions/cancel` | 取消 |
| `POST /api/v1/intel/jobs/{id}/actions/retry-failed` | 重试失败项 |
| `GET /api/v1/intel/status` | 各数据源状态（WP09 字段）、队列长度、`db_bytes`、丢弃计数 |
| `GET /api/v1/intel/nodes/{hash}` | 出口 v4/v6、colo、历史（最近 20 条）、检测结果、两个 IP 的评估摘要 |
| `GET /api/v1/intel/ip/{ip}` | 全部证据（标准化字段）、评估（含 components）、使用该 IP 的节点列表（最多 100 个） |

SSE 需要在 `RequestBodyLimitMiddleware` 和超时设置之外单独处理：设置 `Cache-Control: no-store`，并定期 `Flush`。由于浏览器的 `EventSource` 不能带 `Authorization` 头，**只有 `/api/v1/intel/jobs/{id}/events` 这一个接口**额外接受 `?access_token=<管理员令牌>`：服务端用常量时间比较，请求日志中对该参数脱敏，并计入 WP04 的登录失败限流。

## 8. 替换旧的 inspection

- 删除 `internal/inspection/manager.go`，也删除 `Store` 接口及其依赖。
- `proxycheck`、`abuseipdb`、`ippure` 的解码函数和 `tor_registry.go` 迁移到 `internal/intel/providers/`（WP09），原有测试随之迁移。
- 删除 `ControlPlaneService` 中的 `Inspection`、`IPPure`、`TorRegistry` 字段，改为 `Intel *intel.Service`。

## 9. 兼容旧的 `/api/v1/quality/*`（保留到前端迁移完成后，仍作为别名存在）

| 旧接口 | 新实现 |
|---|---|
| `GET /quality/status` | 由 intel 的数据源状态拼装。字段形状严格等于前端 `QualityStatus` 类型（见 WP04 §4.5）；`manual_sources` 中放经节点数据源（ippure），`registry_sources` 中放 torproject |
| `GET /quality/assessments?q=` | 读取 `ip_assessment` 并映射为 `quality.Summary` |
| `GET /quality/ip/{ip}` | 同上，只返回单个 IP |
| `POST /quality/ip/{ip}/actions/probe` | 为该 IP 在所有 online 数据源上强制入队（优先级 100），返回 202 |
| `POST /nodes/{hash}/actions/probe-quality` | 创建任务 `{kind:"intel", node_hashes:[hash], force:true}`，返回 202 和任务 ID |
| `POST /nodes/{hash}/actions/review-ippure` | 创建任务 `{kind:"intel", providers:["ippure"], node_hashes:[hash], force:true}`，最多等待 15 秒；在时限内完成就返回 IPPure 证据，否则返回 202 和任务 ID |

## 10. 测试

- store 层：迁移测试；各方法的 CRUD 测试；并发下单写连接串行化测试。
- 任务：
  - 创建、展开 scope、按优先级领取；
  - 断点续跑：在 `step_index=3` 时停止，重启后从第 4 步继续；
  - 取消、重试；
  - 完成判定，包括 partial。
- 数据源队列：预算耗尽后次日（用注入时钟模拟）自动恢复；429 触发 blocked，401 触发 paused；Key 变化后通过 `credential_id` 解除 paused。
- 出口：用 httptest 伪造 trace 响应，可以返回 v4 和 v6；v6 失败时视为无 v6；IP 变化时写入历史。
- 投影：重启后从库中重建的内容与重启前一致。
- 以上测试全部使用注入的时钟和伪造的数据源，**不访问网络**。

## 11. 验收

```sh
make verify
```

手工验收：

1. 导入 10 个本地测试节点（WP13 的本地服务端），创建 `kind=intel` 任务，观察 SSE 进度直到 100%。
2. 杀掉进程后重启，确认之前的证据、评估和任务记录都还在。
3. 在任务进行中杀掉进程再重启，确认任务能继续完成。
