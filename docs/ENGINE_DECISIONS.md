# 引擎决策记录

本文件记录影响运行时内核的决策及其依据。改动这些决策前请先读完对应条目。

## D-1 内核：只用 sing-box，不引入 mihomo

**决策**：Prism 以 sing-box 为唯一运行时内核。原方案 WP07 规划的 mihomo 兜底内核**不引入**。

**日期**：2026-09-24。

**背景**：`docs/plan/07-mihomo-fallback.md` 原本计划用 `github.com/metacubex/mihomo@v1.19.31`
兜底 sing-box 无法表示的 Clash 系协议（SSR、Mieru、VLESS XHTTP/encryption、
Snell v1–v3、AmneziaWG、kcptun/restls/gost-plugin 等），代码放在 `with_mihomo` 构建标签下。

**实测依据**（2026-09-24，在本仓库的 `go.mod` 基础上单独引入 mihomo 测量）：

| 指标 | 仅 sing-box | 加上 mihomo |
|---|---|---|
| `github.com/metacubex/*` 模块数 | 1（`utls`，间接） | **51** |
| mihomo 单独拉入的模块总数 | — | **179** |
| 与 `sagernet/*` 功能重叠的平行分支 | — | `gvisor`、`amneziawg-go`、`utls`、`fswatch` 等 |

其中三项重叠值得单独指出：一个进程内会同时装入
`github.com/sagernet/gvisor` 与 `github.com/metacubex/gvisor`（用户态网络栈）、
`github.com/sagernet/wireguard-go` 与 `github.com/metacubex/amneziawg-go`（WireGuard），
以及 `github.com/sagernet/utls` 与 `github.com/metacubex/utls`（TLS 指纹）。它们模块路径不同，
不会直接冲突，但体积、编译时间与后续版本解析复杂度都会显著上升。仓库在 WP01 已经因为
一个 gvisor 伪版本的 semver 排序问题导致 MVS 选错版本、整个项目无法编译，依赖图越稠密，
这类问题的复发概率越高。

**其他代价**：

- 二进制体积按方案自身实测从 48 MiB 增至 76 MiB。
- **DNS 行为不一致**：方案 §4 明确 mihomo 节点中域名形式的目标只走系统解析器，
  不经过 `PRISM_NODE_DNS_UPSTREAMS`。这会破坏出口探测与延迟探测在节点之间的一致性，
  且属于已知限制、原方案不打算修复。
- mihomo 节点不支持 `dialer-proxy`，只能写进解析报告标 `UNSUPPORTED_FEATURE`。
- 解析攻击面扩大：`adapter.ParseProxy` 处理的是用户导入的订阅内容，即不可信输入。
- 维护成本翻倍：方案 R9 要求两个内核一起锁版本、一起升级、一起通过 `make protocol-matrix`。

**收益范围**：mihomo 不可替代的能力只有四类 —— SSR 存量、Snell v1–v3、kcptun、VLESS XHTTP。
其余列出的类型（mieru、masque、trusttunnel、sudoku、shadowquic、gost-relay、restls）都极为小众。
与此同时 sing-box 已经覆盖 SS/SS-2022、VMess、VLESS（含 Reality/Vision）、Trojan、
Hysteria/Hysteria2、TUIC、AnyTLS、SSH、Snell v4/v6、HTTP、SOCKS、WireGuard（endpoint）、
OpenVPN（endpoint）、OpenConnect（endpoint）。

**落地状态**：

- `go.mod` 不包含 mihomo。
- `Makefile` 的 `TAGS_FULL` 不再包含 `with_mihomo`；`.github/workflows/release.yml` 的
  `TAGS_FULL`、根目录 `Dockerfile` 的 `ARG TAGS` 也已与它对齐（发布镜像由
  `.github/Dockerfile.release` 复制 `release.yml` 产出的二进制，因此继承同一套标签）。
- `internal/node/mihomo_built.go` / `mihomo_notbuilt.go` 与 `ENGINE_NOT_BUILT` 报告路径保留，
  因此遇到这类节点时会得到明确原因，而不是静默丢弃。
- `internal/node/capabilities.go` 的 `MihomoFallbackTypes()` 继续声明这些类型，但 mihomo 的
  `Built` 只有在**引擎运行时层注册了自己**时才可能为 true（`node.RegisterEngineRuntime`）；
  只有 `internal/outbound/singbox_runtime.go` 注册，且 `SingboxRuntime.Build` 对 mihomo 文档
  直接返回 `ENGINE_NOT_BUILT`。因此即便带上 `with_mihomo` 标签，该字段仍为 false：
  `internal/outbound` `TestEngineCapabilitiesMatchRuntimeBehaviour` 与 `internal/api`
  `TestSystemCapabilities_NeverClaimsAnUnbuiltEngine` 固定了这一点。

**何时重新评估**：当实际使用的订阅源中稳定出现 `ssr://`、或大量 Snell v1–v3、kcptun、
VLESS XHTTP 节点，且这些节点丢失会造成真实损失时。届时的接入点已经就位。

## D-2 sing-box 版本策略

**决策**：跟随 sing-box 的**稳定补丁线**，锁定具体版本号；升级必须通过
`make protocol-matrix` 与完整测试。

**当前版本**：见 `internal/node/capabilities.go` 的 `SingboxVersion` 与 `go.mod`。

**依据**：sing-box 在 minor 版本之间会做**破坏性移除**，而不是只做增量。
已实测的两例：

- `ShadowsocksR` 在 1.6.0 移除，1.14.0 中 `type: shadowsocksr` 只剩一个返回错误的桩
  （`include/registry.go` 的 `registerStubForRemovedOutbounds`）。
- `WireGuard` outbound 在 1.11.0 弃用、1.13.0 移除，1.14.0 只保留桩；
  必须改用 `wireguard` **endpoint**。

因此跨 minor 升级（例如 1.14 → 1.15）不是"改个版本号"，需要重新核对
outbound / endpoint 注册表并回归协议矩阵。补丁位升级（1.14.0 → 1.14.1）风险较低，但仍需验证。

**注意**：sing-box 用 `*_stub.go` 文件在不带构建标签时注册**返回错误的同名桩**。
这意味着仅"类型名能在 registry 里解析"**不能**证明该协议可用；
真正的证明是能构造出实例并拨号成功 —— 这也是 `TestProtocolMatrix` 实际拨号的原因。

## D-3 sing-box 上游数据竞争（`route/network.go`）：上游已于 v1.14.2 修复

**状态**：**已关闭**。上游在 sing-box v1.14.2 重构了 `route.NetworkManager`，Prism 已升级到该版本，
`make verify`（含 `go test -race`）在 v1.14.2 上竞态干净。**没有 fork、没有本地补丁、没有 `replace`。**

**记录期间**：sing-box v1.12.21 至 v1.14.1。**日期**：2026-09-24 记录，2026-09-25 关闭。

**当时是什么**：在真实 netlink 接口监视器下运行内嵌 sing-box，并用 Go 竞态检测器（`-race`）时，
会报出一条 `WARNING: DATA RACE`，位置是 sing-box 自己的 `route/network.go`：

- 字段：`NetworkManager.started`（`route/network.go:67`，普通 `bool`，不是原子量）；
- 写方：`NetworkManager.Start` 在 `adapter.StartStatePostStart` 分支执行 `r.started = true`
  （`route/network.go:220`）；
- 读方：接口监视器的回调路径派生的 `go r.updateInterface(...)` 里读 `if !r.started`
  （`route/network.go:574`）。

两者之间没有锁、没有 `atomic`、没有建立 happens-before 的通道，因此按 Go 内存模型就是一个
数据竞争（`bool` 不会撕裂，但仍是未定义行为）。

**上游怎么修的**：v1.14.2（2026-09-24 发布）重写了 `NetworkManager` 的并发模型。该版本的结构体里
**`started bool` 字段已不存在**，取而代之的是：

- `startedCtx context.Context` + `startedCancel context.CancelFunc`（context 的读写本身并发安全）；
- 以及显式互斥量：`stateAccess sync.RWMutex`、`environmentUpdateAccess sync.Mutex`、
  `interfaceUpdateAccess sync.Mutex`、`resetRunAccess sync.Mutex`、`powerUpdateAccess sync.Mutex`。

**验证方式（实测，不是读代码推断）**：升级到 v1.14.2 后运行 `make verify`（含 `-race`），
整轮日志里 `DATA RACE` 出现 **0** 次、`FAIL` 出现 **0** 次、退出码 0。
升级同时推进了 `sing`、`sing-quic`、`sing-tun`、`sing-mux`、`wireguard-go`、`bbolt` 的补丁线。

**测试侧的存根为什么还在**：`SingboxBuilderConfig.QuietInterfaceMonitor` 与
`internal/outbound/quiet_platform.go` 的空实现监视器保留了下来，但理由变了 —— 不再是规避竞态，
而是测试本来就不需要活的 netlink 监视器（Prism 从不使用 `auto_detect_interface` /
`bind_interface`，去掉监视器能让测试二进制不依赖宿主网络栈）。生产路径仍使用真实监视器。

**已作废的可选项**：曾经考虑过的"本地 fork + `replace`，把 `started` 改成 `atomic.Bool`"不再需要。

**何时重新评估**：任何一次 `-race` 运行再次命中 `github.com/sagernet/sing-box/route`。

**相关记录**：`docs/PROTOCOLS.md` §10.5。

## D-4 mihomo **导出格式**已用真实 mihomo 二进制实测（与 D-1 无关）

**决策**：mihomo 继续作为**导出格式**（`format=mihomo`）存在，但**不作为内核**（见 D-1）。本条目只记录
「导出的 YAML 能否被真实 mihomo 解析」这一外部验证的结论。

**日期**：2026-09-27。

**验证方式（实测，不是读代码推断）**：下载 mihomo 官方二进制，用它自己的 `mihomo -t -d <dir> -f <config>`
（只做配置解析与初始化，不监听、不出网）校验 Prism 的真实导出：

1. 取两份真实订阅（802,810 B / 776,662 B，Clash YAML 格式），经 `subscription.ParseWithReport` +
   `export.Export(..., FormatMihomo, ...)` 生成配置。结果：**280 / 235 个节点，导出 0 跳过**。
2. 用 **mihomo v1.19.31**（2026-09-14 构建）校验两份导出：**两份都 `test is successful`，退出码 0**。

**发现并澄清的两点**：

- **`anytls` 需要较新的 mihomo。** v1.19.2（2025-02）报 `unsupport proxy type: anytls`；
  v1.19.31（2026-09）正常接受。这不是 Prism 的导出缺陷，而是旧版 mihomo 尚无该类型。
  mihomo 侧的 `anytls` 字段为 `password` / `server` / `port` + TLS 字段。
- **hysteria 的 `up`/`down` 两种拼写都被接受。** 实测 6 种形态（hysteria2 与 hysteria v1 × 整数 /
  数字字符串 / 带单位字符串）全部 `OK`；作为敏感性对照，`up: true`（bool）与 `up: {a: 1}`（map）
  确实被拒（`expected type 'string', got unconvertible type 'bool'`）。因此
  `clashHysteria2` 输出整数、`clashHysteria` 输出字符串（含单位）**都是正确**的，无需统一。

**残留的非阻塞项**：`mihomo -t` 只校验配置结构与类型，不验证节点能否真正连通（那需要出网并实际拨号，
属于订阅端到端测试的范围，见 `docs/release-notes/v3.0.0.md` §5）。`up: "abc"` 这类语义无效但类型正确的
值不会被 `-t` 拦住——Prism 只在输入确实是数字时生成 `up`/`down`，所以不会产生这种值。

**何时重新评估**：mihomo 更改 proxy 类型名或字段名时；或 Prism 新增需要导出的协议时，重跑同一套校验。

## D-5 sing 上游数据竞争（`common/bufio/cache.go` 的 `CachedConn`）：上游尚未修复

**状态**：**记录中，未关闭**。竞争发生在依赖库，不是本仓库的代码；**没有 fork、没有本地补丁、没有 `replace`**
（延续 D-1/D-2 的依赖策略）。CI 的 `make test-race` 会**概率性**红灯，识别方法见下。

**记录期间**：2026-09-27。**首次观测**：commit `b506888` 的 CI run `36326553534`。

**现象**：`make verify` 的 `test-race` 阶段在 `internal/e2e` 的
`TestOfflineProtocolRoundTrip/shadowtls-v3-shadowsocks-chain` 上报 `WARNING: DATA RACE`，
测试随之失败（`race detected during execution of test`）。同一提交的 `make test`（不带 `-race`）通过。

**竞争点**：`github.com/sagernet/sing/common/bufio.CachedConn` 的 `c.buffer` 字段。

- 读方（`Close` 路径）：`(*CachedConn).Close()` `common/bufio/cache.go:85` 读 `c.buffer`，
  随后置 nil 并 `DecRef` + `Release`；
- 写方（`Read` 路径）：`(*CachedConn).Read()` `common/bufio/cache.go:47` 读 `c.buffer`，
  在 `err != nil` 分支里置 nil 并 `DecRef` + `Release`。

`taken` 字段用了 `atomic.Bool`（`CompareAndSwap`），但 `c.buffer` **没有任何同步**：两个方法都直接读写它。
因此"一个 goroutine 正在读、另一个同时在关"就构成 Go 内存模型下的数据竞争。完整竞争栈里
**没有一行 Prism 的代码**：从 `sing/common/bufio` 经 `sing-shadowsocks` 一直到
`sing-box/route.(*ConnectionManager).connectionCopy`。

**上游状态（2026-09-27 实查）**：`sagernet/sing` 的 `dev`、`main`、`master` 三个分支上
`CachedConn.Close()` 仍是同一写法（`cache.go:83-92`），未修复。Prism 钉的是
`sing v0.9.6-0.20260922013354-87c33f17688f`，由 `sing-box v1.14.2` 的 require 决定。

**触发概率**：概率性，不是必现。本地在 `-race` 下把 shadowtls 用例连跑 **40 次**、整包连跑 **10 次**，
一次都没复现；CI 侧连续 6 次推送里命中 1 次（相邻提交 `8fce32a` 是 success）。
两个 goroutine 的窗口很窄，核数与负载不同，命中率就不同。

**影响面（如实记录，不粉饰）**：`c.buffer` 的引用计数可能错乱（重复 `DecRef`/`Release`，或漏 `Release`），
窗口是"读与关闭并发"。真实流量下客户端**在读未结束时断开连接**正好落在该窗口内，
所以这**不是只在测试里才存在的形态**。目前尚未观察到真实故障（panic 或内存异常增长），
也没有把它当作"仅测试问题"掩盖。

**怎么办**：不 fork、不 `replace`、不 `t.Skip`、不进 CI 白名单——`-race` 门禁保持原样，
以本条作为识别依据，避免下次红灯被误判成新缺陷：

- 症状：`make verify` 的 `test-race` 阶段失败，日志含 `WARNING: DATA RACE`，且栈里只出现
  `github.com/sagernet/...`；
- 判定：即本条。**不要**据此重启一轮排查，也**不要**据此判断该次改动有问题；
- 若复现成功，记录机器与负载情况，而不是改测试回避（改测试会掩盖真实流量下的同一窗口）。

**何时重新评估**：① `sagernet/sing` 修好 `CachedConn` 的 `c.buffer` 同步后升级；
② 真实流量下出现与该字段相关的 panic 或内存异常；③ `-race` 命中频率明显上升。

**相关记录**：`docs/PROTOCOLS.md` §10。
