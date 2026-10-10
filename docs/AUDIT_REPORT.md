# Prism 深度审计报告

审计范围：`internal/proxy`、`internal/api` + `internal/config` + `cmd/prism` 安全边界、`internal/routing` + `internal/topology` + `internal/platform` + `internal/node`、`internal/state` + `internal/intel/store`、`internal/subscription` + `internal/export` + `internal/intel`。
方法：静态阅读 + 可运行探针（`go test -tags "<TAGS>" -run TestAudit_`）。每条结论标注证据强度。

`TAGS = with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy`


> **状态：9 项修复已全部完成并验证（2026-10-11）。** 执行记录、验证证据与偏差说明见 [`AUDIT_FIX.md`](./AUDIT_FIX.md)。
> 本文保留为**审计发现**的原始记录：正文中的「修复前」代码行号与机制描述未改动，便于追溯。
---

## 一、已用可运行测试确认的缺陷

### 1. [严重] 已结算的 job 被 in-flight item 复活（intel 作业调度）

**位置**：`internal/intel/jobs/manager.go:605-675`（`runItem`）、`internal/intel/store/repo_jobs.go:424`（`FinishJobItem`）、`:409`（`DeferJobItem`）

**机制**：`jobGate` 三态检查只在每个 step 循环**顶部**执行（L632）。当 timeout sweep（`FailPendingJobItems`）在最后一个 step 执行期间结算 job 后：
- 循环结束 → `m.finishItem(parent, item, store.ItemDone, ...)`（L674）**无 gate 守卫**；
- `FinishJobItem` 的 SQL 无 status 前置条件 → item 从 failed 变 done、`error_code` 清空；
- `publish` → `RefreshJob` 重算（pending=0, failed=0）→ job 从 `partial` 变 **`succeeded`**。

**测试证据**（`internal/intel/jobs/audit_probe_test.go`）：
```
job status after the settled item finished: succeeded (finished_at=...)
item status after the settled item finished: done (error=)
job = succeeded, want partial: a settled job was moved out of its terminal state
item = done, want failed: the settled item was rewritten by the trailing finishItem
item error = "", want "JOB_TIMEOUT"
```
这**正是修复 4 想防的问题**：超时结算的 job 报告为成功，failed 计数被抹掉。

**defer 变体**（同文件第二个测试）：step 返回 `DeferUntilNs` 时 `deferItem` 把已结算 item 从 failed 拉回 `queued`：
```
item status after the late defer: queued (error=JOB_TIMEOUT)
item = queued, want failed: a late defer reopened a settled item
```
job 保持 `partial` 但 item **stranded**——`ListActiveJobs` 只查 `queued`/`running` 的 **job**，终态 job 里的 queued item 永远不会被 claim，`RetryFailedJobItems` 也只处理 failed item，救不回来。

**修法**：`runItem` 循环尾部改为先查 gate 再 finish；或给 `FinishJobItem`/`DeferJobItem`/`SaveJobItemStep` 的 UPDATE 加 `AND status IN ('queued','running')` 前置条件（与 `FailPendingJobItems` 一致），并检查 `RowsAffected()==0` 时静默退出。

---

### 2. [严重] 导出的 wireguard 链接自己解析不回来（导出/订阅）

**位置**：`internal/export/uri.go:608-610`（`strings.Join(allowedIPs, ",")`）与 `internal/node/wireguard.go:444-454`（`stringListOption` 的 string 分支不切逗号）、`:196-204`（`wireGuardSinglePeer`）

**机制**：导出端把多条 allowed-ips 拼成一个逗号串写进 `allowedips=`；解析端 `stringListOption` 对 string 直接返回 `[]string{typed}`，随后 `normalizeWireGuardPrefixList` 把 `"0.0.0.0/0,::/0"` 交给 `NormalizeWireGuardPrefix`，含 `/` 走 `netip.ParsePrefix` 失败 → `INVALID:wireguard prefix` → 整行丢弃。默认 `allowed_ips` 就是两条（`DefaultWireGuardAllowedIPs()`），所以**必然触发**。

**不对称证据**：`address` 走 `wireGuardAddressOptions`（`:138`）**显式** `strings.Split(entry, ",")`——同一份文档里 address 正常、allowedips 全丢。

**测试证据**（`internal/export/audit_probe_test.go`）：
```
exported link: wireguard://WG-PRIVATE-KEY-FIXTURE@203.0.113.9:51820/?address=10.7.0.2%2F32&allowedips=0.0.0.0%2F0%2C%3A%3A%2F0&publickey=WG-PUBLIC-KEY-FIXTURE#wg-default
parsed 0 nodes, want 1 (skipped: [{Name:wg-default Type:wireguard Source:uri Reason:INVALID Detail:unparsable share link}])
```
现有测试没覆盖，因为 fixture 只有单值 `allowed_ips:["0.0.0.0/0"]`（`fixtures_test.go:76`）。

**修法**：`wireGuardSinglePeer` 对每个条目先 `strings.Split(entry, ",")` 再逐个 normalize，与 `wireGuardAddressOptions` 对齐。

---

### 3. [严重] 101 升级流包装器吞掉 `CloseWrite`，提前拆双向连接并竞争计数器（proxy 转发核心）

**位置**：`internal/proxy/reverse.go:423`（`newCountingReadWriteCloser`）、`internal/proxy/request_log_capture.go:121-155`

**机制**：101 分支把 `resp.Body` 包进 `countingReadWriteCloser`，该类只实现 `Read/Write/Close`。`net/http/httputil` 的 `switchProtocolCopier.copyToBackend` 做 `c.backend.(interface{ CloseWrite() error })` 断言，失败则发 `errCopyDone`，`handleUpgradeResponse` 的 `err := <-errc; if err == nil { err = <-errc }` 立即返回 → deferred `conn.Close()`/`backConnCloseCh` 把两端**同时**关闭，另一方向的 `io.Copy` 被打断。

**测试证据**（`internal/proxy/audit_probe_test.go`）：
```
the 101 upgrade body wrapper lost CloseWrite: httputil's switchProtocolCopier falls back to
errCopyDone and closes both connections while the upstream stream is still open (wrapped type
*proxy.countingReadWriteCloser)
```

**同根因的真实数据竞争**（`-race` 确认）：
```
WARNING: DATA RACE
Write at ... by goroutine 10:
  prism/internal/proxy.(*countingReadWriteCloser).Write()
      internal/proxy/request_log_capture.go:139
Previous read at ... by goroutine 9:
  prism/internal/proxy.(*countingReadWriteCloser).TotalWrite()
      internal/proxy/request_log_capture.go:155
```
`totalRead/totalWrite` 是普通 `int64`，被 httputil 的 copy goroutine 写、被 `ServeHTTP` 主 goroutine 读（`reverse.go:475-478`），无同步。

**包内不一致**：`counting_conn.go:110`、`connCloseNotifier`（`:156`）、`tlsLatencyConn`（`:71`）、`prebufferedConn`（`inbound_demux.go:440`）**都**实现了 `CloseWrite`——此处是漏做。

**修法**：补 `CloseWrite()`/`CloseRead()` 委派（底层不支持时返回 nil 以免再次触发 `errCopyDone`）；`totalRead/totalWrite` 改 `atomic.Int64`。

---

### 4. [高] `CancelJob` 无状态守卫：终态 job 可被改写（intel 作业状态机）

**位置**：`internal/intel/store/repo_jobs.go:179`（`UPDATE jobs SET status = 'canceled' WHERE id = ?`）

**机制**：无 `AND status IN (...)` 前置条件。对已 `succeeded` 的 job 调 cancel → 状态被改写为 `canceled`，审计记录与实际结果矛盾。前端有 `disabled={!running}` 守卫（`JobsPage.tsx:626`），所以这是 API 层 TOCTOU/直接调 API 的问题。

**测试证据**（`internal/intel/jobs/audit_probe2_test.go`）：
```
job after cancelling a succeeded job: canceled (finished_at ... -> ...)
job = canceled, want succeeded: a terminal job was rewritten by a later cancel
```

**修法**：`WHERE id = ? AND status IN ('queued','running')`；`RowsAffected()==0` 时区分「不存在」（`ErrJobNotFound`）与「已终态」（返回冲突错误）。

---

### 5. [高] 「重试失败项」把已取消的 job 变成 `succeeded`（intel 作业状态机）

**位置**：`internal/intel/store/repo_jobs.go:206-219`（`RetryFailedJobItems`）与 `:530-571`（`RefreshJob`）

**机制**：`RetryFailedJobItems` 先 requeue failed item，再**无条件** `UPDATE jobs SET status='queued', finished_at_ns=0 WHERE id=? AND status IN ('partial','failed','canceled')`。已取消的 job 没有 failed item（`CancelJob` 把 queued item 变成 canceled），第一个 UPDATE 无效果，第二个仍把 job 重开。随后 `publish` → `RefreshJob` 重算：无 pending、无 failed → 走 default 分支 → **`succeeded`**。

**测试证据**（`internal/intel/jobs/audit_probe3_test.go`）：
```
retried=0 job=succeeded items: total=2 done=0 failed=0 canceled=2 queued=0 running=0
job = succeeded, want canceled: retry-failed reopened a job that had no failed item
```
用户点「重试失败项」（返回 `retried=0`），却把一个取消的 job 报成成功。

**修法**：第二个 UPDATE 加 `AND EXISTS (SELECT 1 FROM job_items WHERE job_id=? AND status='failed')`，或仅在 `retried > 0` 时执行。

---

### 6. [高] provider 空闲轮询每秒烧掉 1 单位日配额（intel provider 队列）

**位置**：`internal/intel/jobs/provider.go:165-185`（`runOnce`）

**机制**：`ConsumeProviderBudget` 在 `ClaimProviderItems` **之前**调用。队列为空时 claim 返回空，但配额已经扣掉：`st.Used++` 与 `NextRequestAtNs` 已写入（`repo_provider.go:180-183`）。空闲轮询周期 `providerIdleSleep = 1s`（`provider.go:15`）+ 默认 `QPS: 1`（`online.go:96/357/513/721`）→ **每秒烧 1 单位**。`ipapi_is` 默认 500/day 约 **8 分钟**耗尽，`proxycheck` 默认 80/day 约 **80 秒**耗尽。

**测试证据**（`internal/intel/jobs/audit_probe2_test.go`）：
```
provider state after 3 empty rounds: found=true used=3 next_request_at=... lookups=0
used = 3, want 0: three empty rounds consumed daily budget without resolving any IP
```

**修法**：先 `ClaimProviderItems`，只在 claim 到非空时 `ConsumeProviderBudget`；或在 `ConsumeProviderBudget` 里把「无工作」作为不扣减的路径。注意 claim 已把 item 置为 running（带 lease），所以顺序调换要同时处理「claim 到但预算关」的回滚（当前 `budgetWait` 逻辑可复用）。

---

## 二、静态确证（代码事实明确，未构造运行时复现）

### 7. [高] 无出口观测的节点被标记为「已完成」— 已修复，此处为回归确认
修复 3 已落地（`steps_no_egress_test.go`）。

### 8. [高] 启动顺序：`RebuildAllPlatforms` 早于质量投影注入，带质量条件的平台视图为空（topology）

**位置**：`cmd/prism/app_runtime.go:221`（`bootstrapFromPersistence`，内部 `:404` 调 `RebuildAllPlatforms`）→ `:231`（`initIntelJobs` → `intel_runtime.go:179` `pool.SetQualitySnapshot`）

**机制**：`RebuildAllPlatforms`（`pool.go:513`）与 `RebuildPlatform`（`:553`）不同，**不调用** `instrumentQuality`。作者自己在 `RebuildPlatform` 的注释里写明了这个不变量（`pool.go:546-552`），但只对 `RebuildPlatform` 生效。`quality_policy`/`ip_types`/`purity_bands` 判据 fail-closed（`node_criteria.go:229-232`），所以这些平台重建后视图为空。

**自愈路径缺失**：已恢复且熔断已闭合的健康节点在探测成功时不产生任何通知；禁用订阅的节点也不被 `ForceRefreshAllAsync` 触达。

**修法**：`RebuildAllPlatforms` 里对每个平台先 `instrumentQuality(plat)` 再 `FullRebuild`（2 行改动）。

---

### 9. [中] `RoutableView.Clear` 的 size 与分片内容不同步 → 误报 503（platform）

**位置**：`internal/platform/routableview.go:100-108`（`Clear` 先清 64 个分片、**最后**才 `size.Store(0)`）、`:113-133`（`RandomPick` 用 `Size()` 做随机落点遍历）

**机制**：读者（`RandomPick`/`Range`/`Contains`）不持 `viewMu`（只有 `FullRebuild`/`NotifyDirty` 持）。每次 `FullRebuild`（启动重建、`RebuildPlatformView` API、`ReplacePlatform`、`SetSubscriptionEnabled`）都有窗口：size 仍为 N、分片已空 → 遍历到底 `target >= 0` → 返回 `(node.Zero, false)`。上层不重试（`router.go:434-438` 直接返回错误）。

**修法**：`Clear()` 先 `size.Store(0)` 再清分片；或 `RandomPick` 落空时重试一次。

---

### 10. [中] `egressIndex` 在 bootstrap 恢复路径不建立，`NotifyEgressIPDirty` 永久漏掉这些节点（topology）

**位置**：`cmd/prism/main.go:907`（`entry.SetEgressIP(ip)` 直写，绕过 `pool.UpdateNodeEgressIP`）、`internal/topology/pool.go:779-783`（只在 IP 变化时 `indexEgressIP`）

**机制**：bootstrap 恢复的 egress IP 不进 `egressIndex`。之后探测观测到**同一个 IP** 时 `oldIP != *ip` 为假，不会补建索引 → `NotifyEgressIPDirty(ip)` 返回 0 → 新的 intel 评估不触发平台重新评估。被新评估判为不合格的节点在这段窗口内继续接流量。

**修法**：`restoreBootstrapNodeDynamics` 改调 `pool.UpdateNodeEgressIP(hash, &ip, &region)`。

---

### 11. [中] 删除平台后 `Router.states` 与其租约/IP 统计永不回收（routing）

**位置**：`internal/service/control_plane_platform.go:798`（`DeletePlatform`）、`internal/routing/router.go:35`（`states *xsync.Map[...]`，全仓库无 `Delete` 调用）

**机制**：`DeletePlatform` 只做 `Engine.DeletePlatform` + `Pool.UnregisterPlatform`。平台的全部租约、`IPLoadStats` 留在内存，`LeaseCleaner` 每 13s 仍遍历；`cache.db` 的 leases 行只在下次重启由 `RepairConsistency` 清掉。

**修法**：加 `Router.PurgePlatform(id)`。

---

### 12. [中] CONNECT 零/单向流量被判为节点失败，可借「空连接」熔断共享节点（proxy）

**位置**：`internal/proxy/tunnel.go:268-278`（`requireBidirectionalTraffic && (ingressResult.n == 0 || egressResult.n == 0)` → `netOK=false`）、`forward.go:426`（开启该开关）

**机制**：TCP 建连完全成功但零流量也计节点失败。持代理凭据的客户端连续 3 次 CONNECT 后立刻关闭即可把 `FailureCount` 顶到阈值（默认 3）并打开熔断，影响同节点其他租户。**不对称**：SOCKS5 走同一条 pump 但没开该开关（`socks5.go:158`），同样的空连接在 SOCKS5 上不计失败。

**修法**：零/单向流量只写请求日志、不触发 `recordPassiveResultAsync`；或给 SOCKS5 打开同一开关消除不对称。

---

### 13. [中] 出站 transport 与直连 dialer 无任何超时（proxy）

**位置**：`internal/proxy/transport.go:15-19`（`OutboundTransportConfig` 只有 `MaxIdleConns`/`MaxIdleConnsPerHost`/`IdleConnTimeout`）、`:97`、`:124`、`direct_dial_guard.go:119`（`&net.Dialer{}` 无 `Timeout`/`KeepAlive`）

**机制**：构造出的 `http.Transport` 无 `ResponseHeaderTimeout`、`TLSHandshakeTimeout`（零值 = 无超时）。`RoundTrip` 遇「TCP 建连成功但永不回响应头」的上游时无限期阻塞。**对照**：项目内其他出站 HTTP 客户端都显式设了超时（`intel/providers/http.go:30-31`、`vianode.go:44`）——本处遗漏。

**修法**：给配置加 `ResponseHeaderTimeout`/`TLSHandshakeTimeout`，`dialer()` 加 `Timeout`/`KeepAlive`。

---

### 14. [中] 主监听与管理监听无 `http.Server` 超时；`internal/api` 里配好的超时是死代码（安全边界）

**位置**：`cmd/prism/endpoint_runtime.go:123`（`&http.Server{Handler: httpHandler}`）、`cmd/prism/admin_runtime.go:41`（同形）、`internal/api/server.go:256-262`（配了超时的 `srv`）

**机制**：真正 `Serve` 的两个 server 都是裸 `&http.Server{Handler: ...}`。`api.Server.ListenAndServe`/`Shutdown`（`server.go:271-277`）**全仓库无调用者**——调用方只用 `apiSrv.Handler()`（`app_runtime.go:580,663`）。`inbound_demux.go:206` 在嗅探首字节后显式 `SetReadDeadline(time.Time{})` 清掉 15s 嗅探超时。客户端发 1 字节后挂住即可无限占用连接（slowloris），主端口与管理端口都受影响。

**虚假保证**：`internal/api/middleware_test.go:140-156` 断言的是那个从不 Serve 的 `httpSrv`。

**修法**：把 `apiReadHeaderTimeout`/`apiIdleTimeout`/`apiMaxHeaderBytes` 应用到两个真实 server（含 demux 那个）；或删掉死超时字段。

---

### 15. [中] SSE 端点接受 `?access_token=` 与无 `Bearer` 前缀的裸 `Authorization`（安全边界）

**位置**：`internal/api/server.go:244`（SSE 注册在 `AuthMiddleware` 之外）、`internal/api/handler_intel.go:268-276`

**机制**：`accessTokenFromRequest` 在 `Authorization` 不是 `Bearer ` 前缀时**直接返回整段头值**（`:273`），无头时回落到 `?access_token=`（`:275`）。WebUI 当前实现就把 token 放进 URL（`features/jobs/api.ts:84`）→ 进入前置反代 access log、浏览器历史、Referer 链。

**文档不符**：`docs/SECURITY.md:32-36`（§1.1）声明 `/api/v1/*` 一律要求 `Authorization: Bearer`；全文 0 处提到 `access_token`。

**测试固化**：`handler_intel_events_test.go:77-79` 三个用例分别是 query token、`Bearer`、裸值。

**修法**：删掉裸 `Authorization` 分支；文档补上该例外；长期方案改成一次性短期 ticket。

---

### 16. [中] `PRISM_PROXY_AUTH_FAIL_LIMIT` 默认值与文档矛盾（安全边界 / 文档）

**位置**：`internal/config/env.go:193`（`envInt("PRISM_PROXY_AUTH_FAIL_LIMIT", 30, &errs)`）

**文档冲突**：`docs/SECURITY.md:101` 写「The default `0` disables all of it」，但同一文件 `:541` 又写「defaults to `30`」；§3.2 标题（`:539`）写「off by default」；`.env.example:39` 写 `# PRISM_PROXY_AUTH_FAIL_LIMIT=0`。代码是 **30（默认开启）**。

**附带**：`ProxyClientIP`（`auth_guard.go:30-34`）只取 `RemoteAddr` 主机部分，忽略 `X-Forwarded-For`/`PRISM_TRUSTED_PROXIES`——NAT 或前置反代下，一个攻击者可让全部用户掉线。

**修法**：确定真值后统一四处（默认值、注释、SECURITY.md、`.env.example`）；让代理路径也用 `limiter.ClientIP(r)`。

---

### 17. [中] `docs/SECURITY.md` §1.5 声称的三个测试不存在（文档）

**位置**：`docs/SECURITY.md:161-164`

**声明**：`TestInboundMuxRoutesSubscriptionPathToManagementHandler`、`TestInboundMuxSubscriptionPathHonoursAllowManagement`、`TestAdminListenerServesSubscriptionPath`（`cmd/prism/subscription_routing_test.go`）。

**事实**：该文件不存在；三个测试名全仓库无命中（仅出现在 SECURITY.md）；`shouldRouteControlPlane`（`inbound_mux.go:160-179`）与 `isManagementPath`（`admin_runtime.go:86-105`）里也没有任何 subscription 路径路由。

**同段其余两个测试确实存在**（`subcommands_test.go:447`、`:486`）。

**修法**：删除这三个不存在的条目，或补上真实测试。

---

### 18. [中] `docs/SECURITY.md` 编号重复且 §1.8 乱序（文档）

`### 1.9` 出现两次（`:289` Node target policy、`:372` Configuration reporting never discloses secrets），`### 1.8` 出现在 `:357`（在第一个 1.9 之后）。§2 里多处「§1.x」跨引用有歧义风险。

---

### 19. [中] 同一族：`SECURITY.md` 另有 3 处与代码不符（文档）

| 位置 | 声明 | 事实 |
|---|---|---|
| `:297` vs `:333` | 前句「`PRISM_DENY_PRIVATE_NODES=true`（默认 true）」、后句「off by default」 | `env.go:192` 默认 true，**后句错** |
| `:565`/`:567` | 「Direct target policy is off by default」「(the default before v0.1.0)」 | `env.go:191` 默认 true，**表述误导** |
| `:494-497` | 「`ValidateProxyTokenForV1` additionally requires 16+ characters ... rejects reserved words」 | 16 字符下限与保留字检查都在 `LoadEnvConfig`（`env.go:257,271,277`）；`ValidateProxyTokenForV1` 自身不做（`:268-270` 注释承认） |
| `:122-123` | 「Inside the reverse proxy the token is compared again in constant time」 | 字面属实，但真正的闸门是 mux 的非常量时间比较（`inbound_mux.go:145`） |

---

### 20. [中] 导出侧静默丢弃/降级字段（导出/订阅）

| 缺陷 | 位置 | 机制 |
|---|---|---|
| wireguard `reserved` 丢失 | `uri.go:611`（`mapUintSlice` 只收 `[]any`+`float64`） | canonical 文档里 `Reserved []uint8` → JSON base64 字符串 → `len(reserved)==3` 恒假。同包已有 `wireGuardReserved`（`clashmap.go:791`，处理 base64），uri 路径没跟 |
| hysteria v1 速率双向丢失 | `uri.go:433,436`（只读 `up`/`down`） | URI 导入写 `up_mbps`/`down_mbps`（`share_links.go:111-116`）、Clash 导入写 `up:"20 Mbps"`（`parser.go:1633-1642`）→ 导出照抄 `up=20 Mbps`，解析端 `strconv.ParseUint` 失败静默丢弃。hysteria2 有 `shareLinkRateSupported` 守卫（`uri.go:337`），v1 没有 |
| SOCKS4/4a 被改写成 SOCKS5 | `uri.go:87`（硬编码 `proxyLink("socks5",...)`）、`clashmap.go:307` | 导入端认版本（`parser.go:1805-1821`、`:2302-2307`），导出端不看 `version` → 语义从 SOCKS4 变 SOCKS5 |
| vmess `allowInsecure` / hysteria2 `pinSHA256`/`ca` 不写 | `uri.go:106-122`、`:336-384` | 解析端会读（`parser.go:3050`、`:3673-3685`）→ hysteria2 带证书固定的节点导出后**丢失证书校验材料**（安全降级） |
| mihomo 丢弃 SSH `host-key` 等 | `clashmap.go:290-305` 等五个函数 | 导入端会读（`parser.go:1789`）→ 导出的 SSH 客户端不再校验服务器主机密钥 |
| TUIC `reduce-rtt` 解析端不读 | `uri.go:469-471` vs `share_links.go:41-63` | 字段往返丢失 |

### 21. [中] Clash shadow-tls `version: 2` 被静默改写成 3（订阅解析）

**位置**：`internal/subscription/chains.go:383-387`（`if version != "1" { dep["version"] = uint64(3) }`）

只区分「是不是 1」。来源写 `version: 2` 被提升为 3，而 sing-box 明确支持 v1/v2/v3 且 v3 握手实现不同 → 节点「能导入但永远连不上」，不产生任何 skip 记录。测试只覆盖 v3。

**修法**：透传 `1/2/3`，缺失时默认 3，无法解析的按 `INVALID` skip。

---

### 22. [中] sing-box 导出链依赖 tag 未去重，可让整份配置被拒（导出）

**位置**：`internal/export/singbox.go:117`（`object["tag"] = item.Name + "/" + node.DepTag(i)`）、`:59`（`uniqueTag` 只对 selector/auto 组 tag 去重）

节点名可含 `/`（`names.go:180-183` 只压缩空白与截断）。若另一节点渲染名恰为 `x/d0`，文档里出现两个相同 tag → sing-box `checkOutbounds` 对**整份配置**报 `duplicate outbound/endpoint tag`。`singbox.go:12-17` 的注释声称「every tag in the document is unique」，与实现不符。

**修法**：把所有已发出的 tag 放进同一个 `taken` 集合逐一出栈去重。

---

### 23. [中] dangling `detour` 既没剥离也没上报（订阅解析，与文档不符）

**位置**：`internal/subscription/chains.go:163-165`、`:224-243`、`:247`；同类 `:289-291`、`:315-324`

**文档**：`docs/PROTOCOLS.md:213-215` 写「a dangling `detour` is stripped from a standalone object」「an unresolvable reference is reported instead of being guessed」。

**事实**：`deps` 为空时把原始对象**原样**存成节点（`:247`），detour 保留、不加 skip 记录。构建时 `RewriteDepDetours` 只重写 `^d[0-9]+$`（`envelope.go:117,306`），所以 dangling 的 `detour: "d0"` 会被改写成 `<base>/d0`、`detour: "someTag"` 保持原样，两者拨号时都得到 sing-box 的 `outbound detour not found`——正是 `protocol_matrix_test.go:172` 明确断言不得出现的失败模式。

**修法**：dangling 分支二选一——删除 `detour` 字段，或记 skip 并丢弃节点。

---

### 24. [中] 订阅刷新：parse summary 在 stale guard 之前写入（订阅/并发）

**位置**：`internal/topology/subscription_scheduler.go:233`（`recordParseReport` 在 fetch/parse 之后立即执行）vs `:264-273`（`ConfigVersion`/`LastAppliedSeq` stale guard）

`recordParseReport` → `SetParseSummary`（`subscription.go:191-201`）与 `onParseReport` 持久化**都没有守卫**。两个重叠尝试中，后完成的旧尝试会把 `parse_summary` 覆盖成旧内容，而节点集合用的是新尝试的结果——报告与实际生效节点集不对应。

**修法**：把 `recordParseReport` 移进 `WithOpLock` 且在两个 stale guard 之后；或给写入带 `attemptSeq`。

---

### 25. [中] `Platform.qualitySnapshot` 无锁写与 viewMu 下的读并发（platform，`-race` 可报）

**位置**：`internal/platform/platform.go:212`（`SetQualitySnapshot` 无锁写）、`:154`（`evaluateNode` 在 `viewMu` 下读）

两把锁不是同一把，写与读可真正并发。**当前无害**（`pool.qualitySnapshot` 只在启动注入一次），但机制错误。

**修法**：改 `atomic.Pointer`，或把 `SetQualitySnapshot` 纳入 `viewMu`。

---

## 三、轻微（加固项）

| # | 缺陷 | 位置 |
|---|---|---|
| 26 | `EphemeralCleaner.Stop`/`SubscriptionScheduler.Stop` 无 `stopOnce`，重复 Stop panic（同类 `LeaseCleaner:46-49` 有） | `ephemeral_cleaner.go:52`、`subscription_scheduler.go:95` |
| 27 | `socks5AuthObserver` 吞掉 `CloseWrite`（与 #3 同族；包内其他包装器都实现了） | `cmd/prism/proxy_auth_guard.go:101-108`，受害点 `tunnel.go:233` |
| 28 | 非 TLS 隧道也按 TLS 握手采样延迟，污染路由用的 TD-EWMA 表 | `tunnel.go:128`、`tls_latency_conn.go:31` |
| 29 | 节点目标策略只在建 outbound 时解析一次，拨号时不复查（DNS rebinding 窗口）；直连路径有 `ControlContext` 复查 | `outbound/manager.go:106-122` vs `direct_dial_guard.go:119-132` |
| 30 | provider `config.url` 可指向私网（无 `addrpolicy` 校验），带 userinfo 的 URL 会回显在 API 响应 | `intel/providers/settings_api.go:619-628`、`:737-746` |
| 31 | `.env` 备份沿用旧文件权限，世界可读的旧 `.env` 留下世界可读的 token 副本 | `cmd/prism/subcommands.go:183-200` |
| 32 | 队列尺寸校验整数溢出（`2*batch` 在 `batch>=2^62` 时溢出为负，校验通过） | `internal/config/env.go:437` |
| 33 | inbound mux 的 token 比较非常量时间，且它才是真正的闸门 | `cmd/prism/inbound_mux.go:125,145` |
| 34 | 解析没有节点条数上限（唯一的界是 32 MiB 字节数） | `subscription/parser.go:98` |
| 35 | `deferItem` 的 `errorCode` 参数是死参数（`DeferJobItem` 不存 error_code） | `internal/intel/jobs/manager.go:716-722` |
| 36 | `jobGate` fail-open（`GetJob` 出错返回 `gateOpen`） | `manager.go:693-706` |
| 37 | `RefreshJob` 非事务读-改-写：300 轮并发探针仅 1 次复现（0.05%），窗口真实但极窄 | `repo_jobs.go:530-571` |
| 38 | `SetConcurrencyPerCheck` 丢弃旧 semaphore → 正在运行的 Run 与新 Run 用不同 channel，改配置瞬间同一规则可超额并发 | `checks/engine.go:397-408` |

---

## 四、性能观察（非缺陷）

- `checks.Engine.selectRules` 对每条规则调一次 `EnabledSource.CheckEnabled`，而生产实现（`cmd/prism/intel_runtime.go:279`）每次调用都做一次全表 `SELECT ... FROM intel_provider_settings`。8 条内置规则 → 每个节点每步 8 次全表扫描。表仅约 20 行，实测影响有限。探针：`internal/intel/checks/audit_probe_test.go`（8 规则 → 8 次查询，符合预期，非缺陷）。

---

## 五、检查后判定无缺陷的区域（正面确认）

- **`internal/scanloop`**：单个复用 timer、初始触发被 drain、jitter 有下限保护，无 ticker 泄漏。
- **`internal/routing/lease.go` + xsync 语义**：`CreateLease`/`DeleteLease`/`DeleteLeaseIfOlderThan` 的 Dec/Inc 与删除在同一 bucket 锁内；核对 xsync v4.4.0 `doCompute` 确认 `valueFn` 单次尝试内至多调用一次；`Range` 先拷贝再回调，嵌套 `Compute` 不自锁。
- **`lease_cleaner.go`**：过期双重校验（外层 `ExpiryNs` + 锁内 `current.ExpiryNs`）正确。
- **`scheduled_rotator.go`**：tombstone 在删除临界区内写入，不会把刚旋转走的 IP 再发出去。
- **`rotation_tombstone.go`**：LRU 上限 100k、读时淘汰、`MoveToFront` 同锁，无泄漏。
- **`internal/node/hash.go`**：信封/普通两种形态字段裁剪明确，`json.Marshal` 对 map 键排序保证确定性。
- **`internal/intel/store` 并发模型**：`SetMaxOpenConns(1)` + WAL + `busy_timeout=5000` 消除了 SQLite 写并发问题。
- **`intel` via-node 步骤**（`steps.go:618-865`）：`answeredViaNodeSources` 防重复扣配额、per-node 429 cooldown、budget 先门控后扣减——写得很严密。
- **`intel` checks 步骤**（`steps.go:871-969`）：`NotExecuted` 规则不落库、无损 defer——修复 4 的成果正确。
- **`docs/SECURITY.md` §1.12/§1.13 等声明**：逐一核对了列出的测试函数，全部真实存在（§1.5 除外，见 #17）。
- **`repo_provider.go` 的 `ConsumeViaNodeBudget`**：门控在扣减之前，无 #6 的问题。

---

## 六、状态持久化域（`internal/state`）审计结论

原委派的第 5 个子 agent 运行 119 分钟 / 736 次工具调用后陷入原地循环，已由我终止并亲自完成该域审计。

**判定无缺陷的区域**（逐项核对）：

- **`HardenDBFiles`（`schema.go:47-68`）**：目录 0700、文件与 `-wal`/`-shm` 三个后缀全部强制 `PrivateFileMode`，`os.ErrNotExist` 正确跳过。`OpenDB` 在 pragmas 前后各加固一次（`:77`）。
- **`FlushTx`（`repo_cache.go:361`）**：单事务、upsert 按依赖顺序（static → sub_nodes → dynamic → latency → leases）、delete 按反向顺序，`defer tx.Rollback()` 正确。
- **`FlushDirtySets`（`engine.go:120-164`）**：drain 五个 set → 失败时 `remerge()` 全部合并回去 → 返回包装错误。**失败不丢数据**。
- **`classifyDirtySet`（`engine.go:98-115`）**：`OpDelete` 直接进 deletes；upsert 路径若 reader 返回 nil 则**降级为 delete**——避免把已删除对象写成幽灵行。这个细节做得对。
- **`CacheFlushWorker`（`flush.go`）**：`stopOnce` 守卫、`Stop()` 前做 final flush、`dirty == 0` 时跳过空刷。
- **`RepairConsistency`（`consistency.go`）**：ATTACH 后单事务执行 5 条 orphan 清理，顺序按依赖（subscription_nodes → nodes_static → nodes_dynamic → node_latency → leases）；`defer` 注册顺序使 rollback 先于 DETACH。
- **迁移文件与版本常量同步**：`stateLatestVersion = 16`，实际文件 `000001`–`000016` 齐全。
- **`000009_platform_regex_filter_rules` 无 down 文件**——**非缺陷**：文件头注释明确说明 `ANY`/`MUST_NOT` 规则无法用旧格式表达（不可逆），且全仓库无任何 downgrade 调用点（`Down()`/`Steps(-1)` 零命中）。
- **`schema.go:86` 与 `store.go:60` 均为 `SetMaxOpenConns(1)`**：单写连接 + WAL + `busy_timeout=5000`，消除了 SQLite 写并发问题。

**该域未覆盖**：`repo_state.go`（927 行）与 `repo_cache.go` 的 SQL 逐条语义、`migrate.go` 的 `prepareLegacyStateBaseline` 兼容检测路径、`engine.go` 的 `DirtySet` 内部实现细节。

---

## 七、未覆盖区域

- **无真实部署复现**：所有结论基于静态阅读 + 单元/集成级探针，未在真实部署上验证（例如 #14 的 slowloris 实际连接耗尽、#12 的跨租户熔断）。
- **未逐行读**：`internal/probe/manager.go`（约 2200 行，只按接口读了状态机）、`internal/netutil`（1164 行，只读关键分支）、`internal/node/entry.go`、`internal/node/latency.go`、`internal/subscription/openvpn.go`、`skips.go`、`yaml_guard.go`、`parser.go` 的 vless/vmess Shadowrocket/Quantumult 分支、`csvjson.go`、`internal/outbound/builder.go` 全文、`internal/addrpolicy`（`HostIsForbiddenLexically` 的 `inet_aton` 兼容拼写表等）。
- **未验证的假设**：sing-box 节点拨号的解析时机（#29）、demux 清空 deadline 后的实际挂起行为（#14）、SOCKS5 半关闭丢失的实际截断效果（#27）。
- **前端（`internal/api/web/`）**：未审（token 存储位置、是否写 localStorage）。
- **构建标签特有路径**：`with_openconnect`/`with_openvpn` 等标签下的额外代码路径未审。
- **`internal/api/web` 之外的 WebUI 鉴权语义**：`/ui` 静态资源本身不鉴权，是否有敏感构建产物未查。
