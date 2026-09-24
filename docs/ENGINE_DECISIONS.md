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
