# 引擎决策记录

本文件记录影响运行时内核的决策及其依据。改动这些决策前请先读完对应条目。

## D-1 内核：只用 sing-box，不引入 mihomo

**决策**：Prism 以 sing-box 为唯一运行时内核。原方案 WP07 规划的 mihomo 兜底内核**不引入**。

**日期**：2026-09-24。

**背景**：原方案曾计划用 `github.com/metacubex/mihomo@v1.19.31` 兜底 sing-box 无法表示的
Clash 系协议（SSR、Mieru、VLESS XHTTP/encryption、Snell v1–v3、AmneziaWG、
kcptun/restls/gost-plugin 等），代码放在 `with_mihomo` 构建标签下。

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
- 构建标签只有一套 `TAGS`，且不含 `with_mihomo`：`Makefile`、`.github/workflows/release.yml` 的
  `TAGS`、根目录 `Dockerfile` 的 `ARG TAGS` 三处一致（发布镜像由 `.github/Dockerfile.release`
  复制 `release.yml` 产出的二进制，因此继承同一套标签）。发布产物只有一个变体。
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
属于订阅端到端测试的范围）。`up: "abc"` 这类语义无效但类型正确的
值不会被 `-t` 拦住——Prism 只在输入确实是数字时生成 `up`/`down`，所以不会产生这种值。

**何时重新评估**：mihomo 更改 proxy 类型名或字段名时；或 Prism 新增需要导出的协议时，重跑同一套校验。

## D-5 sing 上游数据竞争（`common/bufio/cache.go` 的 `CachedConn`）：上游尚未修复，**已可命中生产路径**

**状态**：**记录中，未关闭**。竞争发生在依赖库，不是本仓库的代码；**没有 fork、没有本地补丁、没有 `replace`**
（延续 D-1/D-2 的依赖策略）。CI 的 `make test-race` 会**概率性**红灯，识别方法见下。

**记录期间**：2026-09-27。**首次观测**：commit `b506888` 的 CI run `36326553534`。
**复核日期**：2026-09-28（上游状态、竞争创建点、方法 A 原型、上游补丁，均为本轮）。

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

**上游状态（2026-09-28 复核）**：`sagernet/sing` 的 `dev`（默认分支）与 `main` 上
`CachedConn.Close()` / `Read()` 仍是同一写法，**未修复**。注意：`master` 分支**不存在**
（2026-10-02 那条记录写的 "dev/main/master 三处都查过" 不准确；仓库分支是 `dev`、`main`、
`stable` 和若干 `renovate/*`）。Prism 钉的是 `sing v0.9.6-0.20260922013354-87c33f17688f`，
由 `sing-box v1.14.2` 的 require 决定；`main` 上的 `common/bufio/cache.go` 与我们的 pin
**逐字节相同**（blob `94423887b2`），`dev` 只在 UDP 的 `CachedPacketConn` 上有 `9822d61a`
的重构，`CachedConn`/`CachedReader` 区域两边一致。
`common/bufio/cache.go` 最近一次改动仍是 `9822d61a`（author date 2026-09-10，committer
date 2026-09-27；上游 rebase 过，所以两个日期不同），已包含在我们的 pin 里。
**别把历史修复当成已修**：`b8eed517`（2026-01-17）的标题是 "Fix race between **ReadCached** and
Close" —— 它让 `ReadCached()` 用 `taken.CompareAndSwap` 与 `Close()` 互斥；而踩到我们的是
**`Read()` vs `Close()`**，`Read()` 完全不碰 `taken`，所以那次修复管不到。
**触发概率**：概率性，不是必现。本地在 `-race` 下把 shadowtls 用例连跑 **40 次**、整包连跑 **10 次**，
一次都没复现；CI 侧连续 6 次推送里命中 1 次（相邻提交 `8fce32a` 是 success）。
两个 goroutine 的窗口很窄，核数与负载不同，命中率就不同。

**影响面（2026-09-28 重写；此前两版都是错的，勿再沿用）**：`c.buffer` 的引用计数可能错乱
（重复 `DecRef`/`Release`，或漏 `Release`），窗口是"读与关闭并发"。

**第一版说"真实流量下也存在"，第二版说"生产路径不会创建 `CachedConn`"，两个都不对。**
正确答案：`CachedConn` 既有**入站**创建点，也有**出站（客户端）**创建点，而**出站那个 Prism 真的会走到**。
本轮用 Prism 自己的 builder 复现出来了（证据见下）。

*入站侧*（Prism 不创建）：`sing-box/route/route.go:148/164`（`RoutedConnection`，首包 `buffers`
非空时）、`sing/protocol/http`、`sing/protocol/socks`、`sing-shadowsocks/shadowaead_2022/relay.go:193`、
`sing-box/transport/*` 各服务端。

*出站侧*（**Prism 的 `adapter.Outbound` 会创建**）：

- `sing/protocol/http/client.go:138`——`(*http.Client).DialContext` 在响应后 `reader.Buffered() > 0`
  时 `conn = bufio.NewCachedConn(conn, buffer)`。sing-box 的 `http` outbound 就是用它
  （`sing-box/protocol/http/outbound.go:44` 的 `sHTTP.NewClient`），而 Prism 支持 `http`/`https` 节点
  （`internal/subscription/parser.go:608/980/1530/2321`，protocol matrix 的 `http-uri`/`https-uri` 用例）。
- `sing-box/transport/v2raywebsocket/client.go:117`——ws 升级后 `reader.Buffered() > 0` 时同上。
  这是 vmess/vless 的 `transport: ws` 走的路（`sing-box/transport/v2ray/transport.go:57`），
  Prism 的订阅解析会把 `net=ws` 保留下来（`internal/subscription/parser.go:950/1129/3151`）。
- `sing-box/transport/v2rayhttpupgrade/client.go:113`——httpupgrade 同理
  （`transport/v2ray/transport.go:64`），对应 `internal/subscription/parser.go:1161`。

（另有两个**服务端**创建点，Prism 不涉及：`sing-box/transport/trojan/mux.go:67` 与
`transport/trojan/service.go:121`。）

**生产可复现（2026-09-28，本机 WSL，`-race`）**：写一个 stub 代理，对 CONNECT 一次性回
`200 Connection Established` + 服务器先发字节（这正是"代理 + server-first 协议"的形态），
然后用 **Prism 自己的** `outbound.NewSingboxBuilderWithConfig` + `Build()` 建一个
`{"type":"http",...}` 节点去拨它，`DialContext` 返回的类型就是 `*bufio.CachedConn`；随后按
Prism 隧道泵的形态并发 `Read` + `Close`（`internal/proxy/tunnel.go:220` 的
`io.Copy(upstreamConn, ...)` 与 `:214` 的 `upstreamConn.Close()`），`-race` 报：

```
WARNING: DATA RACE
Read at ... by goroutine 14:
  sing/common/bufio.(*CachedConn).Read()   common/bufio/cache.go:40
Previous write at ... by goroutine 15:
  sing/common/bufio.(*CachedConn).Close()  common/bufio/cache.go:86
```

即**与 CI 在 e2e 上抓到的是同一个字段、同一对方法**，只是创建点换成了出站侧。

**第二次独立复现（2026-09-28，主 agent 重写一遍验证）**：不复用上面的 harness，另写一个最小程序，
同样用 `outbound.NewSingboxBuilderWithConfig` 建 `{"type":"http"}` 节点去拨一个对 CONNECT 一次性回
`200 Connection Established` + 服务器先发字节的 stub，然后在 `-race` 下并发 `Read`/`Close`，得到：

```
dialed; connection type = *bufio.CachedConn
WARNING: DATA RACE
Read at ... by goroutine 13:
  sing/common/bufio.(*CachedConn).Close()   common/bufio/cache.go:85
Previous write at ... by goroutine 12:
  sing/common/bufio.(*CachedConn).Read()    common/bufio/cache.go:47
  ... sing/protocol/http.(*Client).DialContext (client.go:70)
      → sing-box/protocol/http.(*Outbound).DialContext (outbound.go:60)
```

**行号与 CI 抓到的完全一致**（`Close` `:85` / `Read` `:47`）。两次复现的差异（`:40`/`:86` 对 `:47`/`:85`）
是方法内不同分支命中，同一字段、同一对方法。

**为什么 CI 那次出现在 shadowtls 用例、栈里是入站**：那是 e2e 在测试进程内起真实 sing-box 实例
当对端（`startPeerFixture`：`box.New` + `instance.Start`），实例的**入站**创建 `CachedConn`
（`sing-shadowtls/service.go:285`，v3 分支；`newVerifiedConn` 包住连接后交给 ss 入站），
`-race` 只报同进程竞争，所以先在那里暴露。**先暴露的位置不等于唯一的暴露位置。**

**已抓到的完整 CI 竞争栈**（run `36326553534`，job `108640324612`，commit `b506888`）：

- 写方：`(*CachedConn).Read()` `common/bufio/cache.go:47` ← `shadowaead.(*Reader).Read`
  `shadowaead/aead.go:142` ← `shadowaead.(*serverConn).Read` `shadowaead/service.go:150`
  ← `bufio.copyExtendedWithPool` ← `route.(*ConnectionManager).connectionCopy` `route/conn.go:274`
  ← `NewConnection.gowrap1` `route/conn.go:152`；
- 读方：`(*CachedConn).Close()` `common/bufio/cache.go:85` ← `shadowaead.(*serverConn).Close`
  ← `bufio/deadline.(*Conn).Close` ← `connectionCopy` `route/conn.go:286` ← `gowrap2`
  `route/conn.go:153`；
- 两侧 goroutine 都由 `ConnectionManager.NewConnection` 创建（`route/conn.go:152/153`），
  调用链经 `sing-shadowtls/service.go:285` → `sing-box/protocol/shadowtls/inbound.go:140`
  → `route.RouteConnectionEx` → `protocol/shadowsocks/inbound.go:111` → `shadowaead.Service.newConnection`
  → `adapter.legacyUpstreamHandlerWrapper.NewConnection` → `shadowsocks.(*Inbound).newConnection`
  `inbound.go:134` → `route.RouteConnection` `route.go:45`。

**方法 A（把对端 sing-box 挪到进程外）评估结论：不做。** 原型已实测可行（见下；产物留在会话
scratch 的 `d5-methodA-rejected/`：`peerserver-main.go` 与 `protocols_test.diff`，可完整重放），
但它解决不了问题：

1. **它挡不住这条竞争。** 出站侧的 `CachedConn` 是 **Prism 自己的 outbound** 建的，就在测试进程内，
   `-race` 照样看得见。把"对端实例"挪出进程只挪走了入站那一半。
2. **入站那一半也挪不走。** `include.Context` 被 `internal/outbound/singbox_runtime.go:16` 导入，
   而那是**生产代码**（`box.New` 要注册所有协议）。只要 e2e 用 `newBuilder`，协议注册包就在二进制里；
   实测把 `startPeerFixture` 改成 exec 一个 helper 后，测试二进制的依赖集**一个包都没少**
   （917 → 917，`sing-box/protocol/{shadowsocks,shadowtls,socks,http,trojan}`、`sing-box/route`、
   `sing-shadowtls`、`sing-shadowsocks/shadowaead`、`sing/common/bufio` 全部仍在）。要真挪走，
   得连 `newBuilder` 一起改成子进程，那就等于不再测 Prism 的 outbound 了——**削弱覆盖**。
3. 成本上也不划算：实测原型 15 个用例全过、`-race` 连跑 6 轮 ×5 次无 flake，但每轮 ~8s
   （原树 `-count=5` ~9s 含构建；稳定后单轮 7–8s vs 原树 2.5–5s），外加一次性
   `go build ./internal/e2e/peerserver`（暖缓存 ~1–3s，冷 4s），以及进程生命周期、READY 握手、
   `-tags` 透传（要从 `debug.ReadBuildInfo()` 的 `-tags` 读回来）这些复杂度。
   换不来任何收益，因为第 1、2 点决定了它不解决竞争。

**方法 B（配置手段）**：全部否决。换 cipher、换协议、去掉 ss-2022、去掉 shadowtls 都改变 e2e
要证明的东西（"每个协议能与真实 sing-box 服务端完成一次往返"），属于**削弱覆盖**，不是修问题。

**方法 C（最终决定）：不动测试，把上游补丁做到可直接提交，并如实记录这是生产可达的缺陷。**
`-race` 门禁保持原样；`CachedConn` 的修复属于上游。补丁已就绪（见文末），本轮在真实 sing 模块的
副本上用**确定性复现器**验证：未打补丁时 `-race` 必现（`cache.go:85` vs `:47`，与 CI 栈同源），
打上补丁后干净，且 `sing` 全模块测试通过。

**怎么办**：不 fork、不 `replace`、不 `t.Skip`、不进 CI 白名单——`-race` 门禁保持原样，
以本条作为识别依据，避免下次红灯被误判成新缺陷：

- 症状：`make verify` 的 `test-race` 阶段失败，日志含 `WARNING: DATA RACE`，且栈里只出现
  `github.com/sagernet/...`（`(*CachedConn).Read` / `(*CachedConn).Close`）；
- 判定：即本条。**不要**据此重启一轮排查，也**不要**据此判断该次改动有问题；
- 不要改测试回避：把对端实例挪出进程（方法 A）已实测**不解决**（理由见上），换协议/密码套件则是削弱覆盖。

**风险等级（2026-09-28 上调）**：此前按"生产不创建 `CachedConn`"记为纯测试脚手架问题，因此是
非阻塞项。该前提已被证伪——`http` 出站节点、以及 vmess/vless 的 `ws` / `httpupgrade` 传输
在"代理一次性回握手 + 首包"的形态下会在 Prism 进程内创建 `CachedConn`，并按 Prism 隧道泵的
并发读/关形态触发同一竞争。触发需要该形态（服务器先发字节），不是每条连接必中，但**这是生产
数据路径上的缺陷**，不再是脚手架属性。它可能导致 `buf.Buffer` 引用计数错乱
（重复 `Release` → 缓冲池复用已释放内存，或漏 `Release` → 泄漏）。
缓解只能来自上游修复；本轮已把补丁备好。

**何时重新评估**：① `sagernet/sing` 修好 `CachedConn` 的 `c.buffer` 同步后升级；
② 真实流量下出现与该字段相关的 panic 或内存异常；③ `-race` 命中频率明显上升；
④ 上游合入等价修复后，把本条降级为"已由上游修复，见 <commit>"。

**相关记录**：`docs/PROTOCOLS.md` §10。

**上游补丁（2026-09-28 已就绪，可直接提交；本轮未向上游提 PR）**：根因是 `CachedConn.buffer`
没有任何同步，而上游 2026-01-17 的 `b8eed517` 只让 `ReadCached()` 与 `Close()` 通过 `taken` 互斥，
**`Read()` 没被覆盖**。修法是让所有碰 `c.buffer` 的方法共用一把 `sync.Mutex`：

```go
type CachedConn struct {
	net.Conn
	taken  atomic.Bool
	access sync.Mutex // guards buffer
	buffer *buf.Buffer
}

func (c *CachedConn) Read(p []byte) (n int, err error) {
	c.access.Lock()
	if c.buffer != nil {
		n, err = c.buffer.Read(p)
		if err == nil {
			c.access.Unlock()
			return
		}
		c.buffer.DecRef()
		c.buffer.Release()
		c.buffer = nil
	}
	c.access.Unlock()
	return c.Conn.Read(p)
}

func (c *CachedConn) Close() error {
	if c.taken.CompareAndSwap(false, true) {
		c.discardBuffer()
	}
	return c.Conn.Close()
}

// discardBuffer releases the cached buffer and clears the field. The lock makes
// the hand-off atomic, so only one of Read, ReadCached and Close releases it.
func (c *CachedConn) discardBuffer() {
	c.access.Lock()
	defer c.access.Unlock()
	if buffer := c.buffer; buffer != nil {
		c.buffer = nil
		buffer.DecRef()
		buffer.Release()
	}
}
```

（上面就是补丁里的写法；`access` 这个名字沿用上游 `common/bufio/race.go` 里 `RaceWriter.access`
的既有命名。）

`ReadCached` 与 `Close` 同样先抢 `access` 再动 `c.buffer`（`taken` 保留，它保证只有一个消费者
取走缓存）。需要给该文件加 `sync` import。`Read` 是热路径，但只在 `buffer != nil` 的首包阶段有争用，
之后 `buffer` 恒为 nil，锁只在开头落到。`CachedReader` 与 `CachedPacketConn` 是同一写法的复制品，
已一起改（`dev` 的 `CachedPacketConn` 已被 `9822d61a` 重构，所以单独出了一份 dev 版补丁）。
`WriteTo` 不能持锁调用 `w.Write`（对端阻塞会把 `Close` 一起卡住），所以先在锁内拷一份字节再写。
`ReaderReplaceable()` 原先裸读 `c.buffer`，也一并放进锁内。

**补丁产物**（在会话 scratch，不在仓库里）：

- `d5-upstream-patch/0001-bufio-guard-cached-buffer.patch` —— 针对 `main` / 我们的 pin 的形态
  （`git am` 可直接用；上游 `main` 的 `cache.go` 与我们的 pin 逐字节相同）。
- `d5-upstream-patch/0001-bufio-guard-cached-buffer.dev.patch` —— 针对 `dev`（默认分支）的形态，
  差异只在 `9822d61a` 重构过的 `CachedPacketConn`。
- `d5-upstream-patch/cache.go.final` / `cache.dev.final.go` —— 两个分支打完补丁后的完整文件。
- `d5-upstream-patch/README.md` —— 说明、验证方法与复现步骤。

**补丁验证（2026-09-28，本机 WSL，go1.27.1）**：把真实 `sing` 模块拷到可写目录，加一个
确定性复现器（同一 `CachedConn` 上并发 `Read` + `Close`，500 轮）：

- 未打补丁：`-race` 必现，栈是 `cache.go:85`(Close) vs `cache.go:47`(Read) 与 `:86` vs `:46`
  —— **与 CI run `36326553534` 抓到的字段和行号一致**；
- 打上补丁（两个分支形态各一次）：`-race` 干净，`go test -race ./common/bufio/...` 通过，
  `sing` 全模块 `go test ./...` 通过。

**为什么不在我们这边 `replace`**：那是 D-1/D-2 的依赖策略（不 fork、不本地补丁），而且这个字段的同步
属于上游的设计范畴。我方边界内能做的"修"就是把定性、影响面与识别方法记准（本条），
并**把补丁做到可直接提交**——但对外提交（PR）需要主人点头，本轮没有做。
