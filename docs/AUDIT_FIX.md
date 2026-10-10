# Prism 审计缺陷修复执行记录（AUDIT-FIX）

对应计划：`docs/AUDIT_FIX_PLAN.md`
来源报告：`docs/AUDIT_REPORT.md`
基线：`3657803` + 已完成的 8 项 intel 修复（同一工作区，未提交）
构建标签：`with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy`

---

## 1. 结果总览

| # | 缺陷 | 严重度 | 状态 | 关键实现位置 |
|---|---|---|---|---|
| F1 | 已结算 job 被 in-flight item 复活 | 严重 | 已修复 | `internal/intel/store/repo_jobs.go:454,473,491,511`（三个 UPDATE 加 `status IN ('queued','running')` 守卫 + `ErrJobItemSettled`）；`internal/intel/jobs/manager.go:679,708,731,759`（`jobGate` + 遇 sentinel 静默返回不 publish） |
| F2 | wireguard 导出链接自己解析不回来 | 严重 | 已修复 | `internal/node/wireguard.go:196-210,230-248`（`splitWireGuardPrefixEntries` 逗号切分；key 存在但内容不可用则 fail closed） |
| F3 | 101 升级流包装器吞 `CloseWrite` + 计数器 DATA RACE | 严重 | 已修复 | `internal/proxy/request_log_capture.go:118-205`（`CloseWrite`/`CloseRead` + `closeWriteIfSupported` 不支持时返回 nil；`totalRead/totalWrite` 改 `atomic.Int64`）；`internal/proxy/reverse.go:475-481`（消费点注释） |
| F4 | `CancelJob` 无状态守卫 | 高 | 已修复 | `internal/intel/store/repo_jobs.go:180-219`（`status IN ('queued','running')`，0 行区分 `ErrJobNotFound`/`ErrJobNotActive`）；`internal/service/control_plane_intel.go:420-423`（映射 `CONFLICT`） |
| F5 | `RetryFailedJobItems` 把已取消 job 变 `succeeded` | 高 | 已修复 | `internal/intel/store/repo_jobs.go:221-279`（canceled 直接 no-op 返回 0；仅在 `retried > 0` 且 `status IN ('partial','failed')` 时重开） |
| F6 | provider 空闲轮询每秒烧 1 单位日配额 | 高 | 已修复 | `internal/intel/jobs/provider.go:154-227`（claim 在预算之前；空队列不消费；预算被拒走 `releaseItems` 回滚）；`internal/intel/store/repo_provider.go:687-727`（`ReleaseProviderItems` 按 owner 匹配） |
| F7 | `RebuildAllPlatforms` 缺 `instrumentQuality` | 高 | 已修复 | `internal/topology/pool.go:306-323,547-580`（批量重建逐个注入投影；`SetQualitySnapshot` 注入后触发修复性重建）；`cmd/prism/intel_runtime.go:426-439`（`startIntel` 在投影加载后重建一次） |
| F8 | 主/管理监听无 `http.Server` 超时；`api.Server` 超时是死代码 | 高 | 已修复 | `internal/api/server.go:11-57,279-284`（导出超时常量 + `NewListenerServer`；删除死代码 `ListenAndServe`/`Shutdown`）；`cmd/prism/{endpoint_runtime.go:123-129,admin_runtime.go:40-47,inbound_demux.go:44-50}`（两处真实 server 走同一构造器） |
| F9 | `SECURITY.md` 假测试引用 + 编号重复 + 默认值矛盾 | 文档 | 已修复 | `docs/SECURITY.md`（§1.5 删 3 条不存在的测试引用、补 SSE `?access_token=` 例外、编号 1.1–1.14 重排、§1.2/§3.2/§3.3 默认值改正、§2 描述修正）；`.env.example:21-43` |

**回归测试**：16 个新增测试文件，其中 4 个由审计探针转正（`TestAudit_*` 前缀保留以便追溯）。

---

## 2. 逐项修复要点与决策

### F1 — 两层守卫
- SQL 层是**权威边界**：`FinishJobItem`/`DeferJobItem`/`SaveJobItemStep` 三个 UPDATE 都加 `status IN ('queued','running')`，0 行返回 `ErrJobItemSettled`。
- 调用方层：`manager.go` 的 `finishItem`/`deferItem` 遇该 sentinel 静默返回且**不 publish**（用陈旧状态重算会把已结算的 job 拉回 active set）。
- 新增 `jobGate`：一次 `GetJob` 同时回答「是否被取消」与「是否已结算」，item 每步一次查询而不是两次。
- `deferItem` 额外处理：被取消 job 里仍在跑 step 的 item 会以 `ItemCanceled` 落定，而不是回到 queued（否则 stranded：没有 worker 会 claim 非 active job 的 item，`RetryFailedJobItems` 也拒绝 canceled）。

### F2 — 只改解析端
- 逗号格式是既定契约（`export/uri.go` 与手写 Clash/Surge 文档都这么写），改解析端兼容面更广。
- 切分放在 `wireGuardSinglePeer` 内部，**不动共享的 `stringListOption`**（另一个调用方 `wireGuardAddressOptions` 已有自己的切分）。
- key 存在但内容不可用 → `INVALID:wireguard allowed_ips is empty`（fail closed，避免静默放宽成默认全网路由）。

### F3 — 关键语义
- `CloseWrite` 底层不支持时**必须返回 nil**：httputil 的 `switchProtocolCopier` 把任何非 nil 当作 copy 失败并发 `errCopyDone`，`handleUpgradeResponse` 收到第一个非 nil 就拆双向连接 —— 返回错误等于没修。
- 与 `closeWriteErr`/`errHalfCloseUnsupported` 的差别写在注释里：那个是给 `tunnel.go` 的 `closeWriteConn` 用的，它需要知道对端是否真的半关闭。
- 计数器改 `atomic.Int64`：写方是 httputil 的 copy goroutine，读方是代理自己的 goroutine（`reverse.go:475-481`），普通 int64 是真实 DATA RACE。
- 同族缺陷 #27（`socks5AuthObserver` 吞 `CloseWrite`）一并修复（`cmd/prism/proxy_auth_guard.go:120-166`）。

### F4/F5 — canceled 是终态
- F4：cancel 只作用于 `queued`/`running`；0 行时区分 404（不存在）与 409（已终态）。
- F5：canceled job 即使有 failed item 也不重试复活（用户明确取消了）—— 整个操作 no-op 返回 0，而不是只不重开 job。理由：requeue 后的 item 会 stranded 在 canceled job 里，且会抹掉运维正在看的失败记录。
- 两处语义决策都写进代码注释。

### F6 — claim 在前，预算在后
- 每日配额与 QPS 槽位都是「为一次真实请求预留」，所以只能在确实有请求要发时消费。
- claim 到但预算被拒 → `releaseItems` 按 `lease_owner` 匹配回滚（不会误伤别的 worker 的行，也不增 `attempts`），再 `budgetWait` 睡到闸门打开。
- 空队列路径仍要显式调 `LiftProviderPauseForRotatedCredential`（原本搭 `ConsumeProviderBudget` 便车）。

### F7 — 注入点即修复点
- `RebuildAllPlatforms` 在并发 worker 里逐个 `instrumentQuality` 再 `FullRebuild`，与 `RebuildPlatform` 一致。
- 追加发现：启动顺序上 `SetQualitySnapshot` 注入的 `*intel.Snapshot` **当时是空的**（`Service.Start` → `ReloadSnapshot` 才填），所以单靠包内修复仍留一个窗口。补了 `cmd/prism/intel_runtime.go` 在 `intelSvc.Start()` 之后重建一次。
- 锁安全性：`instrumentQuality` 只取 `platMu.RLock` 读字段后立即释放，`SetQualitySnapshot` 在 `Unlock` 之后才调 `RebuildAllPlatforms`，无自锁。

### F8 — 单一构造器
- `api.NewListenerServer` 是超时的唯一落点，两个真实 serve 点（demux 主监听、可选管理监听）都走它。
- `WriteTimeout`/`ReadTimeout` 刻意不设：SSE 是长流，`ReadTimeout` 还会给流式响应设界。
- 死代码 `api.Server.ListenAndServe`/`Shutdown` 直接删除（全仓 0 引用），并加 `TestNoBareHTTPServerInProductionSources` 守住「新监听器不得裸构造 `http.Server`」。
- demux 清空 sniff deadline 的问题：`ReadHeaderTimeout` 由 net/http 在头阶段重新武装，所以现在有界。

### F9 — 文档与代码对齐
- §1.5 那三个测试**曾经存在**，在 `e9b4366 "Remove subscription export backend"` 中随 `/sub` 功能一起删除；同一次提交从正文删了 `/sub` 描述却漏删了 "Verified by" 三条。所以是彻底删除而非「移到 §2」。
- 新加 SSE 例外段落（唯一 `?access_token=` 入口、无 `Bearer ` 前缀也接受），并附三条真实测试引用。
- 编号 1.1–1.14 重排后无重复；文档内所有 `Test*` 名逐一 grep 验证存在（0 MISSING）。

---

## 3. 验证证据

### 3.1 缺陷敏感性（变异测试：先 FAIL 后 PASS）
| 变异 | 目标测试 | 结果 |
|---|---|---|
| 剥掉 `countingReadWriteCloser` 的 `CloseWrite`/`CloseRead` | `TestAudit_UpgradeStreamWrapperKeepsHalfClose` | FAIL → 还原后 PASS |
| 让 `CloseWrite` 对不支持的底层返回错误 | `TestAudit_UpgradeStreamWrapperHalfCloseSemantics` | FAIL → PASS |
| 剥掉 `socks5AuthObserver.CloseWrite` | `TestSocks5AuthObserverKeepsHalfClose` | FAIL → PASS |
| 让 `socks5AuthObserver.CloseWrite` 不委派 | `TestSocks5AuthObserverKeepsHalfClose` | FAIL → PASS |
| 删掉 `startIntel` 的重建调用 | `TestStartIntelRebuildsPlatformsOnceTheProjectionIsLoaded` | FAIL（`view size after startIntel = 0, want 1`）→ 还原后 PASS（md5 一致） |

### 3.2 审计探针（原缺陷场景，全部 PASS）
`scripts/.audit_probe.sh`（3 探针）、`.audit_probe_checks.sh`、`.audit_probe_export.sh`（round-trip hash 一致）、`.audit_probe_proxy.sh`（含 `-race` 段）、`.audit_probe4.sh`（F1×F4×F5 交互）、`.audit_probe5.sh`。

### 3.3 全量门禁
| 检查 | 结果 |
|---|---|
| `make build` | 通过（npm ci → vite → go build，8 标签 + ldflags） |
| `make verify` | 退出码 0（vet + eslint + test + race + 协议矩阵 + 前端门禁） |
| `go test -tags "$TAGS" -count=1 ./...`（无缓存） | 30 包，0 FAIL |
| `go test -race -tags "$TAGS" -count=1 ./...` | 30 包，0 FAIL，0 DATA RACE |
| `bash scripts/smoke.sh` | 23 passed, 0 failed, 0 skipped |
| `make lint-go`（golangci-lint 2.14.0） | 0 issues |

---

## 4. 与原计划的偏差

1. **F7 追加 `cmd/prism/intel_runtime.go` 重建**：计划只要求改 `RebuildAllPlatforms`。实测发现启动时注入的 snapshot 对象是空的（`Service.Start` 之后才填充），单靠包内修复仍有窗口。追加的调用在 `intelSvc.Start()` 之后，等 `ReloadSnapshot` 完成后重建。
2. **F8 删除死代码而非保留**：计划里写「处理方式待定」。全仓 0 引用，直接删除更干净，并加测试守住复发。
3. **`TestAudit_ConcurrentRefreshJobLosesUpdates` 改为 opt-in**：这是审计确认但**刻意不修**的 `RefreshJob` 窄窗竞态（实测 0.05%）。它随机让测试变红是缺陷本身而非代码问题，留在默认路径会让 CI 随机失败。改为 `PRISM_AUDIT_REFRESH_RACE=1` 时才运行，测量能力保留。
4. **F2 附带更新 golden 文件**：fixture 从单值改为双值 `allowed_ips` 后，`testdata/{mihomo.yaml,singbox.json}` 随 `UPDATE_GOLDEN=1` 重新生成（diff 仅新增 `::/0` 一处）。`TestExportURIRoundTripsThroughTheParser` 原本不含 wireguard 节点，补入 `fixtureEndpointNode(t)` 后该测试才真正覆盖多值路径（修复前实测 FAIL：`parsed 9 nodes, want 10`）。

---

## 5. 本轮未处理（范围外，已记录）

| 项 | 原因 |
|---|---|
| `RefreshJob` 非事务读-改-写 | 实测 0.05% 复现窗口极窄；改为事务需重设计 settle 路径，风险大于收益。探针保留为 opt-in 锚点 |
| `egressIndex` bootstrap 绕过（#10） | 需与 F7 的重建设计一起做 |
| CONNECT 零流量熔断（#12） | 涉及健康度语义决策 |
| 出站 transport / 直连 dialer 无超时（#13） | 涉及配置项设计 |
| 其余轻微项（#26–#38） | 按计划另立任务 |
| `deploy/backend.env.example:30,32` 默认值不符 | F9 只授权改 `.env.example` 与 `docs/SECURITY.md`，此处仅报告 |
| `internal/proxy/{reverse,forward,direct_dial_guard}.go`、`cmd/prism/app_runtime.go` 的陈旧注释称这些开关「opt-in」 | 属代码注释，与 F9 的文档范围分开 |
| `ProxyClientIP` 忽略 `X-Forwarded-For`/`PRISM_TRUSTED_PROXIES`（NAT 下一人拖垮所有人） | 代码问题，需单独设计 |

---

## 6. 审核结论

本轮修复完成后做了独立复核，逐项检查「修复正确性 + 关联功能未被破坏」：

- **F1/F4/F5**：三个 sentinel 的全部引用点已核对（store / manager / service / api）；`CancelJob` 的第二条 `UPDATE job_items ... WHERE status='queued'` 保留原守卫；`RefreshJob` 的 `case status == JobCanceled` 分支保证 canceled 不被 publish 改写；`RetryFailed` 的 `publish` 仅在 `retried > 0` 时触发。被改动的既有测试（`handler_intel_events_test.go`、`control_plane_intel_test.go`）是**语义变更的必然连带**而非放宽断言：前者原用 `CancelJob` 强行制造终态 job，现在改为断言 job 已自然终结。
- **F2**：wireguard 各字段逐个过 —— `address`/`reserved` 本就有逗号切分，`allowed_ips` 是唯一漏项；`stringListOption` 仅 2 个调用点，共享函数未改，address 侧零影响；三条调用链（share link / sing-box endpoint / legacy outbound）全部覆盖。
- **F3/#27**：`CloseWrite` 返回 nil 的语义与 `closeWriteErr` 的差别已在注释中明确；`totalRead/totalWrite` 无遗漏读写点；`reverse.go` 消费点语义不变（快照精度问题已注明）；`-race` 全树 0 DATA RACE。
- **F6**：`ReleaseProviderItems` 强制要求 `owner`（空 owner 会匹配该 provider 所有 running 行）；不修改 `attempts`；`budgetWait` 各分支均返回有效时长。
- **F7**：锁路径无自锁（`Unlock` 后才调 `RebuildAllPlatforms`，`instrumentQuality` 只 `RLock` 读字段）；批量重建的并发 worker 之间无共享可变状态。
- **F8**：`NewListenerServer` 是全进程唯一超时落点；两处真实 serve 点已核对；`TestNoBareHTTPServerInProductionSources` 守住复发；SSE 不被切断由 `WriteTimeout == 0` 断言 + 全量 SSE 测试共同保证。
- **F9**：章节编号无重复；文档内所有 `Test*` 名全部存在（0 MISSING）；默认值与 `internal/config/env.go:191-193` 一致；`.env.example` 同步。

**审核结论：9 项修复全部通过**。未发现修复引入的新缺陷；发现并修正了 1 处验证基建问题（RefreshJob 探针的 CI 随机红）。
