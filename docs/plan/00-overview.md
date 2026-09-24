# 00 · 总览：目标、已核实事实、架构决策与全局规则

> 每个工作包都依赖本文。执行 AI 必须先读完本文。

## 1. 现状（一句话）

Prism = 上游 Resin（`9b8ef8e`）+ 质量检测、定时轮换和新前端。当前仓库**无法构建和运行**，原因有三：

- `.gitignore` 误伤了 `cmd/prism/` 和 `internal/state/`；
- `go.mod` 依赖不一致；
- 测试全部被删除。

运行时内核**只有 sing-box**：导入时能识别 mihomo/Clash YAML 和 v2rayN 分享链接，但最终都转成 sing-box 配置运行。详见 `docs/FEATURE_AUDIT_2026-09-24.md`。

## 2. 本期目标

1. **可构建、可运行、可测试**：修复仓库、依赖、入口和持久化层，恢复全部测试和 CI。
2. **全量继承 Resin**：Resin 的全部行为和上游测试在 Prism 中保持可用，与 Resin 的差异只允许出现在 §7 的偏差清单中。
3. **协议扩展**：
   - 以 sing-box 为唯一主内核。WireGuard 改为 endpoint；新增 OpenVPN 和 OpenConnect；支持 ShadowTLS 等 detour 串接；支持 Snell v4/v6；补齐缺失的分享链接格式。
   - 只有 **sing-box 无法支持的 Clash 系协议** 才交给 mihomo 兜底，例如 SSR、Mieru、VLESS XHTTP 或加密、Snell v1–v3 等。兜底按节点自动判定，不提供内核偏好配置。
4. **批量 IP 信息与纯净度**：导入后自动（也可手动）批量检测，范围包括：
   - 出口 IPv4/IPv6；
   - 离线 ASN 和城市；
   - 在线信誉数据源；
   - 经节点自身出口的检测（含 IPPure）；
   - 流媒体和 AI 服务解锁；
   - 综合纯净度评分。

   **所有结果写入 SQLite（intel.db）**，重启不丢，任务可以续跑。
5. **导出**：导出为 sing-box、mihomo（Clash Meta）、v2rayN（base64 分享链接）、纯分享链接、CSV 和 JSON；可按条件生成带令牌的订阅链接。

## 3. 已核实的技术事实（2026-09-24 实测，不要推翻）

| 编号 | 事实 | 证据 |
|---|---|---|
| F1 | `.gitignore` 中未锚定的 `prism`、`state/`、`*_test.go` 规则忽略了 `cmd/prism/`、`internal/state/` 和全部测试；这两个目录从未进入 git 历史 | `git check-ignore -v`、`git log --all -- cmd internal/state` |
| F2 | 执行 `go mod edit -require=github.com/sagernet/gvisor@v0.0.0-20260727.0-sing-box-mod.1`、`-dropreplace=github.com/sagernet/wireguard-go`、`-require=github.com/sagernet/wireguard-go@v0.0.5-0.20260823125007-8bd032a91a30` 后，全部构建标签下编译通过 | 在临时副本中编译 |
| F3 | sing-box v1.14.0 **已移除 WireGuard outbound**（只保留一个返回错误的桩），WG 只能以 **endpoint** 形式使用。endpoint 类型有：`wireguard`、`openvpn-client`、`openvpn-server`、`openconnect`、`tailscale`（分别需要 `with_wireguard`、`with_openvpn`、`with_openconnect`、`with_tailscale` 标签） | `include/registry.go` |
| F4 | `adapter.Endpoint` 实现了 `adapter.Outbound` 接口，可以直接 `DialContext` | `adapter/endpoint.go` |
| F5 | WireGuard endpoint 构造时依赖 ctx 中的 `adapter.NetworkManager`（而它又依赖 pause manager 等服务）。Prism 现有 builder 手工拼装的服务图缺少这些服务，实测直接空指针崩溃 | 实测 panic 于 `protocol/wireguard/endpoint.go:91` 和 `route/network.go` |
| F6 | **已验证可行的方案**：用 `box.New(box.Options{Context: include.Context(ctx), Options: option.Options{Log:&option.LogOptions{Disabled:true}}})` 创建实例并 `Start()`，之后在运行期调用 `inst.Endpoint().Create(...)` 和 `inst.Outbound().Create(...)` 动态创建节点，用 `Remove(tag)` 删除。实测结果：WG endpoint 创建成功；`shadowsocks{detour}` → `shadowtls` 串接能解析 detour 并真正发起握手；Snell v4 创建成功；Prism 自定义的 DoH 故障转移传输（`prism-sequential-failover`）可以写入 `option.DNSOptions.Servers`，`box.New` 与 `Start` 均成功 | 临时测试程序 |
| F7 | detour 在首次拨号时经 `OutboundManager.Outbound(tag)` 解析（找不到 outbound 时也会查 endpoint）。Prism 现在逐个单独创建节点，因此报 `outbound detour not found: <tag>`（已复现） | `common/dialer/detour.go` |
| F8 | OpenVPN 的类型名是 `openvpn-client`，需要 `with_openvpn` 标签。TLS 模式下必须提供 CA 证书或 `peer_fingerprint`，否则创建时报错（已验证）。sing-box **不自带 .ovpn 文件解析器**。`routes` 和 `redirect_gateway` 只影响 sing-box 内部的路由偏好；Prism 直接对 endpoint 调用 `DialContext` 时，流量一定走隧道 | `option/openvpn.go` 和文档 |
| F9 | sing-box 1.14 的 Snell 出站**只支持 version 4 和 6** | `option/snell.go` |
| F10 | Naive 需要 `with_naive_outbound`，Linux amd64/arm64 上还需要 `with_purego` 并在运行时提供 `libcronet.so`。Tor 需要外部的 `tor` 可执行文件。两者都**不在本期默认范围** | sing-box 构建文档 |
| F11 | mihomo `v1.19.31`（模块 `github.com/metacubex/mihomo`，GPL-3.0）可以和 sing-box 1.14 编进同一个 Go 模块：它依赖的是 `metacubex/*` 分叉库，模块路径不冲突。`adapter.ParseProxy(map)` 可在进程内使用，SSR、VLESS-XHTTP、Mieru、Snell v3、WireGuard 都能解析并发起拨号。strip 后的二进制从 48 MiB 增至 76 MiB | 临时测试程序 |
| F12 | mihomo v1.19.31 支持的类型：anytls、direct、dns、easytier、gost-relay、http、hysteria、hysteria2、masque、mieru、openvpn、reject、rematch、shadowquic、snell、socks5、ss、ssr、ssh、sudoku、tailscale、trojan、trusttunnel、tuic、vless（含 xhttp 传输和 encryption）、vmess、wireguard、zerotier。SS 插件支持 obfs、v2ray-plugin、gost-plugin、shadow-tls、restls、kcptun | `adapter/parser.go` |
| F13 | mihomo 的 `common/convert.ConvertsV2Ray([]byte)` 能把分享链接转成 mihomo 节点 map，支持：hysteria、hysteria2、tuic、trojan、vless（含 xhttp 和 encryption）、vmess、ss、ssr、socks、http、anytls、mierus | `common/convert/converter.go` |
| F14 | Resin 的令牌语义：环境变量必须定义但可以为空（为空即关闭认证）；弱令牌只在 `/api/v1/system/config/env` 中标记 `admin_token_weak` 和 `proxy_token_weak`，不拒绝启动 | 上游 `config/env.go`、`api/handler_system.go` |
| F15 | Resin 用单个端口（默认 2260）同时承载 UI、API、HTTP 正向代理、反向代理和 SOCKS5（`cmd/resin/inbound_demux.go`）；多接入点由 `endpoint_runtime.go` 管理 | 上游 `cmd/resin` |
| F16 | 从 `3633100` 恢复历史测试后，`subscription`、`quality`、`platform`、`routing`、`node`、`netutil`、`probe`、`topology`、`geoip` 均通过。`outbound` 的测试桩缺少 `ExchangeAsync`；`config` 和 `proxy` 的失败来自改名遗留和 `bf70428` 的行为变更 | 临时副本 |

## 4. 目标架构

```text
导入格式                         解析与判定                                   运行时
sing-box JSON ─┐
Clash/mihomo ──┤  subscription.Parse ─► NodeDoc + ParseReport ─► Build ─┬─► sing-box（内嵌 box.Box；outbound / endpoint / chain）
v2rayN 分享链接 ┤        （sing-box 优先；不支持时才用 mihomo）            └─► mihomo 兜底（构建标签 with_mihomo）
Surge / .ovpn ─┘                                                                  │
                                         GlobalNodePool ◄── adapter.Outbound ──────┘
                                               │
     ┌───────────────┬─────────────────────────┼───────────────────────┬──────────────────┐
  probe 出口探测    routing / proxy          intel（intel.db：任务、数据源、    export 导出
  （v4/v6/colo）    （Resin 原样）            经节点检测、解锁、评估）          （sing-box/mihomo/v2rayN/URI/CSV/JSON）
                                               │
                                 评估投影缓存（内存，可随时从 intel.db 重建）
                                               │
                                      platform 质量准入（fail-closed）
```

## 5. 关键设计决策

- **D1 内核**：sing-box 是唯一主内核。mihomo 只在节点满足 WP07 判定表（sing-box 无法表示）时使用，按节点自动判定，**没有任何内核偏好配置**。mihomo 代码放在构建标签 `with_mihomo` 下，完整版默认开启；不带该标签即得到精简版，此时这类节点在解析报告中标为 `ENGINE_NOT_BUILT`。**不引入 Xray，不做 sidecar 进程。**
- **D2 sing-box 运行时**：用内嵌的 `box.Box` 取代现在手工拼装的服务图（事实 F5、F6）。每个节点使用唯一 tag，动态 `Create`/`Remove`。
- **D3 节点文档（node RawOptions）**：
  - 普通的单个 sing-box outbound 保持原 JSON 不变，与 Resin 的哈希完全一致，旧数据零迁移；
  - endpoint、串接和 mihomo 节点使用信封格式 `{"prism_node":1,...}`，定义见 WP06 §1。
- **D4 统一接口**：Prism 其余模块只依赖 sing-box 的 `adapter.Outbound`。mihomo 节点包一层适配器。
- **D5 检测数据**：权威存储在 `intel.db`（SQLite）。内存里只保留可以从库中重建的投影缓存，因为路由热路径不能查库。
- **D6 批量**：持久化任务表（jobs 和 job_items，一个节点一个任务项），加上按数据源去重的队列（provider_queue）。每个数据源的预算、QPS 和暂停状态都持久化，重启后续跑。
- **D7 纯净度**：采用 Prism 自定义的多来源、可解释评分（WP10），不依赖单一来源。IPPure 作为"经节点"数据源，支持批量并持久化；默认限速，并在界面上提示用户自行确认条款。
- **D8 兼容**：默认保持 Resin 行为。偏差必须列入 §7，并提供开关或文档说明。
- **D9 简单优先**：遇到"更灵活"与"更简单"的取舍，选更简单的，除非方案明确要求。

## 6. 数据归属

| 文件 | 内容 | 持久性 |
|---|---|---|
| `$PRISM_STATE_DIR/state.db` | 系统配置、平台（含 `quality_policy` 和轮换设置）、订阅（含解析报告）、请求头规则、接入点、数据源设置（含 Key）、导出配置、审计日志 | 强一致，`synchronous=FULL` |
| `$PRISM_CACHE_DIR/cache.db` | 节点静态和动态信息、延迟、租约、订阅与节点的绑定（Resin 原样） | 可重建，`synchronous=NORMAL` |
| `$PRISM_STATE_DIR/intel.db` | 出口观测、离线和在线证据、经节点检测、解锁结果、评估、数据源预算和队列、任务 | 持久，`synchronous=NORMAL`（WAL） |
| `$PRISM_LOG_DIR/` | 请求日志分库（Resin 原样）、指标库 | 滚动 |
| `$PRISM_CACHE_DIR/geo/` | 离线库（country.mmdb、GeoLite2/DB-IP 等 mmdb） | 可重新下载 |

所有 SQLite 库：单写连接（`SetMaxOpenConns(1)`）、WAL、`busy_timeout=5000`，文件权限 0600，目录权限 0700。

## 7. 与 Resin 的有意偏差清单（唯一允许的差异）

| 编号 | 偏差 | 开关 |
|---|---|---|
| X1 | 默认要求令牌非空且长度至少 16 | `PRISM_ENFORCE_STRONG_TOKENS=false` 恢复 Resin 行为（只在 UI 中标记弱令牌） |
| X2 | 空令牌（关闭认证）需要显式允许 | `PRISM_ALLOW_EMPTY_PROXY_TOKEN=true`、`PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true`；并且此时监听地址必须是 loopback，除非另设 `PRISM_ALLOW_INSECURE_LISTEN=true` |
| X3 | 环境变量以 `PRISM_*` 为准，`RESIN_*` 作为兜底并打印弃用告警 | 无（总是兼容） |
| X4 | 同时接受 `X-Prism-Account` 和 `X-Resin-Account`；错误响应同时带 `X-Prism-Error` 和 `X-Resin-Error` | 无 |
| X5 | 平台 `quality_policy` 为空时行为与 Resin 完全相同；非空时启用质量准入 | 平台字段 |
| X6 | 新增的后台检测会消耗节点流量 | 订阅 `auto_intel=false` 或系统配置 `intel_enabled=false` |

## 8. 全局规则（每个 WP 都必须遵守）

- **R1**：不得删除、跳过或隔离测试。上游 Resin 测试必须通过；因偏差需要调整的断言，要写 `// PRISM-DEVIATION: X<n> <原因>` 注释。
- **R2**：单元测试不访问公网。外部 HTTP 一律用 `httptest` 加 `testdata/` 夹具；需要真实协议时，用进程内的 sing-box inbound 做本地服务端（WP13）。
- **R3**：`internal/proxy` 和 `internal/routing` 的请求路径不得访问数据库或外部网络，只读内存快照。
- **R4**：每个队列、缓存、工作池和输入都必须有上限，并有明确的超限行为（丢弃并计数、返回 429，或写入失败原因）。
- **R5**：构造函数收到非法参数属于编程错误，保持 panic（与上游一致），**不要返回 nil**。
- **R6**：不在日志、API 或错误信息中输出密钥、令牌或节点凭证。API 对密钥只返回 `has_key`。URL 错误信息沿用现有脱敏函数。
- **R7**：新 API 统一放在 `/api/v1` 下，需要管理员鉴权；错误格式沿用 `{"error":{"code":"...","message":"..."}}`，分页沿用现有 `limit`/`offset` 加 `WritePage`。
- **R8**：时间统一以 UTC Unix 纳秒存储，列名以 `*_ns` 结尾。时长在 API 中使用 Go duration 字符串（如 `"24h"`）。
- **R9**：锁定版本 sing-box `v1.14.0` 和 mihomo `v1.19.31`。升级任一内核都必须通过 `make protocol-matrix`。
- **R10**：前端新增文案必须同时提供中文和英文（`src/i18n/translations.ts`）。
- **R11**：方案没写到的细节，按优先级处理：先保持 Resin 行为；再选最简单的实现；在 WP 总结中说明。
- **R12**：每个 WP 结束必须运行 `make verify`（WP01 定义），并附上结果。

## 9. 统一验收命令（WP01 建立）

```sh
make web              # 前端 npm ci + build
make backend          # 完整版 bin/prism（含 with_mihomo）
make backend-lite     # 精简版（不含 mihomo）
make lint             # go vet + eslint
make test             # go test（完整版标签）
make test-race        # go test -race
make protocol-matrix  # 协议矩阵测试（WP06/07 建立）
make verify           # lint + test + test-race + protocol-matrix + 精简版编译测试
```

## 10. 术语

- **节点**：一个可以拨号的上游代理（sing-box outbound 或 endpoint，或 mihomo proxy）。
- **出口 IP**：通过节点访问 Cloudflare trace 时观测到的公网 IP。
- **证据（evidence）**：某个数据源对某个 IP 的一次标准化结论。
- **经节点检测**：请求经由被测节点发出，数据源看到的是该节点的出口 IP。
- **评估（assessment）**：Prism 按 WP10 规则，由多条证据计算出的结论。
