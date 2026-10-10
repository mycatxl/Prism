# Prism 审计缺陷修复计划（AUDIT-FIX）

来源：`AUDIT_REPORT.md`（深度审计报告，26 条 + 加固项）
基线：`3657803` + 已完成的 8 项 intel 修复（未提交，40 项改动）
纪律：每项修复必须**先有失败测试**，修复后转正为回归测试；不扩大范围；不改无关文件。

`TAGS = with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy`

> **状态：本计划已全部执行完毕（2026-10-11）。** 逐项结果、验证证据、与原计划的 4 处偏差见 [`AUDIT_FIX.md`](./AUDIT_FIX.md)。
> 下文保留为**计划原文**，未按执行结果回改。

---

## 修复范围：9 项（6 条严重/高危 + 2 条关联高危 + 1 条文档）

| # | 缺陷 | 严重度 | 负责域 | 冲突风险 |
|---|---|---|---|---|
| F1 | 已结算的 job 被 in-flight item 复活（finish/defer 无 status 守卫） | 严重 | intel-jobs | 中（与 F4/F5 同文件同族） |
| F2 | wireguard `allowedips` 逗号拼接导致导出链接不可解析 | 严重 | export | 低 |
| F3 | 101 升级流包装器吞掉 `CloseWrite` + 计数器 data race | 严重 | proxy | 低 |
| F4 | `CancelJob` 无状态守卫，终态 job 被改写 | 高 | intel-jobs | **高（与 F1/F5 同族）** |
| F5 | `RetryFailedJobItems` 把已取消 job 变成 `succeeded` | 高 | intel-jobs | **高（与 F1/F4 同族）** |
| F6 | provider 空闲轮询每秒烧 1 单位日配额 | 高 | intel-providers | 低 |
| F7 | `RebuildAllPlatforms` 缺 `instrumentQuality`，重启后质量平台视图为空 | 高 | topology | 低 |
| F8 | 主/管理监听无 `http.Server` 超时；`api.Server` 超时是死代码 | 高 | cmd-prism | 低 |
| F9 | `SECURITY.md` §1.5 声称的三个测试不存在 + 编号重复 + 默认值矛盾 | 文档 | docs | 低 |

**刻意不修（本轮范围外）**：`RefreshJob` 非事务读-改-写（实测 0.05% 复现，窗口极窄）、26 条轻微加固项（另立任务）、#10 `egressIndex` bootstrap（需与 F7 一起设计）、#12 CONNECT 零流量熔断（涉及健康度语义决策）、#13 transport 超时（涉及配置项设计）。

---

## 执行拓扑（3 波，避免同文件并发）

### 第 1 波：并行 3 个 fixer（互不重叠）

**Agent A — `intel-jobs`（F1 + F4 + F5）**
独占文件：`internal/intel/jobs/manager.go`、`internal/intel/jobs/audit_probe*_test.go`、`internal/intel/store/repo_jobs.go`、`internal/intel/store/jobs_test.go`

**Agent B — `export`（F2）**
独占文件：`internal/export/uri.go`、`internal/export/audit_probe_test.go`、`internal/export/fixtures_test.go`、`internal/export/uri_test.go`

**Agent C — `proxy`（F3）**
独占文件：`internal/proxy/request_log_capture.go`、`internal/proxy/audit_probe_test.go`

### 第 2 波：并行 3 个 fixer（等第 1 波全部完成后启动）

**Agent D — `intel-providers`（F6）**
独占文件：`internal/intel/jobs/provider.go`、`internal/intel/store/repo_provider.go`、`internal/intel/store/jobs_test.go`（A 已完成，可安全编辑）

**Agent E — `topology`（F7）**
独占文件：`internal/topology/pool.go`

**Agent F — `cmd-prism`（F8）**
独占文件：`cmd/prism/endpoint_runtime.go`、`cmd/prism/admin_runtime.go`、`internal/api/server.go`、`internal/api/middleware_test.go`

### 第 3 波：串行 1 个 fixer

**Agent G — `docs`（F9）**
独占文件：`docs/SECURITY.md`

### 第 4 波：审核（等全部修复完成）

3 个 code-reviewer 并行审核，见「审核方案」。

---

## F1 — 已结算的 job 被 in-flight item 复活

**根因**：`runItem`（`manager.go:605-675`）的 `jobGate` 检查只在循环顶部；循环尾部的 `m.finishItem(ItemDone)`（L674）无守卫。`FinishJobItem`/`DeferJobItem`/`SaveJobItemStep` 的 UPDATE 无 status 前置条件，所以已结算的 item 会被写回 done/queued，`publish` → `RefreshJob` 重算后 job 从 `partial` 变 `succeeded`（或 item stranded）。

**修复（两层，都要做）**：

1. **SQL 层前置条件**（`repo_jobs.go`）——给三个 UPDATE 加 status 守卫，并在 `RowsAffected()==0` 时返回 `ErrJobItemSettled`（新 sentinel）：
   - `FinishJobItem`（L429-433）：`WHERE job_id = ? AND node_hash = ? AND status IN ('queued','running')`
   - `DeferJobItem`（L414-419）：同上
   - `SaveJobItemStep`（L398-403）：同上
2. **调用方处理**（`manager.go`）——`finishItem`/`deferItem` 遇 `ErrJobItemSettled` 时静默返回（只 log debug），且**不调用 `publish`**（避免用陈旧状态重算 job）。

**为什么两层都要**：SQL 守卫是权威边界（防所有调用方）；`manager.go` 的守卫避免无意义的 `publish` 与日志噪音。

**必须先写的失败测试**：`audit_probe_test.go` 现有 3 个测试中前 2 个当前失败，修复后必须**全部通过**（第 3 个已通过，作为不回归锚点）。测试文件顶部注释里的「PRISM-DEVIATION」说明要改成「修复后的回归测试」。

**关联面检查**（防止修复引入新问题）：
- `FinishJobItem` 有 5 处测试调用（`store/jobs_test.go:381,384,410,472,475`），这些测试的 item 都是 `running`/`queued`，守卫不会误伤——**但必须实跑确认**。
- `CancelJob`（F4）依赖 `job_items` 的 status 转换，F1 的守卫**不能**阻止 `CancelJob` 把 queued → canceled（那是 `UPDATE job_items SET status='canceled' WHERE status='queued'`，不是这三个函数）。
- `RetryFailedJobItems`（F5）把 failed → queued，也不走这三个函数。
- `FailPendingJobItems` 已带守卫（`status IN ('queued','running')`），无需改。

**验收**：
```bash
bash scripts/.audit_probe.sh          # 5 个探针全部 PASS
go test -tags "$TAGS" -count=1 ./internal/intel/... ./cmd/prism/...
```

---

## F2 — wireguard `allowedips` 逗号拼接

**根因**：`uri.go:609` 用 `strings.Join(allowedIPs, ",")` 写单值；`node/wireguard.go:454` 的 `stringListOption` 对 string 不切逗号；`normalizeWireGuardPrefixList` 把 `"0.0.0.0/0,::/0"` 交给 `netip.ParsePrefix` 失败。对照 `wireGuardAddressOptions`（`:138`）**显式** `strings.Split(entry, ",")`。

**修复**：`wireGuardSinglePeer`（`wireguard.go:196-204`）里对每个条目先按逗号切分再逐个 `NormalizeWireGuardPrefix`，与 `wireGuardAddressOptions` 对齐。**不改导出端**（导出端用逗号是既定格式，改解析端更安全、兼容面更广）。

**必须先写的失败测试**：`internal/export/audit_probe_test.go` 现有 1 个测试当前失败，修复后必须通过。

**补回归覆盖**：`fixtures_test.go:76` 的 fixture 只有单值 `allowed_ips:["0.0.0.0/0"]`，要改成两条（`["0.0.0.0/0","::/0"]`）——这样 `TestExportURIRoundTripsThroughTheParser` 才会覆盖多值路径。

**关联面检查**：
- `wireGuardAddressOptions` 已切分，不要重复改。
- 检查 `stringListOption` 的**其他调用点**是否依赖「不切分」语义（`grep -rn 'stringListOption' internal/node/`）——若有，改为在 `wireGuardSinglePeer` 内部切分而非改 `stringListOption` 本身。
- `parseWireGuardReserved` 已有逗号切分（`:293`），说明该文件其他字段的逗号处理是对称的——确认 allowed_ips 是唯一漏的。

**验收**：
```bash
bash scripts/.audit_probe_export.sh
go test -tags "$TAGS" -count=1 ./internal/export/... ./internal/node/... ./internal/subscription/...
```

---

## F3 — 101 升级流包装器吞掉 `CloseWrite`

**根因**：`countingReadWriteCloser`（`request_log_capture.go:121-155`）只实现 `Read/Write/Close`；httputil 的 `switchProtocolCopier` 断言 `CloseWrite` 失败 → 发 `errCopyDone` → 两端同时关闭。同根因的 `totalRead/totalWrite` 是普通 int64，被 copy goroutine 写、被主 goroutine 读（`reverse.go:475-478`），`-race` 确认为 DATA RACE。

**修复**：
1. 补 `CloseWrite()`/`CloseRead()`：委派给底层；底层不支持时返回 `nil`（**关键**：不能返回 `errHalfCloseUnsupported`，否则 httputil 又走 `errCopyDone` 路径）。参照 `counting_conn.go:110` 的 `closeWriteErr`/`closeReadErr` 辅助函数。
2. `totalRead`/`totalWrite` 改 `atomic.Int64`，`Read`/`Write`/`TotalRead`/`TotalWrite` 用原子操作。

**必须先写的失败测试**：`internal/proxy/audit_probe_test.go` 现有 2 个测试——第 1 个（`CloseWrite` 断言）当前失败，第 2 个（`-race`）当前报 DATA RACE。修复后**两个都必须通过**，包括 `-race`。

**关联面检查**：
- 同包内其他包装器（`counting_conn.go`、`connCloseNotifier`、`tlsLatencyConn`、`prebufferedConn`）已实现 `CloseWrite`——确认修复后**行为一致**，特别是不支持时返回什么。
- `reverse.go:475-478` 读 `TotalRead()/TotalWrite()` 的时机在 `ServeHTTP` 返回后，改原子后仍要确认读数语义（读到的值可能仍不完整，但那只是统计精度问题，不是 race）。
- **顺带检查**：`socks5AuthObserver`（`cmd/prism/proxy_auth_guard.go:101`）是同族缺陷（报告 #27）。**本轮一并修**（同一模式，风险极低）：加 `CloseWrite`/`CloseRead` 委派。→ 该文件归 Agent C。

**验收**：
```bash
bash scripts/.audit_probe_proxy.sh    # 含 -race 段
go test -tags "$TAGS" -race -count=1 ./internal/proxy/... ./cmd/prism/...
```

---

## F4 — `CancelJob` 无状态守卫

**根因**：`repo_jobs.go:179` 的 `UPDATE jobs SET status='canceled' WHERE id=?` 无 status 前置条件。

**修复**：加 `AND status IN ('queued','running')`；`RowsAffected()==0` 时先 `GetJob` 区分：
- job 不存在 → `ErrJobNotFound`
- job 已终态 → 返回新的 `ErrJobNotActive`（`internal/service` 层映射为 `CONFLICT`/`409`，附当前状态）

**必须先写的失败测试**：`audit_probe2_test.go` 的 `TestAudit_CanceledJobIsNotOverwrittenByALaterCancel` 当前失败，修复后通过。

**关联面检查**：
- `CancelJob` 内还有第二条语句 `UPDATE job_items SET status='canceled' WHERE job_id=? AND status='queued'`（L187，**已有守卫**）——保持。
- 检查 `CancelIntelJob`（`service/control_plane_intel.go:179`）是否需要把 `ErrJobNotActive` 映射成新的 API 错误码；前端 `JobsPage.tsx:626` 已有 `disabled={!running}`，所以正常路径不受影响。
- **与 F1 的交互**：F1 给 `FinishJobItem` 加守卫后，被取消的 item 不会再被 in-flight worker 写回——这正是想要的。
- 检查 `control_plane_intel_test.go:236,263` 的现有断言是否需要更新（取消 running job 仍应成功；取消未知 job 仍应 NOT_FOUND）。

**验收**：
```bash
go test -tags "$TAGS" -count=1 ./internal/intel/... ./internal/service/... ./internal/api/...
```

---

## F5 — `RetryFailedJobItems` 把已取消 job 变成 `succeeded`

**根因**：`repo_jobs.go:206-219`——第一条 UPDATE 只处理 failed item，第二条 UPDATE **无条件**重开 job（`status IN ('partial','failed','canceled')`）。已取消 job 没有 failed item，但第二条仍执行 → `publish` → `RefreshJob` 走 default 分支 → `succeeded`。

**修复**：第二条 UPDATE 加「存在 failed item」条件，或仅在 `retried > 0` 时执行。推荐后者（更直观）：
```go
if retried > 0 {
    // UPDATE jobs SET status='queued', finished_at_ns=0 WHERE id=? AND status IN ('partial','failed')
}
```
注意 `canceled` 应从该列表**移除**——一个被取消的 job 即使有 failed item，重试也不该把它复活（用户明确取消了）。这是**语义决策**，需要在代码注释里写明。

**必须先写的失败测试**：`audit_probe3_test.go` 的 `TestAudit_RetryFailedTurnsACanceledJobIntoSuccess` 当前失败，修复后通过。

**关联面检查**：
- `RetryFailed`（`manager.go:305-312`）调用后 `publish`——`publish` 会重算 job；若 job 仍是 canceled，`RefreshJob` 的 `case status == JobCanceled` 分支保持 canceled（`repo_jobs.go:548`），正确。
- `control_plane_intel.go:279,390` 引用了 `JobFailed`——确认状态列表语义未被破坏。
- **与 F4 的交互**：F4 让 cancel 只作用于 active job；F5 让 retry 不复活 canceled job。两者一起保证 canceled 是终态。
- 现有测试 `control_plane_intel_test.go:236` 附近的 retry 用例需要实跑确认。

**验收**：同 F4。

---

## F6 — provider 空闲轮询每秒烧 1 单位日配额

**根因**：`provider.go:165-185`——`ConsumeProviderBudget` 在 `ClaimProviderItems` **之前**。队列为空时 claim 返回空，但 `st.Used++` 与 `NextRequestAtNs` 已写入。空闲周期 `providerIdleSleep = 1s` + 默认 `QPS: 1` → 每秒烧 1 单位。

**修复（保持预算是「预留」语义）**：把预算消费移到 claim **之后**，并处理「claim 到但预算关」的回滚：
1. `ClaimProviderItems` 先执行；
2. 若 `len(items) == 0` → 直接返回 `providerIdleSleep`（**不消费预算**）；
3. 若 claim 到 items → 调 `ConsumeProviderBudget`：
   - 成功 → 继续 lookup；
   - 失败（gate 关闭）→ **把刚 claim 的 items 放回 queued**（新增 `ReleaseProviderItems` 或复用现有 reset 路径），返回 `budgetWait(state, now)`。

**必须先写的失败测试**：`audit_probe2_test.go` 的 `TestAudit_ProviderBudgetIsNotConsumedWithoutWork` 当前失败，修复后通过。

**关联面检查**：
- **新增的回滚路径必须测试**：claim 到但预算关时，item 必须回到 queued 且 `attempts` 不增（否则会虚假消耗重试次数）。
- `ClaimProviderItems` 会把 item 置 running 并设 lease（`repo_provider.go:587-597`）——回滚要清 lease。
- `ResolveProviderItem`/`FailProviderItem` 语义不能受影响。
- `jobs_test.go` 有 7 处 `ConsumeProviderBudget` 直接调用（`:17,30,40,45,52,67,73,86,92`）——那是 store 层测试，**不应改**（store 的原子预留语义本身是对的，问题在调用顺序）。
- 检查 `budgetWait` 的返回值处理是否仍正确（`provider.go:254-271`）。

**验收**：
```bash
bash scripts/.audit_probe.sh
go test -tags "$TAGS" -count=1 ./internal/intel/... 
```

---

## F7 — `RebuildAllPlatforms` 缺 `instrumentQuality`

**根因**：`pool.go:513-543` 的 `RebuildAllPlatforms` 不调 `instrumentQuality`，而 `RebuildPlatform`（`:553-554`）调。启动顺序 `app_runtime.go:221`（bootstrap → `RebuildAllPlatforms`）早于 `:231`（`initIntelJobs` → `SetQualitySnapshot`），所以带质量条件的平台重启后视图为空且无自愈。

**修复**：在 `RebuildAllPlatforms` 的循环里，对每个平台先 `p.instrumentQuality(plat)` 再 `FullRebuild`（与 `RebuildPlatform` 一致）。

**必须先写的失败测试**：新增 `internal/topology/pool_rebuild_snapshot_test.go` 用例（该文件已有 `RebuildPlatform` 的快照测试，`:29-33`）——断言 `RebuildAllPlatforms` 后带 `quality_policy` 的平台视图**非空**。当前应失败。

**关联面检查**：
- `instrumentQuality` 读 `p.qualitySnapshot`（`pool.go:304-311`）——确认并发调用安全（`RebuildAllPlatforms` 用 worker 池并发跑各平台，`instrumentQuality` 只读 `p.qualitySnapshot`，但 F7 的 `SetQualitySnapshot` 是普通字段写——见报告 #25。**本轮不动 #25**，但要在测试里确认无 race）。
- `SetQualitySnapshot`（`:290-299`）会遍历所有平台设置快照——确认与 `RebuildAllPlatforms` 的调用顺序在 bootstrap 后仍正确。
- `subscription_scheduler.go:464` 也调 `RebuildAllPlatforms`——修复后该路径也会注入快照，这是**改善**（原先那里同样缺）。

**验收**：
```bash
go test -tags "$TAGS" -race -count=1 ./internal/topology/... ./internal/platform/...
```

---

## F8 — 主/管理监听无 `http.Server` 超时

**根因**：`api.Server` 里配好的超时（`server.go:259-261`）挂在**从未 Serve** 的 `httpServer` 上；真正 Serve 的是 `endpoint_runtime.go:123`（`&http.Server{Handler: httpHandler}`）与 `admin_runtime.go:41`（同形）。`inbound_demux.go:206` 还清空了嗅探 deadline。

**修复**：
1. 把 `apiReadHeaderTimeout`/`apiIdleTimeout`/`apiMaxHeaderBytes` 应用到 `endpoint_runtime.go:123` 与 `admin_runtime.go:41` 的 `http.Server`。
2. 导出这三个常量（或提供 `api.ConnectionBounds()` 访问器），供 `cmd/prism` 使用——`cmd/prism` 已 import `internal/api`。
3. `WriteTimeout` **保持不设**（SSE 长连接会被切断，`server.go:247-255` 的注释说明了原因）。
4. **保留** `api.Server` 的超时字段（它现在仍是 `Handler()` 的构造点，且 `TestServerConnectionBounds` 断言它们）——在 `ListenAndServe`/`Shutdown` 上补注释说明它们不被 `cmd/prism` 使用，或把死代码删掉并同步删测试。

**推荐做法**：删掉 `Server.ListenAndServe`/`Shutdown`（无调用者）与 `httpServer` 的 `Addr` 字段，改成导出的 `api.ReadHeaderTimeout` 等常量；`TestServerConnectionBounds` 改为断言这些常量被 `cmd/prism` 使用（或改测真实 server）。

**必须先写的失败测试**：`cmd/prism` 侧新增测试，断言 `endpoint_runtime` 与 `admin_runtime` 构造的 `http.Server` 有非零 `ReadHeaderTimeout`/`IdleTimeout`/`MaxHeaderBytes`。当前应失败。

**关联面检查**：
- `inbound_demux.go:199-206` 清空 deadline 的行为：给 `http.Server` 设 `ReadHeaderTimeout` 后，net/http 会自己设 header 阶段 deadline，所以嗅探后的清空**不再构成漏洞**——但要在测试里验证（构造一个只发 1 字节的连接，断言在 `ReadHeaderTimeout` 后被关闭）。
- `TestServerConnectionBounds`（`middleware_test.go:140-156`）当前断言的是从不 Serve 的 `httpSrv`——**修复后必须更新**，否则是虚假保证。
- `adminListener.server.Serve(listener)`（`admin_runtime.go:44`）与 `demux.httpServer.Serve(httpListener)`（`inbound_demux.go:68`）都是真实 Serve 点。
- smoke 测试 `scripts/smoke.sh` 的 `admin listener refuses CONNECT` 需要实跑。

**验收**：
```bash
go test -tags "$TAGS" -count=1 ./internal/api/... ./cmd/prism/...
bash scripts/smoke.sh
```

---

## F9 — `SECURITY.md` 修正

**修改项**（逐条，都有已核实的代码事实）：

1. **§1.5（`:161-164`）删除不存在的三个测试**：`TestInboundMuxRoutesSubscriptionPathToManagementHandler`、`TestInboundMuxSubscriptionPathHonoursAllowManagement`、`TestAdminListenerServesSubscriptionPath`（文件 `cmd/prism/subscription_routing_test.go` 不存在）。保留同段真实存在的两个（`subcommands_test.go:447,486`）。
2. **§1.2（`:101`）默认值矛盾**：改为与代码一致——`PRISM_PROXY_AUTH_FAIL_LIMIT` 默认 **30（启用）**，与 `:541` 统一。`.env.example:39` 的 `# PRISM_PROXY_AUTH_FAIL_LIMIT=0` 也要改成 `# PRISM_PROXY_AUTH_FAIL_LIMIT=30`。
3. **§3.2 标题（`:539`）**：「off by default」→「on by default」，并保留 `:541` 的正确说明。
4. **§1.9 内矛盾（`:333`）**：「The switch is off by default」→ 与 `:297` 一致（`PRISM_DENY_PRIVATE_NODES` 默认 `true`）。
5. **§3.3（`:565,567`）**：`PRISM_DIRECT_DENY_PRIVATE` 默认 `true`（`env.go:191`），标题与「旧默认」表述改为准确描述。
6. **§1.9 编号重复**：`:289`（Node target policy）与 `:372`（Configuration reporting）都是 1.9；`:357` 是 1.8 但排在第一个 1.9 之后。重排为 1.8/1.9/1.10/...，并**同步更新 §2 里的所有「§1.x」跨引用**。
7. **§1.1（`:32-36`）补 SSE 例外**：`GET /api/v1/intel/jobs/{id}/events` 在 `AuthMiddleware` 之外，接受 `?access_token=` 与裸 `Authorization`（`handler_intel.go:268-276`）。文档需明说这是**唯一**例外。
8. **§2（`:494-497`）**：`ValidateProxyTokenForV1` 不做 16 字符/保留字检查（那在 `LoadEnvConfig`，`env.go:257,271,277`）——改为准确描述。
9. **§1.3（`:122-123`）**：补一句「mux 层也做一次比较（`inbound_mux.go:145`，非常量时间）」——避免读者以为命名空间鉴权全程常量时间。

**约束**：只改 `docs/SECURITY.md` 与 `.env.example`。**不要**改任何代码行为。每条修改都要能指向具体代码行作为依据。

---

## 审核方案（第 4 波）

修复完成后，3 个 code-reviewer 并行审核，每个都要**审修复项 + 审关联功能**（防止修复引入新问题）：

**Reviewer 1 — intel 域（F1/F4/F5/F6）**
- 逐条验证修复是否真的解决了根因（不是掩盖症状）
- 检查 `repo_jobs.go` 的所有 UPDATE 语句现在是否都有一致的 status 守卫策略
- 检查 job 状态机的完整性：queued → running → {succeeded|partial|failed|canceled}，是否存在其他能改写终态的路径
- 检查 `FailPendingJobItems` / `ResetRunningJobItems` / `MarkJobRunning` / `RefreshJob` 与新守卫的交互
- 检查 provider 队列的回滚路径（F6）是否引入新的泄漏或重试计数问题
- 检查 `cmd/prism/intel_runtime.go`、`internal/service/control_plane_intel.go` 的调用面

**Reviewer 2 — proxy + export（F2/F3）**
- 验证 `CloseWrite` 委派的 nil 返回语义正确（不触发 `errCopyDone`）
- 检查同包所有 `CloseWrite` 实现的一致性
- 检查 `atomic.Int64` 改造后所有读写点（含 `reverse.go` 的消费点）
- 验证 wireguard 解析改动不破坏单值路径、`address` 路径、`reserved` 路径
- 检查 `stringListOption` 的其他调用点未被影响
- 端到端跑 `TestExportURIRoundTripsThroughTheParser`

**Reviewer 3 — topology + cmd-prism + docs（F7/F8/F9）**
- 验证 `RebuildAllPlatforms` 修复后启动路径与 `subscription_scheduler.go:464` 路径都正确
- 检查 `instrumentQuality` 并发安全（worker 池并发）
- 验证超时注入后 SSE 不被切断、demux 嗅探路径正确
- 检查 `TestServerConnectionBounds` 是否仍能提供真实保证
- 逐条核对 SECURITY.md 修改与代码一致

**审核输出要求**：每条修复给「通过 / 不通过 + 具体理由 + 行号」；不通过的必须给出可复现的失败测试或明确的代码路径。同时列出「修复引入的新问题」（如有）。

---

## 最终验收（我执行）

```bash
make build                 # 完整构建（npm ci → vite → go build 8 标签）
make verify                # lint + test + test-race + protocol-matrix + test-web
bash scripts/smoke.sh      # 23 项
make lint-go               # golangci-lint
go test -tags "$TAGS" -count=1 ./...        # 无缓存全树
go test -tags "$TAGS" -race -count=1 ./...  # 无缓存全树 + race
```

全部探针测试（`scripts/.audit_probe*.sh`）必须 PASS。

---

## 交付物

1. 9 项修复 + 转正的回归测试
2. 审核报告（3 个 reviewer 的结论）
3. `docs/AUDIT_FIX.md`（本计划的执行记录，含偏差说明）
4. 更新 `AUDIT_REPORT.md` 的状态（哪些已修、哪些留待后续）
