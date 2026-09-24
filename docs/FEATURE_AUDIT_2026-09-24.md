# Prism 深度审计报告：功能现状、缺口与建议

审计日期：2026-09-24
审计对象：`mycatxl/Prism` 分支 `claude/exciting-ptolemy-vg8fs4`，HEAD `bf70428`
对照基线：上游 Resin `9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`（即 `docs/UPSTREAM_BASELINE.md` 声明的基线，也是上游当前 HEAD）

审计目标对应用户的四项诉求：

1. 支持更多代理协议；
2. 导入节点后批量采集 IP 信息；
3. 批量测试节点纯净度；
4. Resin 原有功能全部保留。

---

## 0. 结论摘要

- **仓库目前无法构建，也无法运行。** 程序入口 `cmd/prism/` 和持久化层 `internal/state/` 被 `.gitignore` 规则误伤，从未进入过 git 历史。依赖树本身也不一致：在标准构建标签下，所有依赖 sing-box 的包都编译失败。本报告中的"已实现"指代码存在、逻辑可测，不代表能跑起来。
- **协议**（先修复依赖，再实测 40 余个样例）：SS、VMess、VLESS（含 Reality/Vision）、Trojan、Hysteria2、HTTP 和 SOCKS 可用。
  - **WireGuard 完全不可用**。sing-box 1.13 起移除了 WireGuard outbound，而 Prism 只会创建 outbound，相对 Resin 属于功能退化。
  - **ShadowTLS 等 detour 链节点**能构建成功，但一拨号就报 `outbound detour not found`。
  - Snell 已被内核支持，但被白名单过滤掉了。Naive 和 Tor 无法使用。
  - TUIC、Hysteria v1、AnyTLS、SSH 和 WireGuard 没有分享链接解析。
  - SIP002 带插件的 SS 链接解析失败。
- **批量 IP 信息采集**：只做了一半。
  - 做到的部分继承自 Resin：导入后每个节点自动探测出口 IP 和国家。
  - ASN、组织、IP 类型和风险完全依赖 ProxyCheck。匿名额度 80 次/天，免费 Key 900 次/天，代码把上限硬性锁在 900。
  - 没有离线 ASN 库，没有批量任务、进度和导出。
- **批量纯净度测试**：**未实现**。纯净度分只来自 IPPure，而 IPPure 被刻意设计为手动触发：全局每分钟 1 次，只存内存，最多 512 条，重启清空。评估逻辑又要求必须有 IPPure 结果才能给出"有效"结论，所以批量纯净度在设计上就走不通。
- **Resin 兼容**：管理 API 是 Resin 的超集，前端也覆盖了 Resin UI 的全部功能。以下几处破坏了兼容：
  - 入口组装（单端口多协议分流、多接入点运行时、token-action 接线）缺失；
  - `X-Resin-Account` 和 `RESIN_*` 没有兼容别名；
  - 提交 `bf70428` 引入了反代行为回归和前后端契约回归。
- **工程质量**：仓库里没有任何测试文件。97 个测试文件在提交 `2e40363` 被删除，`*_test.go` 又被加进了 `.gitignore`。从历史中恢复后，13 个包里有 9 个仍然通过。`SECURITY_AUDIT_COMPLETED.md` 中有多项声明与代码不符。

---

## 1. 审计方法

| 手段 | 内容 |
|---|---|
| 上游逐文件对比 | 先把 `github.com/Resinat/Resin`/`resin` 统一替换为 `prism`，再对全部 Go 文件做 diff |
| 分包构建 | 用 `Makefile` 中的标准标签 `with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy` 逐个编译 `internal/*` |
| 依赖修复验证 | 在临时副本中修正 `go.mod`，再用全部标签重新编译 |
| 协议矩阵实测 | 编写测试程序，对 40 余个分享链接、Clash 和 sing-box JSON 样例执行"解析 → 用 sing-box 实例化"，并对 detour 节点实际拨号 |
| 恢复历史测试 | 从 `3633100` 恢复 97 个被删测试文件，逐包运行 |
| 前端 | `npm ci` 加 `npm run build`（`tsc -b` 和 Vite）通过 |

---

## 2. 阻断级问题（P0，必须先修）

### P0-1 `.gitignore` 误伤入口、持久化层和测试

```text
.gitignore:2   prism        → 忽略了 cmd/prism/（以及任何名为 prism 的路径）
.gitignore:11  *_test.go    → 忽略了全部测试
.gitignore:17  state/       → 忽略了 internal/state/
.gitignore:33  state/       （重复）
```

`git check-ignore -v cmd/prism/main.go internal/state/engine.go` 的结果证实这两个路径都被忽略了。`git log --all -- cmd internal/state` 没有任何记录，说明它们从未提交过。

缺失的规模可以用上游对应部分估算：

- 上游 `cmd/resin` 共 2,520 行：`main.go` 976 行，`app_runtime.go` 601 行，`endpoint_runtime.go` 284 行，`inbound_demux.go` 480 行，`inbound_mux.go` 179 行。
- 上游 `internal/state` 约 1,884 行，另有 21 个 SQL 迁移文件。

Prism 在此基础上还需要：质量检测仓储（历史记录中提到的 `repo_quality.go` 和 state migration 10），以及平台 `quality_policy`、定时轮换字段所需的列。

**修复方法**：

1. 把规则改为锚定写法：`/prism`、`/bin/`、`/state/`，并删除 `*_test.go` 规则。
2. 如果原开发机（`SESSION_RECOVERY` 记录的 `/opt/Prism`）上还有这两个目录，执行 `git status --ignored` 确认后，用 `git add -f cmd/prism internal/state` 提交。
3. 如果已经丢失，就以上游 `cmd/resin`、`internal/state` 为骨架重建，再补上 Prism 新增的装配和迁移（清单见 §4.4 和 §4.5）。

### P0-2 依赖树不一致：启用 gvisor 或 wireguard 标签后编译失败

- `go.mod:137` 中的 `github.com/sagernet/gvisor v0.0.0-20250325023245-7a9c0f5725fb` 是 sing-box 1.12 时代留下的版本。
  - sing-box 1.14 需要的是 `v0.0.0-20260727.0-sing-box-mod.1`。
  - 按 semver 规则，数字预发布标识的优先级低于字母数字标识，所以 2025 年的旧版反而被判定为"更高"，MVS 选中了它。
  - 结果：`sing-tun` 报 `route.WritePacketDirect undefined`。
- `go.mod:201` 的 `replace github.com/sagernet/wireguard-go => ./third_party/wireguard-go` 指向旧 API（beta.7）。sing-box 1.14 需要新接口，编译报 `Send([][]byte, conn.Endpoint, int)` 签名不匹配。

**已验证的修复**：以下命令执行后，全部标签下除 state 相关包以外的所有包都能编译通过。

```sh
go mod edit -require=github.com/sagernet/gvisor@v0.0.0-20260727.0-sing-box-mod.1
go mod edit -dropreplace=github.com/sagernet/wireguard-go
go mod edit -require=github.com/sagernet/wireguard-go@v0.0.5-0.20260823125007-8bd032a91a30
go mod tidy          # 需要在 internal/state 恢复之后执行
rm -rf third_party/wireguard-go   # 同时更新 THIRD_PARTY_NOTICES 与 UPSTREAM_BASELINE
```

### P0-3 构建脚本路径错误

- `Makefile:16/29/33` 和 `scripts/verify.mjs` 使用了 `npm --prefix web`，但前端实际位于 `internal/api/web`。
- `internal/api/webui.go` 通过 `//go:embed web/dist` 嵌入前端，所以必须先构建前端才能 `go build`。
- 仓库里还有一个没人引用的 `internal/api/web/embed.go`（package `webui`），同样嵌入 `dist`，属于冗余代码。

### P0-4 部署脚本与实际配置不符，按 README 部署必然启动失败

- `scripts/deploy.sh:126` 写入的 `.env` 使用 `PORT`、`STATE_DIR`、`ADMIN_TOKEN`、`DNS_UPSTREAMS`，而程序读取的是 `PRISM_PORT`、`PRISM_STATE_DIR`、`PRISM_ADMIN_TOKEN`、`PRISM_NODE_DNS_UPSTREAMS`。
  - 同时缺少必填的 `PRISM_PROXY_TOKEN`，程序会直接报错退出。
  - 脚本会无条件覆盖已有的 `.env`，并把管理令牌打印到终端和 journal。
- 脚本使用二进制 `./prism`，而 `Makefile` 产出的是 `bin/prism`。
- systemd 单元设置了 `ProtectSystem=strict`，但 `ReadWritePaths` 只放行了 `STATE_DIR`；默认的 `./.local/cache` 和 `./.local/logs` 不可写。
- 端口默认值三处不一致：代码中 `PRISM_PORT=2260`（`internal/config/env.go:105`），`.env.example` 中是 1080，README 和部署脚本中是 8080。

---

## 3. 已实现功能

图例：
- ✅ 代码完整，历史单测通过；
- 🔌 代码存在，但装配位于缺失的 `cmd/`，或依赖缺失的 `state`，当前无法运行；
- ⚠️ 实现有缺陷，或只实现了一部分。

### 3.1 继承自 Resin 的能力（逻辑完整；在补齐 `cmd`、`state` 之前全部是 🔌）

| 模块 | 能力 |
|---|---|
| 订阅 | 远程 URL 和本地粘贴两种来源；定时刷新和手动刷新；启用/停用；临时订阅与节点驱逐延迟；增量保留存活节点；清理熔断节点 |
| 解析格式 | sing-box JSON 和出站数组；Clash JSON/YAML；Surge/QuantumultX 的 `[Proxy]` 段（含 WireGuard section）；URI 行（`vmess`、`vmess1`、`vless`、`trojan`、`ss`、`ssd`、`hysteria2`/`hy2`、`socks`、`socks5(h)`、`http(s)`、`tg`/`t.me`、`netch`）；`IP:PORT[:USER:PASS]`；Base64 包裹；单个订阅上限 32 MiB（Prism 新增） |
| 去重 | 按配置哈希跨订阅去重；一个节点可被多个订阅引用，共享健康状态 |
| 健康与出口 | 出口探测（Cloudflare trace，取 IP 和国家；新节点立即探测，默认 24 小时复探）；延迟探测（按域名 authority 维护延迟表并做衰减）；被动 TLS 握手延迟采样；连续失败熔断与主动恢复 |
| GeoIP | 国家级 mmdb（MetaCubeX），按 cron 自动更新，校验 SHA256；支持单个和批量查询 API |
| 平台 | 正则规则（普通为 ANY，`*` 为 MUST，`!` 为 MUST_NOT）；地区过滤；筛选预览；重建视图；重置为默认 |
| 调度 | P2C 结合按域名延迟加权；三种分配策略；粘性租约（TTL）；同 IP 节点故障切换；租约清理；IP 负载统计 |
| 接入 | HTTP 正向代理和 CONNECT；SOCKS5；URL 反代（含 WebSocket）；`Platform.Account:Token` 身份格式；请求头提取规则（含 URL 前缀规则）；miss action；固定账号头；bypass 直连 |
| 其他 | 接入点（Endpoint）CRUD；请求日志（SQLite 分库滚动，可捕获 payload）；实时、历史和快照指标；节点 DNS 走 DoH 并支持故障转移 |
| 管理 API | 上游全部路由原样保留（逐条对比确认），Prism 另增 6 个质量相关路由 |

### 3.2 Prism 新增的能力

| 能力 | 状态 | 说明 |
|---|---|---|
| IP 质量检测框架 | 🔌 | 持久化任务队列：按 IP、来源和 profile 去重，含 generation、租约、指数退避、每日预算和暂停。证据有效 24 小时，查询失败时保留旧证据。`Store` 接口的实现在缺失的 `state` 中 |
| ProxyCheck v3 | ✅/🔌 | 提供 IP 类型、ASN、组织、国家，代理、VPN、Tor、机房、被攻陷、爬虫、匿名等信号，以及风险分、运营商和攻击历史 |
| AbuseIPDB | ✅/🔌 | 近 30 天举报的置信度、举报次数和举报人数；需要免费 Key |
| Tor 公共名录 | ✅ | 离线拉取 Onionoo 全量 exit/guard/relay 名录，不上传任何库存 IP，没有配额限制 |
| IPPure 手动复核 | ⚠️ | 通过节点自身出口访问，得到纯净度、住宅和原生信息。每分钟全局只能查 1 次，结果只存内存，最多 512 条，有效 24 小时 |
| 综合评估 `Assess` | ⚠️ | 给出 favorable/caution/review/high_risk/conflicting/incomplete/pending 判定和原因码，不混合不同来源的分值。但结论必须依赖 IPPure（见 §4.3） |
| 节点质量筛选 | ✅/🔌 | 节点列表支持 `ip_type`、`quality_state`、`risk_grade`、`purity_band` 过滤；另有"出口记录"视图 |
| 定时轮换 | ⚠️ | `ScheduledRotator` 和平台 API 字段已实现，但从未被实例化，也没有对应的前端界面 |
| 平台质量准入 | ⚠️ | 只有运行时模型 `QualityPolicy`，API 无法设置（见 §4.3） |
| 安全加固 | ✅ | 常量时间比较令牌；令牌强度校验（至少 16 位，并用 zxcvbn 检查）；下载和订阅大小上限；请求日志脱敏（账号用 HMAC，URL 去掉 query 和 userinfo）；错误信息中的 URL 脱敏；跨源跳转时清除凭证 |
| 新前端工作台 | ✅ | React 19 加 TypeScript，中英双语，深浅色主题，适配移动端；覆盖 Resin UI 的全部功能，外加质量视图；`npm run build` 通过 |

---

## 4. 待实现、未完成和有缺陷的部分

### 4.1 诉求一：更多协议

以下结论来自协议实测矩阵：先修好依赖（P0-2），再用标准构建标签解析样例，并实例化为 sing-box outbound。

| 协议 | 分享链接 | Clash | sing-box JSON | 结论与原因 |
|---|---|---|---|---|
| SS（AEAD 和 2022） | ✅ | ✅ | ✅ | 可用。2022 密钥被百分号编码（`%3D`）时失败，因为解析时没有做 URL 解码 |
| SS 带 obfs 或 v2ray-plugin | ❌ | ✅ | ✅ | SIP002 标准格式 `ss://…@host:port/?plugin=` 失败：`parser.go:3446` 把尾部的 `/` 当作端口的一部分，导致端口解析失败 |
| SS 带 shadow-tls 或 restls 插件 | — | ❌ | — | 报 `plugin not found`，需要改写成 shadowtls 加 detour |
| VMess（ws/tls/grpc 等） | ✅ | ✅ | ✅ | 可用 |
| VLESS（tcp/ws/grpc/httpupgrade，TLS/Reality，Vision） | ✅ | ✅ | ✅ | 可用 |
| VLESS XHTTP | ❌ | ❌ | — | 内核不支持，节点被**静默丢弃** |
| VLESS `encryption=mlkem…` | ⚠️ | — | — | 能构建成功，但参数被静默忽略，运行时必然连接失败，应当拒绝并标注原因 |
| Trojan | ✅ | ✅ | ✅ | 可用 |
| Hysteria2（salamander 混淆、端口跳跃） | ✅ | ✅ | ✅ | 可用 |
| Hysteria v1、TUIC v5、AnyTLS、SSH | ❌ | ✅ | ✅ | 缺 `hysteria://`、`tuic://`、`anytls://`、`ssh://` 链接解析 |
| HTTP、HTTPS、SOCKS4/4a/5 | ✅ | ✅ | ✅ | 可用 |
| **WireGuard** | ❌ | ❌ | ❌ | **不可用，相对 Resin 退化**。sing-box 1.13 起移除了 WG outbound，只保留一个返回错误的桩。解析器仍生成旧的 outbound 格式（`parser.go:1415`），`builder.go:144` 只走 `OutboundRegistry`，而 endpoint 格式同样会被拒绝 |
| ShadowTLS 等 detour 链 | — | ❌ | ⚠️ | 能构建成功，但拨号实测报 `outbound detour not found`。原因是每个节点被单独创建，detour 目标没有注册到 sing-box 的 outbound manager |
| Snell | — | ❌ | ❌ | sing-box 1.14 已支持，但被白名单过滤（`parser.go:20-36`），Surge 解析也会主动拒绝（`parser.go:362`）；Clash 中的节点被静默丢弃 |
| Naive | ❌ | — | ❌ | 构建标签缺少 `with_naive_outbound`（该标签基于 cronet，二进制体积较大） |
| Tor | — | — | ❌ | 运行时需要 `tor` 可执行文件，否则报 `executable file not found` |
| SSR、Mieru、Restls、Juicity | ❌ | ❌ | — | sing-box 本身不支持；Clash 中的节点被静默丢弃 |
| OpenVPN、OpenConnect、Tailscale | — | — | — | sing-box 1.14 已支持（需要对应标签，并以 endpoint 方式创建），未接入 |

其他协议相关缺陷：

- **静默丢弃**：一份含 17 个节点的 Clash 配置只解析出 13 个，既没有报错，也没有提示。用户无法知道缺了哪些节点、为什么缺。
- **协议筛选缺陷**：`handler_node.go:123` 只允许 7 个值，而且 `socks5`、`https` 并不是 sing-box 的类型名（实际是 `socks`、`http`），所以这两个值永远匹配不到。`hysteria2`、`tuic`、`anytls`、`wireguard`、`ssh` 则无法筛选。
- Clash 的 `smux`、`udp-over-tcp`、`ech-opts` 字段被忽略。由于 Prism 只转发 TCP，影响较小，但应在解析报告中注明。

### 4.2 诉求二：导入后批量 IP 信息采集

| 能力 | 状态 | 说明 |
|---|---|---|
| 导入后自动探测出口 IP 和国家 | 🔌 | 逻辑继承自 Resin。需要 `pool.SetOnNodeAdded → probe.TriggerImmediateEgressProbe` 和 `probe.SetOnEgressObserved → inspection.Observe`，这两处接线原本都在缺失的 `cmd` 中 |
| 出口 IP 变化检测 | 🔌 | 默认每 24 小时复探（`max_egress_test_interval`） |
| ASN、组织、IP 类型 | ⚠️ | 只有 ProxyCheck 能提供：匿名 80 次/天，免费 Key 900 次/天。`config/quality.go:44` 把上限硬锁在 900，付费 Key 也无法提高。1000 个出口 IP 需要 2 到 13 天才能查完 |
| 离线 ASN、ISP 和城市信息 | ❌ | mmdb 读取器只读国家字段（`geoip.go:48`），没有 ASN 或 City 库 |
| 批量任务 | ❌ | 无法按订阅、平台、筛选条件或勾选范围一键批量检测；没有任务进度，也不能取消。接口和界面都只有单节点操作 |
| 导出 | ❌ | 无法导出 CSV、JSON，也无法生成订阅 |
| 双栈出口 | ❌ | 每个节点只记录一个出口 IP，不区分 IPv4 和 IPv6；trace 中的 `colo`（Cloudflare 接入点）也没有记录 |
| 证据到达后的联动 | ❌ | 质量证据写入后，不会触发平台视图重算（见 §4.3） |

### 4.3 诉求三：批量纯净度测试

**核心障碍是设计层面的。** `Assess` 规定：没有 IPPure 结果就无法得出"有效"结论（`assessment.go:64`，原因码 `IPPURE_REQUIRED`）。而 IPPure 出于其服务条款限制，被刻意设计为手动、限速、不持久化（`ippure.go:20-22`：每分钟全局 1 次，最多 512 条）。结果是：批量导入的节点绝大多数永远停留在 `pending` 或 `partial` 状态。

| 能力 | 状态 | 说明 |
|---|---|---|
| 纯净度分 | ⚠️ | 只能来自 IPPure 手动复核，无法批量 |
| 风险等级 | ⚠️ | 来自 ProxyCheck 风险分，受 §4.2 的配额限制 |
| 滥用举报 | ⚠️ | AbuseIPDB 需要 Key，900 次/天 |
| Tor 识别 | ✅ | 离线名录，无配额 |
| 原生 IP 与广播 IP | ⚠️ | 只有 IPPure 手动复核能提供 |
| 多来源评分框架 | ❌ | 数据源在代码中写死，没有插件化，也不能按来源配置额度和批量接口 |
| DNSBL 黑名单（Spamhaus、SpamCop 等） | ❌ | 未实现 |
| 流媒体和 AI 服务解锁测试 | ❌ | Netflix、Disney+、YouTube、ChatGPT、Claude、Gemini、TikTok 等均未实现 |
| 验证码和人机挑战率 | ❌ | Google、Cloudflare 均未实现 |
| 25 端口和邮件连通性 | ❌ | 未实现 |
| 平台按质量准入 | ⚠️ | 模型有，但无法使用，见下方说明 |
| 质量状态接口与前端 | ⚠️ | 自 `bf70428` 起字段缺失，见下方说明 |

**平台按质量准入**只有模型，实际无法使用：

- **API 设置不了**：`CreatePlatformRequest` 和 `PatchPlatform` 都没有 `quality_policy` 字段，返回体也没有；前端没有对应表单。
- `MinConfidence` 是空实现（`quality_policy.go:173` 注释写着 "placeholder"）。
- `ConflictAction` 的默认值实际是**放行**。注释说默认排除，但 `actionAllows("")` 返回 true。
- 没有分数时，`MinScore` 直接通过（`quality_policy.go:164`）。这是"失败即放行"，违背了设计文档中的 INV-08。
- 证据到期或年龄超限都不会触发重新评估。只有出口 IP 或地区变化时才重算，违背了 INV-03。

**质量状态接口与前端契约断裂**（`bf70428` 引入）：

- `/quality/status` 丢失了 `used_today`、`daily_limit`、`failed`、`paused`、`next_allowed_at`、`error_code`、`manual_sources`、`registry_sources`。
- `running` 被改名为 `in_flight`，但前端仍读取 `running`。
- `storage_error` 的类型被改成 `error`，并且适配器从不给它赋值。
- 结果：质量页的预算显示为 `undefined / undefined`，IPPure 的冷却时间不显示，存储错误被吞掉。

### 4.4 诉求四：保留 Resin 原有功能

| Resin 功能或行为 | Prism 现状 |
|---|---|
| 可构建、可运行的单一二进制 | ❌ 缺 `cmd`、`state`，依赖树不一致 |
| 状态持久化与重启恢复（节点、健康、延迟、租约、配置） | ❌ `internal/state` 缺失 |
| 单端口同时提供 UI、API、HTTP 代理、SOCKS5 和反代（`inbound_demux`） | ❌ 该逻辑位于缺失的 `cmd` |
| 在 WebUI 热添加监听端口（Endpoint 运行时） | ❌ API 在，运行时 `endpoint_runtime` 缺失 |
| 代理令牌命名空间的 token-action API | ❌ handler 在，接线缺失 |
| WireGuard 节点 | ❌ 退化，见 §4.1 |
| `X-Resin-Account` 请求头 | ❌ 被改名为 `X-Prism-Account`（`reverse.go:116`）且没有别名。现有 Resin 客户端会**静默丢失粘性会话** |
| `RESIN_*` 环境变量 | ❌ 没有兼容读取，迁移用户的 `.env` 会失效 |
| 空代理令牌（`RESIN_PROXY_TOKEN=""`，关闭代理认证） | ❌ Prism 强制要求至少 16 位。这是有意为之的安全取舍，但应提供仅限 loopback 的免认证选项 |
| 反代经节点访问内网或本机目标 | ❌ `bf70428` 新增的 SSRF 检查拦截了**经远端节点转发**的私网目标，却放行了**本机直连**的 bypass 路径。拦截方向正好反了（SSRF 风险只存在于本机直连路径），还导致上游 e2e 用例返回 400 |
| bypass 请求不建租约 | ⚠️ `bf70428` 让 bypass 请求也会创建租约，行为发生变化 |
| Docker 镜像、compose 和 Release CI | ❌ 没有 `Dockerfile`，也没有 `.github/workflows` |
| 读取 Resin 的 `state.db` 平滑迁移 | ❓ 需要等 state 恢复后才能确认 schema 兼容 |
| 管理 API 与 WebUI 功能覆盖 | ✅ API 是超集；前端覆盖了 Resin UI 的全部能力。`ip-load` 和 `preview-filter` 两个接口在 Resin UI 中同样没有入口 |

### 4.5 工程质量与文档

- **测试**：`2e40363` 删除了 97 个测试文件（连同设计文档共 33,346 行），`*_test.go` 又被加入 `.gitignore`。从历史中恢复后逐包运行：
  - 通过：`subscription`、`quality`、`platform`、`routing`、`node`、`netutil`、`probe`、`topology`、`geoip`。
  - `outbound`：测试桩缺少 sing-box 1.14 新增的 `ExchangeAsync`，编译失败。
  - `config`：令牌最小长度变更导致失败。
  - `proxy`：SSRF 行为变更导致失败，另有一项是改名遗留（`realm="Resin"`）。
  - `inspection`：依赖缺失的 `state`。
  - `service`、`api`、`metrics`、`requestlog`：依赖 `state`，无法运行。
- **`SECURITY_AUDIT_COMPLETED.md` 与代码不符**：
  - 声称的"认证限流"：`RateLimiter` 从未接入 `server.go`（`:178-179` 只有请求体大小限制和鉴权）。而且它信任客户端可伪造的 `X-Forwarded-For`，即使接入也能被轻易绕过。
  - 声称的"管理操作审计日志"：代码中不存在任何 `AuditLogger`。
  - 声称的"拒绝绝对路径"：`cleanDirPath` 没有实现。
  - 声称"全部测试通过"：仓库里没有测试。
  - "移除过时的 WireGuard 测试"：真正的问题是 WG 已经失效，这里用删测试掩盖了它。
- **`bf70428` 引入的反模式**：
  - `NewLatencyTable`、`NewGlobalNodePool`、`NewDirectDownloader` 在参数非法时由 panic 改成返回 nil，把编程错误推迟成之后的空指针崩溃。
  - 账号请求头被截断到 256 字节，前缀相同的两个账号会共用同一个租约。
- **文档失真**：
  - README 声称 MIT 许可，实际 `LICENSE` 是 GPL-3.0。
  - README 中的接口示例不存在：`POST /api/v1/nodes`、`POST /api/v1/quality/probe`、`/metrics/snapshots/summary`、`/ui/docs`。
  - README 的仓库地址写的是 `ermitcc/Prism`，Go 版本写的是 1.21（实际 1.26），目录结构中列出了不存在的 `internal/rotation` 和 `web/`。
  - `UPSTREAM_BASELINE.md` 写 sing-box 为 1.12.21，实际是 1.14.0。
  - `docs/scheduled-rotation.md` 的接口路径缺少 `/api` 前缀，也没有带鉴权头。
  - `DESIGN.md` 链接的多份文档已被删除。
- **冗余与死代码**：
  - `internal/api/web/static/*` 和 `templates/index.html` 是没人提供服务的旧界面。
  - 孤立的 `webui` embed 包。
  - `model.QualityPolicy` 与 `platform.QualityPolicy` 两套重复类型。
- **备份脚本**：`prism-backup.sh:121` 在服务运行时直接 tar SQLite（WAL 模式），可能得到不一致的备份；并且只备份了 state 目录。

---

## 5. 建议增加的功能与实施路线

### 阶段 0：恢复可构建、可运行（最高优先级）

1. 按 P0-1 修正 `.gitignore`，提交或重建 `cmd/prism` 与 `internal/state`（含质量表迁移）。
2. 按 P0-2 修正 `go.mod`，删除 `third_party/wireguard-go`。
3. 修正 `Makefile` 和 `verify.mjs` 中的前端路径；构建时先生成 `dist`；删除孤立的 embed 包和旧界面。
4. 恢复历史测试，并修复三类失败：改名遗留、sing-box 1.14 接口变化、SSRF 行为。
5. 撤回或修正 `bf70428` 的回归：恢复质量状态契约；把 SSRF 检查移到本机直连路径，并在 DNS 解析后按 IP 判断；恢复构造函数 panic；把限流器改为只信任可配置的反代来源后再接入。
6. 重写 `deploy.sh`（使用 `PRISM_*` 变量，调用 `prism init`，不覆盖已有 `.env`，systemd 可写路径完整）；新增 `Dockerfile`、compose 文件，以及 CI（`go vet`、`go test -race`、前端 lint 和 build）。
7. 把"协议矩阵"做成表驱动测试（`internal/outbound/protocol_matrix_test.go`），纳入 CI，防止 sing-box 升级时再次静默失效。

### 阶段 1：协议扩展

| 优先级 | 事项 |
|---|---|
| 高 | **WireGuard 改为 endpoint**：解析器输出 endpoint 选项，`Build` 通过 `EndpointRegistry` 创建（`adapter.Endpoint` 本身实现了 outbound 接口） |
| 高 | **支持 detour 链**：为每个节点建立一个小型 outbound 注册表，或让 `Build` 接收同一节点的多段配置，从而支持 ShadowTLS、Clash `shadow-tls` 插件改写和 `dialer-proxy` 前置代理 |
| 高 | **补全分享链接**：`tuic://`、`hysteria://`、`anytls://`、`ssh://`、`wireguard://`/`wg://`、`naive+https://`；修复 SIP002 插件格式和 ss2022 的 URL 解码 |
| 高 | **解析报告**：每次导入返回"成功、跳过、失败"清单及原因（例如"XHTTP 内核不支持"），不再静默丢弃；对 `encryption=mlkem` 等能构建但必然连不上的参数直接拒绝 |
| 中 | 接入 Snell（放开白名单，补全 Clash 和 Surge 转换） |
| 中 | 可选构建变体：`with_naive_outbound`，以及 `with_openvpn`、`with_openconnect`、`with_tailscale`（均走 endpoint）；Tor 需在文档中说明依赖，或使用 `with_embedded_tor` |
| 中 | 修正节点协议筛选：按 sing-box 实际类型名，列出全部支持的类型 |
| 可选 | **外部内核适配器**：以 sidecar 方式运行 mihomo 或 Xray，每个节点映射成一个本地 SOCKS 入口（或单端口按用户名路由），Prism 把它当 `socks` 节点管理。这样可以解锁 SSR、XHTTP、VLESS 加密、Mieru、Restls 等，代价是需要管理进程、端口和资源 |

### 阶段 2：批量 IP 信息采集

1. **离线补全（无配额）**：接入 ASN、城市级 mmdb，候选有 GeoLite2 ASN/City（需免费 License Key）、IPinfo Lite 和 DB-IP Lite，并支持自动更新。这样每个出口 IP 都能零成本得到 ASN、组织、城市和注册国家。"注册国家与地理国家不一致"可以作为"广播 IP 嫌疑"的离线参考信号。
2. **批量任务子系统**：
   - 新增 `jobs` 表，范围可以是订阅、平台、筛选条件或勾选的节点。
   - 流水线：出口探测 → 离线补全 → 按预算调用在线数据源 → 联动重算平台视图。
   - 支持进度推送（SSE）、取消和重试。
   - 优先级从高到低：手动触发、新导入、周期复检。
3. **导入后自动触发**：可以按订阅开关；订阅刷新时只检测新增节点和出口变化的节点。
4. **双栈出口**：分别探测 IPv4 和 IPv6 出口；记录 trace 中的 `colo` 和 RTT；保存出口 IP 变更历史。
5. **接口与界面**：`POST /api/v1/jobs`、`GET /api/v1/jobs/{id}`（SSE）、节点列表多选批量操作、导出 `GET /api/v1/nodes/export?format=csv|json`。
6. 允许付费 Key 突破 900 次/天的硬上限；对支持批量查询的来源使用批量接口。

### 阶段 3：批量纯净度测试

1. **可插拔数据源框架**：通过配置声明数据源、Key、每日预算、QPS 和是否支持批量；证据按来源独立保存（沿用现有 Evidence 模型）。候选来源如下，免费档额度和条款以官网为准，接入前需逐一核实是否允许自动化和存储：
   - 风险和代理识别：ProxyCheck（已有）、ipapi.is、IP2Location.io、IPQualityScore、Scamalytics、ipinfo（privacy 字段）；
   - 举报：AbuseIPDB（已有）；
   - 黑名单：DNSBL（Spamhaus ZEN、SpamCop、Barracuda 等，按其使用政策限速）；
   - 离线代理库：IP2Proxy LITE。
2. **经节点的实测项**（通过被测节点自身出口，并发和频率受控）：
   - 流媒体和 AI 服务可用性：Netflix、Disney+、YouTube Premium 地区、ChatGPT、Claude、Gemini、TikTok、Spotify；
   - 人机挑战：Google 搜索验证码、Cloudflare 挑战率；
   - 25 端口连通性。
   - 测试项以"请求 + 响应匹配规则"的形式配置，便于随目标站点变化更新。
3. **可解释的综合纯净度**：
   - 不再强制依赖 IPPure，由多个来源的信号按公开、可配置的权重计算，同时输出置信度（参与来源数量、来源间一致性）和各项扣分明细。
   - 延续"不伪造、未知就是未知"的原则。
   - IPPure 保留为可选的手动复核来源。
4. **平台质量准入落地**：
   - API 和前端补上 `quality_policy`；
   - 默认失败即拒绝（未知、冲突时排除），实现 `MinConfidence`，没有分数时不通过 `MinScore`；
   - 证据写入、到期、超龄时通过定时器或事件触发 `NotifyDirty`，重新评估。
5. **报表**：按 IP 类型、国家、ASN、分数段和解锁项统计分布；支持筛选后导出"干净节点"。

### 阶段 4：Resin 兼容与运维增强

- 兼容别名：同时接受 `X-Resin-Account` 和 `X-Prism-Account`；读取 `RESIN_*` 作为后备并打印弃用警告；提供 Resin `state.db` 导入工具。
- 可选的 loopback 免认证代理模式（等价于 Resin 的空令牌，但只允许本机访问）。
- 恢复单端口多协议分流和多接入点运行时；提供 OpenAPI 文档（落实 README 中提到的 `/ui/docs`）。
- 备份改用 SQLite 在线备份 API（或 `VACUUM INTO`），覆盖 state、cache 和配置。

### 其他建议新增功能

- **"导入 → 检测 → 导出干净节点"闭环**：按平台或筛选条件生成 Clash、sing-box、v2ray 订阅链接（带令牌）。
- 节点手动标签、备注和分组；按 IP 信息模板自动重命名（例如 `US-Residential-01`）。
- **强制换 IP**：定时轮换和手动轮换时排除之前的出口 IP。目前只是删除租约，下一次请求可能仍然分到同一个 IP。另外应补上定时轮换的前端界面。
- 告警：通过 Webhook 或 Telegram 通知节点池健康率下降、订阅刷新失败、数据源额度耗尽。
- Prometheus `/metrics` 导出。

---

## 附录 A：复现命令

```sh
# 1. 确认被忽略的关键路径
git check-ignore -v cmd/prism/main.go internal/state/engine.go

# 2. 分包构建（修复前会失败）
go build -tags 'with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy' ./internal/outbound

# 3. 从历史恢复被删除的测试（在临时副本中执行）
for f in $(git ls-tree -r 3633100 --name-only | grep '_test.go$'); do
  mkdir -p "$(dirname "$f")"; git show 3633100:"$f" > "$f"
done
```

## 附录 B：与上游 Resin 的差异规模

先统一改名，再逐文件 diff：

- 仅上游存在：`cmd/resin/*`（5 个文件）和 `internal/state/*`（10 个文件及迁移）。
- 仅 Prism 存在：质量、检测、定时轮换、限流器和日志脱敏相关的 17 个文件。
- `internal/subscription/parser.go` 与上游只差 5 行。**协议支持范围与 Resin 完全相同**，而 WireGuard 的失效来自 sing-box 升级。
- 改动最多的文件：`proxy/reverse.go`（124 行）、`api/webui.go`（102 行）、`service/control_plane_platform.go`（97 行）。
