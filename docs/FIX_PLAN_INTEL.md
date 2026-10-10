# Prism Intel 修复计划

基线提交：`3657803`（origin/main）
建立时间：2026-10-09

## 背景

真实部署验证中发现以下问题，按依赖顺序修复。

核心定位澄清（用户确认）：
- **健康度、网络特征、解锁结果都是标签**，各管各的，互不当门槛。
- 节点只要**健康**就入池。解锁检测**只如实记录结果**，没解锁也能用。
- 解锁结果用于**筛选/分类**，**不是准入门槛**（平台策略里的 `required_checks` 保持可选、默认空）。

## 修复清单

### 修复 1：coverage 分母别名 bug（最高优先级）✅

**根因**：`cmd/prism/intel_runtime.go:366` 的 `intelEnabledSources` 只遍历
`assess.ScoringSources()`，该列表**不含 `proxycheck_node`**。当宿主侧 `proxycheck` 关闭时：
- 分母：`enabled["proxycheck"]=false` → 权重 3 被剔除
- 分子：`proxycheck_node` 证据经 `ProviderAliases` 折回 `SourceProxycheck`（`assess.go:384`）
  仍计入 `components()` → 权重 3 进入分子
- 结果：`coverage` 被 1.0 截断 → 置信度虚高 → `incomplete` 判定与 `min_confidence` 准入失真

`docs/INTEL.md §9` 已将此记录为「未确定是否为有意行为」。经独立查证，**是 bug**。

**修法**：`intelEnabledSources` 增加反向别名解析——某来源的任一别名变体启用时，该来源计入分母。

**改动点**：`cmd/prism/intel_runtime.go` `intelEnabledSources`
**验证**：新增单元测试覆盖「只开 proxycheck_node、关 proxycheck」时 coverage 不被高估。

---

### 修复 2：渠道收敛 + 权重重分配 ✅

**去掉**（强制 key，十万节点规模跑不动）：
| 源 | 权重 | 原因 |
|---|---|---|
| `abuseipdb` | 2 | 强制 key（`online.go:381` 硬拦截），1000/天 |
| `ipqs` | 3 | 强制 key（`online.go:537` 硬拦截），5000/月，禁商用 |
| `proxycheck`（宿主） | 3 | 被 `proxycheck_node` 完全覆盖 |

**保留**（零配额或按节点计）：
| 源 | 新权重 | 类型 |
|---|---|---|
| `ippure` | 4 | via-node |
| `proxycheck_node` | 3 | via-node |
| `ip_api` | 1 | via-node |
| `dnsbl` | 1 | 宿主机 |

**新增**：`ipapi_is` 经节点变体，权重 2（见修复 5）。

权重合计 11（原 14）。**评分架构不动**——`assess.go` 是纯函数、可测试、设计正确，只改
`scoringWeights` 表与默认启用状态。

**改动点**：
- `internal/intel/providers/online.go`：三个源的 `DefaultEnabled` 保持 false（已是 false）
- `internal/intel/assess/assess.go`：`scoringWeights` 重分配
- `docs/INTEL.md §2.2/§3.2`：同步文档

**注意**：`abuseipdb` 去掉后 `FlagAbuse`、`ReasonRecentAbuse`、`ReasonAbuseConfidence`
不再触发（滥用举报历史永久失去）。这是零配额约束下的必要代价，已在文档说明。

---

### 修复 3：`skipped(no egress)` 假成功 ✅

**根因**：`internal/intel/steps.go` 的 `skipped()` 返回 `StepResult{Summary: ...}`，
`Skip` 字段为 **false**。而 `manager.go:525` 只在 `result.Skip == true` 时才 `finishItem`。
所以「无出口观测」的节点会继续走完 step3/4/5（每步空转返回 skipped），最后
`finishItem(ItemDone)` —— **不通的节点被标记为「已完成」，实际零检测**，job 报「成功」。

**修法**：
1. step2/3/4 遇到「无 egress」返回 `DeferUntilNs`（短延迟，如 30s）而非 skipped
2. 超过 `maxItemAttempts`（5 次）后返回 `Skip: true` + 明确 error code
3. 该终态标记为 **`ItemFailed`** 而非 `ItemSkipped`，使 job 变 `JobPartial`

**改动点**：`internal/intel/steps.go`（offlineStep/enqueueOnlineStep/viaNodeStep）
**验证**：新增测试——无 egress 的节点重试超限后为 `ItemFailed`，job 为 `JobPartial`。

---

### 修复 4：超时与并发 ✅

| 问题 | 现状 | 修法 |
|---|---|---|
| `itemTimeout` | 90s，但 8 条 check 规则超时合计 94s | 提到 **180s** |
| job 级总超时 | **完全没有** | 新增 `jobTimeout = 6h`，超时终结为 `JobPartial` 并释放槽位 |
| running job 霸占槽位 | 无让位机制 | 加让位：running 超阈值无进展则降级为 queued |
| `CHECK_BUSY` | 计入 attempts + 指数退避（30s→6h） | 改**无损 defer**，不进 attempts |
| `max_running_jobs` | 16 | 修完槽位问题后提高 |
| `ConcurrencyPerCheck` | 16（per-rule 全局） | 提到 **512**（经节点请求彼此独立，无需全局限制） |

**改动点**：`internal/intel/jobs/jobs.go`（常量）、`internal/intel/jobs/manager.go`（调度）、
`internal/intel/checks/engine.go`（CHECK_BUSY 语义）、`internal/config/runtime.go`（默认值）

**风险：中** —— 涉及调度核心，需补测试。

**实际落地**：

| 常量 | 值 | 位置 |
|---|---|---|
| `itemTimeout` | 180s（8 条规则超时合计 94s） | `jobs.go` |
| `itemLease` | 5min（**必须 > itemTimeout**） | `jobs.go` |
| `jobStallYield` | 30min（无进展 → 排到 queued 之后） | `jobs.go` |
| `jobStallTimeout` | 12h（无进展 + 有排队者 → 结算 partial） | `jobs.go` |
| `checkRetryDeferral` | 15s（CHECK_BUSY 无损停放） | `steps.go` |
| `DefaultCheckConcurrencyPerCheck` | 512（原 16） | `checks/engine.go` |
| `MaxCheckConcurrencyPerCheck` | 4096 | `checks/engine.go` |
| `MaxRunningJobs` | 1024 | `jobs.go` |
| `maxActiveJobsFetched` | `4 * MaxRunningJobs`（防截断把 running 挤出窗口） | `manager.go` |

新增：`CodeItemTimeout`/`CodeJobTimeout` 错误码；`store.FailPendingJobItems`（queued+running →
failed + 清租约，保留 partial 证据，`RetryFailedJobItems` 可重开）；`manager.settleAbandonedJobs`、
`trackProgress`、`stallFor`、`forgetProgress` 与 `progressMu`/`progress` 进度表；`jobGate` 三态
（`gateOpen`/`gateCanceled`/`gateSettled`）替换原 `jobCanceled`/`jobSettled`，并在 `runItem` 入口加
`job.Terminal()` 早退守卫（防终态 job 的 item 被复活）。

CHECK_BUSY 定案：**不落库为 error 行**，`checks.NotExecuted(code)` 判定后走 `DeferUntilNs` 无损停放
（`deferDeadline` 再施加退避下限），claim 仍 `attempts++` 但不进错误计数。

**测试**：`internal/intel/jobs/schedule_stall_test.go`（5 测试：让位/无竞争不结算/放弃结算 partial
且释放槽位/`ITEM_TIMEOUT` 不复活/有进展保持槽位）、`store/jobs_test.go` 的
`TestJobs_FailPendingItemsSettlesTheJobAndStaysRetryable`、`internal/intel/steps_checks_busy_test.go`
（用不同 NodeKey 占住规则槽位**真实复现 CHECK_BUSY**，非只测 CHECK_CANCELED 路径）。

---

### 修复 5：新增 `ipapi_is` 经节点变体 ✅

**依据**：`online.go:759` 确认 key 可选（`if p.key != ""` 才加 header）。匿名额度约 1000/天
**按发起 IP** —— 走节点后变成每节点 1000/天，与 `proxycheck_node` 同理。

**实现**：
1. `internal/intel/providers/vianode.go` 新增 `IPAPIISViaNode` 类型（照抄 `ProxyCheckViaNode` 模式）
2. 复用已有的 `DecodeIPAPIIS`（无需改动）
3. `internal/intel/providers/builtin.go` 注册
4. `internal/intel/assess/assess.go` 的 `ProviderAliases` 增加映射到 `SourceIPAPIIs`
5. 权重 2

**补回的信号**：`IsAbuser` → `Signals.Compromised`、ASN、国家/城市。

**实际落地**：
- `providers/vianode.go`：新增 `IPAPIISViaNodeURL = "https://api.ipapi.is/"`、
  `IPAPIISNodeProfile = "ipapi-is-via-node-v2-2"`、`IPAPIISViaNodeOptions`、`IPAPIISViaNode`、
  `NewIPAPIISViaNodeProvider`（id `ipapi_is_node`、`DefaultEnabled: true`、无 Prism 侧配额）。
  `Lookup` 用 `url.Parse` + `q=<addr>` 查询参数；**密钥永不经节点**。
- `providers/online.go`：`DecodeIPAPIIS` 变薄包装，新增 `DecodeIPAPIISAs(body, ip, now, ttl, provider, profile)`
  显式身份模式（照抄既有 `DecodeProxyCheckAs` 模式），让变体复用同一解码器。
- `providers/builtin.go`：注册新 provider。
- `assess/assess.go`：新增 `SourceIPAPIIsNode = "ipapi_is_node"`；`ProviderAliases` =
  `{"proxycheck_node": SourceProxycheck, "ipapi_is_node": SourceIPAPIIs}`；新增 `scoringSource(provider)`
  helper（`noEvidenceState` 用它匹配）；`voteOf` 加变体 case 保证投票。
- 权重 2；经别名折叠进宿主源，一个地址只出一个分量（精确匹配优先）。

**测试**：`providers/providers_test.go`（`TestIPAPIISViaNode`、`TestIPAPIISViaNodeNeedsAnAddress`、
`TestIPAPIISNodeSpecSpendsQuotaPerNode`）、`providers/registry_test.go` 目录断言、
`assess/assess_vianode_alias_test.go`（4 测试）。

---

### 修复 6：解锁检测 UI 打通 ✅

**现状**：后端能力齐全，前端零入口。
- `NodeFilters.Checks []NodeCheckFilter{ID, Outcome}` 已实现（`control_plane_nodes.go:47`）
- API 接受 `check=chatgpt:available`
- `GET /api/v1/intel/checks` 返回 8 条规则
- 但节点页是**自由文本输入框**（`NodesPage.tsx:900-906`）
- 平台策略 `RequiredChecks` **WebUI 无编辑器**

**修法**：
1. 节点页「检测结果」→ 多选标签（调 `GET /api/v1/intel/checks` 取规则列表），
   保留文本输入作兜底
2. 平台策略新增「解锁要求」编辑器，**默认全不勾选**（不是门槛）

**定位**：解锁结果是**标签**，用于筛选/分类，不是准入条件。

**实际落地**：
- 新增 `internal/api/web/src/features/intel/checks.ts`：`IntelCheck` 类型、`INTEL_CHECK_OUTCOMES`、
  `listIntelChecks(signal)`（`GET /api/v1/intel/checks?limit=100`）、`checkFilterValue`/`parseCheckFilterValue`
  （`<check id>:<outcome>`，兼容旧 URL）。
- `NodesPage.tsx`：新增 `intelChecks` useQuery；`UnlockCheckFilter` 组件（规则下拉 + 结果 Select +
  移除按钮 + 提示「解锁结果是标签，不是准入条件」）替换原自由文本框。规则列表必须来自服务端——
  后端对未知 id 直接 400。
- 平台策略：`platforms/types.ts` 加 `PlatformQualityPolicy` 与 `Platform.quality_policy`；
  `formModel.ts` 加 `required_checks_text` 与 `toQualityPolicy(values, current)`（**保留未编辑字段**，
  空则 undefined）；`NodeCriteriaFields.tsx` 加 `UnlockRequirementsField`（details「高级：解锁要求（可选）」）。
  默认空 = 不要求任何解锁结果。
- **PATCH 是整体替换**（`control_plane_platform.go` 的 `json.Unmarshal` 进零值 struct），所以编辑器
  必须回传未编辑字段，否则静默清空。`PlatformDetailPage.tsx` 的 update mutation 传 `platform.quality_policy`。

**测试**：`tests/platform-criteria.test.mjs` 新增 6 测试（未设置不发送、行文本往返、无结果=任意、
保留未编辑字段、清空保留其余、清空最后一条不发送）。

---

### 修复 7：UI 隐藏数据来源 ✅

**范围**：i18n 字符串（`internal/api/web/src/i18n/quality.ts`）+ 渲染层代码位置。

| 位置 | 改动 |
|---|---|
| `i18n/quality.ts` | 厂商名全部 → 中性表述（含 `Tor Project`、`AbuseIPDB`、`DNSBL`） |
| `presentation.ts` | 删除死代码 `providerName()`（无调用点，改渲染层后已成孤儿） |
| `QualityDetails.tsx` | 删「AbuseIPDB」「Tor Project」两处 SectionTitle trailing；`可配置免费的 AbuseIPDB API Key` → `可配置举报数据源` |
| `PurityGuide.tsx` | 移除 proxycheck.io / ippure.com 外链；3 段文案中性化 |
| `IPPureReview.tsx` | 文案中性化；删除 ippure.com 接口说明外链 |
| `ExitRecordsPanel.tsx` | `IPPure 有效评分/纯净度参考/复核时间` → 中性 |
| `NodesPage.tsx` | 删「网络特征」列表头的 `ProxyCheck` 角标 |
| `NodeIntel.tsx` | `DNSBL 命中` → `黑名单命中` |

**约束**：`types.ts` 的 `provider` / `network_provider` / `source_type` 字段与所有
provider id 字面量（`"ippure"`、`"proxycheck"`、`"abuseipdb"`）**保留**——它们是数据层与
请求 payload 的一部分，只改渲染层。

**验证**：`tests/i18n-coverage.test.mjs`（每个 `t()` 字面量都有字典条目）+ `npm run check:types`
+ `npm run lint` 全绿。后端 provider id 不变，`internal/service/*.go` 的 64 处 id 引用未动。

---

### 修复 8：零散 bug ✅

1. **重复 `probeMgr.Start()`**：`cmd/prism/app_runtime.go` 连续调用两次。`Start()` 无幂等保护
   （`internal/probe/manager.go:308` 直接 `wg.Add` + 起 goroutine），重复调用会起 2 个扫描循环 +
   2×`workerCount` 个 worker，`wg.Add` 翻倍导致 `Stop()` 可能挂起。**已删掉一行**。
2. **纯净度下拉框混入非分档项**：`NodesPage.tsx` 硬编码 `review`/`unknown`。
   `review` 是 verdict 语义（判定下拉框已有该项），**已删**；`unknown` 是真实分档
   （`internal/quality/model.go:211` 的 `PurityBand(nil)` 返回 `"unknown"`，且
   `platform/model_codec.go:74` 的合法词表含它），**保留**。后端
   `handler_node.go:187` 仍接受 `purity_band=review`（老 URL 兼容），只是不再从下拉框发出。

---

## 执行顺序（按依赖）

1. 修复 1（coverage bug）—— 否则后续数据不可信
2. 修复 2（渠道收敛 + 权重）—— 配置层
3. 修复 3（skipped 语义）—— 数据正确性
4. 修复 4（超时并发）—— 解决卡死
5. 修复 5（ipapi_is 变体）—— 补回信号
6. 修复 6（解锁检测 UI）—— 核心需求
7. 修复 7（UI 隐藏来源）—— 展示层
8. 修复 8（零散 bug）

## 验证结果（全部通过）

命令与实测输出：

| 检查 | 命令 | 结果 |
|---|---|---|
| 后端构建 | `go build ./...` | 通过 |
| 后端静态检查 | `go vet -tags '<TAGS>' ./cmd/... ./internal/...` | 通过 |
| 后端测试 | `go test -tags '<TAGS>' ./cmd/... ./internal/...` | **30 包全通过，0 失败** |
| 竞态检测 | `go test -race -tags '<TAGS>'`（intel/api/service/config/quality/platform/topology/cmd） | 14 包全通过 |
| 协议矩阵 | `go test -tags '<TAGS>' -run TestProtocolMatrix ./internal/outbound/...` | 通过 |
| 前端类型 | `npm run check:types` | 通过 |
| 前端测试 | `npm run test:config` | **46/46 通过** |
| i18n 覆盖 | `node --test tests/i18n-coverage.test.mjs` | 2/2 通过 |
| 前端 lint | `npm run lint` | 通过 |
| 前端构建 | `npm run build` | 通过 |

`<TAGS>` = `with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy`。

**注意**：裸 `go test ./...`（不带 `BUILD_TAGS`）会失败 5 个协议包，原因是 QUIC / WireGuard /
OpenVPN / uTLS 都在构建标签后面（`Makefile:7`）。这与本次改动无关，必须用 `make test` 或带上
标签集才能得到真实结果。

## 与原计划的偏差

| 项 | 原计划 | 实际 | 原因 |
|---|---|---|---|
| job 级总超时 | `jobTimeout = 6h`（墙钟） | **取消**，改 `jobStallTimeout = 12h`（无进展）+ `jobStallYield = 30min`（让位排序） | 墙钟 6h 会误杀合法长退避（`backoffMax=6h` × 5 次尝试 ≈ 30h）与 20 万节点大 job；`TestManager_RetriesFailuresWithBackoffAndSettlesPartial` 暴露了这一点 |
| 让位实现 | running 超阈值降级为 queued（写 store） | **纯排序**：无进展 30min 排到 queued 之后；只有 `queuedWaiting > 0` 时才结算 | 无等待者时让位无意义，写 store 还会引入状态竞争 |
| `purity_band=unknown` 下拉项 | 删除 `review` + `unknown` 两项 | **只删 `review`**，保留 `unknown` | `unknown` 是真实分档（`quality.PurityBand(nil)`），且在 `platform/model_codec.go:74` 的合法词表里 |
| `providerName()` | 改成中性标签 | **整个删除** | 改完渲染层后已无调用点，留着就是死代码 |

## 未决事项

- 「节点池少字」：仓库代码正确（`NodesPage.tsx:565` = `t("节点池")`，
  `translations.ts:28` = `"节点池": "Node Pool"`）。疑为部署时构建产物未更新，
  待用户确认具体位置。
