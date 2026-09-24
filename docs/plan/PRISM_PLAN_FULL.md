# Prism v3 完整实施方案（由 docs/plan/ 各分册拼接生成，内容与分册一致）

> 使用说明见 docs/plan/README.md。分册有修改时请重新拼接：
> `cd docs/plan && (head -4 PRISM_PLAN_FULL.md; for f in 0*.md 1*.md; do echo; echo "---"; echo; cat "$f"; done) > /tmp/full && mv /tmp/full PRISM_PLAN_FULL.md`

---

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

---

# WP01 · 仓库与构建修复

**前置**：无。**目标**：修正 `.gitignore`、`go.mod`、前端嵌入方式、Makefile、CI 和 Docker，让仓库具备可构建的基础。完整构建要等 WP02、WP03 补齐 `internal/state` 和 `cmd/prism`。

## 1. `.gitignore` 全量替换

用下面的内容**完整替换**根目录 `.gitignore`（所有路径都锚定到仓库根，不再误伤 `cmd/prism/` 和 `internal/state/`，也不再忽略测试）：

```gitignore
# Build output
/bin/
/prism
/prism.exe
*.test
*.out
coverage.*
*.coverprofile

# Local runtime data (repo root only)
/.local/
/state/
/data/
/backups/
*.db
*.db-shm
*.db-wal
*.log

# Secrets
.env
.env.local
.admin_token

# Frontend
node_modules/
/internal/api/web/dist/*
!/internal/api/web/dist/.gitkeep

# Private notes
/docs/design/
/references/

# Editors / OS
.vscode/
.idea/
*.swp
*~
.DS_Store
Thumbs.db

# Go workspace
go.work
go.work.sum
```

同时把 `internal/api/web/.gitignore` 中的 `dist` 一行改为 `dist/*` 和 `!dist/.gitkeep` 两行。

**恢复本地文件**：如果原开发机上还存在 Prism 版本的 `cmd/prism` 或 `internal/state`（例如 `/opt/Prism`），先在那台机器上执行：

```sh
git status --ignored --short | grep -E 'cmd/prism|internal/state|_test\.go'
git add -f cmd/prism internal/state $(git ls-files --others --ignored --exclude-standard | grep '_test\.go$')
```

然后把这些文件带入本分支。WP02 和 WP03 会说明如何与本方案对齐。如果找不到这些文件，就按 WP02、WP03 从上游重建。

## 2. `go.mod` 修复（已验证，事实 F2）

```sh
go mod edit -require=github.com/sagernet/gvisor@v0.0.0-20260727.0-sing-box-mod.1
go mod edit -dropreplace=github.com/sagernet/wireguard-go
go mod edit -require=github.com/sagernet/wireguard-go@v0.0.5-0.20260823125007-8bd032a91a30
git rm -r third_party/wireguard-go
```

- 在 `go.mod` 的 gvisor 那一行上方加注释：`// pinned: sagernet pseudo-version "20260727.0-sing-box-mod.1" sorts lower than older hash-style pseudo-versions under semver; keep explicit`。
- 更新 `THIRD_PARTY_NOTICES.md`：删除 WireGuard 本地补丁一节；sing-box 版本改为 `v1.14.0`；新增 mihomo `v1.19.31`（GPL-3.0，WP07 引入）。
- 更新 `docs/UPSTREAM_BASELINE.md`：写明 sing-box 版本为 1.14.0、Go 版本为 1.26.0，列出构建标签清单，删除 wireguard 替换相关的说明。
- 等 WP02 恢复 `internal/state` 之后再执行 `go mod tidy`。在此之前不要执行，否则会因为缺包而删错依赖。

## 3. 前端嵌入（让 `go build` 和 `go test` 在未构建前端时也能通过）

1. 新建空文件 `internal/api/web/dist/.gitkeep` 并提交。
2. 删除孤立的 `internal/api/web/embed.go`（包 `webui`，没有被任何代码引用）。
3. 删除没有任何地方提供服务的旧界面：`internal/api/web/static/` 和 `internal/api/web/templates/`。
4. 在 `internal/api/webui.go` 中把嵌入指令改为 `//go:embed all:web/dist`，并把 handler 替换为上游 Resin 的实现，逻辑如下：
   - 只接受 GET 和 HEAD；
   - 对路径做 `path.Clean`；
   - 文件存在就用 `http.ServeFileFS` 返回；
   - 带扩展名但找不到的路径返回 404；
   - 其余路径回退到 `index.html`（SPA）；
   - 如果 `index.html` 不存在，返回 **503**，响应体为 `WebUI not built. Run: make web`；
   - `/` 重定向到 `/ui/`，`/ui` 重定向到 `/ui/`。
5. 验收：`rm -rf internal/api/web/dist/*`（保留 `.gitkeep`）之后，`go build ./internal/api` 能通过。

## 4. Makefile 全量替换

```make
GO ?= go
NPM ?= npm
WEB_DIR := internal/api/web
TAGS_BASE := with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy
TAGS_FULL := $(TAGS_BASE) with_mihomo
BUILD_TAGS ?= $(TAGS_FULL)
VERSION ?= dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X prism/internal/buildinfo.Version=$(VERSION) \
  -X prism/internal/buildinfo.GitCommit=$(GIT_COMMIT) \
  -X prism/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build web backend backend-lite test test-race lint protocol-matrix verify clean

build: web backend

web:
	$(NPM) --prefix $(WEB_DIR) ci
	$(NPM) --prefix $(WEB_DIR) run build

backend:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/prism ./cmd/prism

backend-lite:
	$(MAKE) backend BUILD_TAGS='$(TAGS_BASE)'

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

protocol-matrix:
	$(GO) test -tags '$(BUILD_TAGS)' -run 'TestProtocolMatrix' -count=1 -v ./internal/outbound/...

lint:
	$(GO) vet -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...
	$(NPM) --prefix $(WEB_DIR) run lint

verify: lint test test-race protocol-matrix
	$(GO) vet -tags '$(TAGS_BASE)' ./cmd/... ./internal/...
	$(GO) test -tags '$(TAGS_BASE)' ./internal/...

clean:
	rm -rf bin
```

说明：

- 构建标签 `with_mihomo` 由 Prism 自己定义（WP07）。在 WP07 完成之前，它不影响任何代码。
- 在 WP06/WP07 建立 `TestProtocolMatrix` 之前，`protocol-matrix` 目标会报 "no tests to run" 而不是失败，这是允许的。
- 删除 `scripts/verify.mjs`，以 Makefile 作为唯一的验证入口。
- `internal/api/web/package.json` 里的 e2e 脚本保持不变。

## 5. CI：`.github/workflows/ci.yml`

在 push 和 pull_request 时触发。Job 使用 `ubuntu-latest`：

1. `actions/checkout@v4`；
2. `actions/setup-node@v4`，node-version 22，并缓存 `internal/api/web/package-lock.json`；
3. `actions/setup-go@v5`，`go-version-file: go.mod`；
4. `make web`；
5. `make verify`；
6. `make backend` 和 `make backend-lite`（两个变体都必须能编译）。

## 6. Docker 与发布

- 以上游 `Dockerfile`、`docker/entrypoint.sh`、`docker-compose.yml.example` 和 `.github/workflows/release.yml` 为蓝本移植，替换规则如下：
  - `resin` 改为 `prism`；
  - `RESIN_` 改为 `PRISM_`；
  - 目录改为 `/var/lib/prism`、`/var/cache/prism`、`/var/log/prism`；
  - 暴露端口 2260；
  - 构建标签使用 `$(TAGS_FULL)`；
  - Go 版本使用 `go.mod` 中的版本。
- `release.yml` 的矩阵：linux amd64/arm64、darwin amd64/arm64、windows amd64。每个平台构建完整版，linux 额外构建 `-lite` 变体。**不要**加 `with_embedded_tor` 和 `with_naive_outbound`（事实 F10）。
- `.dockerignore` 至少包含：`.git`、`node_modules`、`bin`、`.local`、`internal/api/web/dist`。

## 7. 验收

```sh
git check-ignore cmd/prism/main.go internal/state/engine.go internal/node/hash_test.go; echo "exit=$?"  # 必须输出 exit=1（均未被忽略）
make web
go build -tags "with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy" \
  ./internal/outbound ./internal/subscription ./internal/proxy ./internal/probe ./internal/topology ./internal/routing ./internal/api/...
# internal/api 仍依赖 state，等 WP02 完成后才能通过；此处允许只报 "prism/internal/state" 缺失这一类错误
```

## 8. 不要做

- 不要重新引入 `third_party/wireguard-go`，也不要加任何 `replace`。
- 不要把 `with_embedded_tor`、`with_naive_outbound`、`with_tailscale` 加入默认标签。

---

# WP02 · 持久化层 `internal/state` 重建与迁移

**前置**：WP01。**目标**：恢复与上游 Resin 一致的持久化层（state.db 和 cache.db、脏集合回写、启动一致性修复、迁移），并加上 Prism 新增的表和列。完成后，所有依赖 `prism/internal/state` 的包都能编译。

## 1. 来源

- **情况 A**：WP01 §1 找回了 Prism 本地的 `internal/state`（带有 `repo_quality.go` 和 state migration 10）。以它为基础，把其中质量相关的表**删除**（新方案的检测数据全部放在 intel.db，见 WP08），再按本文第 3 节重新编号新增的迁移。编号必须连续，已经发布过的迁移不允许修改。
- **情况 B**（默认）：从上游 `github.com/Resinat/Resin@9b8ef8e` 复制以下内容：
  - `internal/state/*.go`，共 10 个非测试文件：consistency、dirtyset、engine、errors、flush、migrate、persistence_bootstrap、repo_cache、repo_state、schema；
  - `internal/state/migrations/**`，共 21 个 SQL 文件；
  - `internal/state/*_test.go`，共 10 个测试文件。

  复制后，把导入路径 `github.com/Resinat/Resin` 替换为 `prism`。注释和错误文案中的 Resin 可以改为 Prism，但**不要改任何 SQL 表名或列名**。

## 2. 模型改动（`internal/model/models.go`）

1. 删除 `platform.QualityPolicy` 这个重复类型。全项目只保留 `model.QualityPolicy`，由 `platform` 包引用。它的语义在 WP10 实现，本 WP 只负责持久化。

   ```go
   // QualityPolicy: empty value means "no quality admission" (Resin behaviour).
   type QualityPolicy struct {
       MinPurity        *int              `json:"min_purity,omitempty"`         // 0..100
       IPTypes          []string          `json:"ip_types,omitempty"`           // residential|mobile|business|wireless|datacenter|non_residential
       AllowedVerdicts  []string          `json:"allowed_verdicts,omitempty"`   // favorable|caution|review|high_risk|conflicting|incomplete
       MinConfidence    string            `json:"min_confidence,omitempty"`     // ""|low|medium|high
       RequireNative    bool              `json:"require_native,omitempty"`
       RequiredChecks   map[string]string `json:"required_checks,omitempty"`    // check_id -> outcome, e.g. {"chatgpt":"available"}
       MaxAssessmentAge string            `json:"max_assessment_age,omitempty"` // Go duration, "" = unlimited
       MaxEgressAge     string            `json:"max_egress_age,omitempty"`
       UnknownAction    string            `json:"unknown_action,omitempty"`     // ""(=exclude)|allow|exclude
       ExcludeTor       *bool             `json:"exclude_tor,omitempty"`        // nil = true
       ExcludeHighRisk  *bool             `json:"exclude_high_risk,omitempty"`  // nil = true
   }
   func (q QualityPolicy) IsEmpty() bool // 所有字段为零值时返回 true
   ```

   **旧键兼容**：解码时把 `min_score` 当作 `min_purity`，把 `max_assessment_age_seconds` 和 `max_egress_age_seconds` 转为 duration 字符串；`profile_id`、`pending_action`、`conflict_action` 直接忽略。编码时只输出新键。

2. `model.Platform` 保留已有的 `ScheduledRotationEnabled` 和 `ScheduledRotationIntervalNs`，另外新增：

   ```go
   QualityPolicy           QualityPolicy `json:"quality_policy"`
   RotationAvoidPreviousIP bool          `json:"rotation_avoid_previous_ip"`
   ```

3. `model.Subscription` 新增：

   ```go
   AutoIntel           bool   `json:"auto_intel"`             // 默认 true
   UserAgent           string `json:"user_agent"`             // 空 = 使用全局 UA
   LastParseReportJSON string `json:"-"`                      // 由 WP06 写入
   ```

4. 新增模型：

   ```go
   type IntelProviderSetting struct {
       ProviderID  string  `json:"provider_id"`
       Enabled     bool    `json:"enabled"`
       APIKey      string  `json:"-"`            // 绝不序列化
       DailyLimit  int     `json:"daily_limit"`  // 0 = 使用数据源默认值
       QPS         float64 `json:"qps"`          // 0 = 使用数据源默认值
       TTLNs       int64   `json:"ttl_ns"`       // 0 = 使用数据源默认值
       ConfigJSON  string  `json:"config_json"`  // 数据源私有配置（例如 DNSBL zones）
       UpdatedAtNs int64   `json:"updated_at_ns"`
   }
   type ExportProfile struct {
       ID, Name, Format, TokenSHA256, PlatformID, FilterJSON, NameTemplate string
       Enabled                                                          bool
       LastAccessAtNs, AccessCount, CreatedAtNs, UpdatedAtNs            int64
   }
   type AuditEntry struct {
       ID                                        int64
       AtNs                                      int64
       Actor, RemoteAddr, Action, Target, Detail string // Detail 为 JSON 文本
   }
   ```

## 3. 新增 state.db 迁移

编号从上游的 000009 往后接；情况 A 下按实际情况顺延。每个迁移都要提供 `.up.sql` 和 `.down.sql`。modernc sqlite 支持 `DROP COLUMN`，down 迁移直接删列即可。

`000010_platforms_quality_and_rotation.up.sql`

```sql
ALTER TABLE platforms ADD COLUMN quality_policy_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE platforms ADD COLUMN scheduled_rotation_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN scheduled_rotation_interval_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN rotation_avoid_previous_ip INTEGER NOT NULL DEFAULT 1;
```

`000011_subscriptions_import_options.up.sql`

```sql
ALTER TABLE subscriptions ADD COLUMN auto_intel INTEGER NOT NULL DEFAULT 1;
ALTER TABLE subscriptions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN last_parse_report_json TEXT NOT NULL DEFAULT '{}';
```

`000012_intel_provider_settings.up.sql`

```sql
CREATE TABLE IF NOT EXISTS intel_provider_settings (
    provider_id   TEXT PRIMARY KEY,
    enabled       INTEGER NOT NULL,
    api_key       TEXT NOT NULL DEFAULT '',
    daily_limit   INTEGER NOT NULL DEFAULT 0,
    qps           REAL NOT NULL DEFAULT 0,
    ttl_ns        INTEGER NOT NULL DEFAULT 0,
    config_json   TEXT NOT NULL DEFAULT '{}',
    updated_at_ns INTEGER NOT NULL
);
```

`000013_export_profiles.up.sql`

```sql
CREATE TABLE IF NOT EXISTS export_profiles (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    format            TEXT NOT NULL,
    token_sha256      TEXT NOT NULL UNIQUE,
    platform_id       TEXT NOT NULL DEFAULT '',
    filter_json       TEXT NOT NULL DEFAULT '{}',
    name_template     TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    last_access_at_ns INTEGER NOT NULL DEFAULT 0,
    access_count      INTEGER NOT NULL DEFAULT 0,
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);
```

`000014_audit_log.up.sql`

```sql
CREATE TABLE IF NOT EXISTS audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at_ns       INTEGER NOT NULL,
    actor       TEXT NOT NULL,
    remote_addr TEXT NOT NULL,
    action      TEXT NOT NULL,
    target      TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_audit_log_at ON audit_log(at_ns);
```

cache.db **不做任何修改**，保持与 Resin 完全一致。

## 4. 仓储（StateRepo / StateEngine）方法

- 平台和订阅的 CRUD 读写新列。`quality_policy_json` 用 `json.Marshal(model.QualityPolicy)` 序列化，读取时按 §2 兼容旧键。
- 新增以下方法。StateEngine 通过嵌入 StateRepo 暴露这些方法，写操作都在事务中执行：

  ```go
  SetSubscriptionParseReport(id string, reportJSON string) error          // 超过 64 KiB 截断（保留 JSON 合法）
  ListIntelProviderSettings() ([]model.IntelProviderSetting, error)
  UpsertIntelProviderSetting(s model.IntelProviderSetting) error
  ListExportProfiles() ([]model.ExportProfile, error)
  GetExportProfile(id string) (*model.ExportProfile, error)
  GetExportProfileByTokenSHA256(hash string) (*model.ExportProfile, error)
  UpsertExportProfile(p model.ExportProfile) error                        // name 冲突时返回 ErrConflict
  DeleteExportProfile(id string) error
  TouchExportProfileAccess(id string, atNs int64) error                   // access_count+1
  AppendAudit(e model.AuditEntry) error
  ListAudit(beforeID int64, limit int) ([]model.AuditEntry, error)        // 按 id 倒序
  PruneAudit(olderThanNs int64, keepMax int) (int64, error)               // 默认保留 90 天且最多 100000 条
  ```

- `PersistenceBootstrap(stateDir, cacheDir)` 保持上游签名不变。intel.db 由 WP08 的独立包负责打开和迁移，**不放在这里**。
- 文件权限：数据库文件 0600，目录 0700。state.db 设置 `synchronous=FULL`（与 Prism 此前的安全修复一致）。

## 5. 使用方的编译修复

- `internal/service`、`internal/metrics`、`internal/requestlog` 只引用了 `state.InitDB`、`OpenDB`、`ErrNotFound`、`ErrConflict` 和 `StateEngine`，恢复后即可编译。
- 执行 `go mod tidy`（此时依赖已完整）。

## 6. 测试

1. 上游 10 个 state 测试全部通过。
2. 新增 `migrate_prism_test.go`，验证三件事：
   - 先只执行上游 1–9 号迁移，并插入平台、订阅和接入点数据；
   - 再执行到最新版本；
   - 断言旧数据完好、新列取默认值（`auto_intel=1`、`rotation_avoid_previous_ip=1`、`quality_policy_json='{}'`）。
3. 为每个新表写 CRUD 测试。`IntelProviderSetting.APIKey` 必须能存取，并且 `json.Marshal` 的结果中不出现 Key。
4. `QualityPolicy` 编解码测试：覆盖旧键兼容、空策略 `IsEmpty()` 返回 true、往返（round-trip）一致。

## 7. 验收

```sh
go build -tags "with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy" ./internal/...
go test ./internal/state/... ./internal/model/...
```

`./internal/...` 必须全部能编译；`./cmd/...` 由 WP03 负责。

## 8. 不要做

- 不要修改上游 1–9 号迁移的内容或编号。
- 不要把检测数据放进 state.db 或 cache.db。

---

# WP03 · 程序入口 `cmd/prism` 与运行时组装（Resin 全量）

**前置**：WP02。**目标**：恢复可运行的二进制，行为与 Resin 一致：

- 单端口同时承载 UI、API、HTTP 正向代理、反向代理和 SOCKS5；
- 多接入点；
- token-action；
- 持久化恢复；
- 优雅关闭。

另外加上 Prism 的子命令和定时轮换。WP08 之前，旧的质量检测子系统先**停用**，由 WP08/WP09 用 intel 子系统替换。

## 1. 移植上游入口

1. 把上游 `cmd/resin/` 的 5 个源文件和 8 个测试文件复制到 `cmd/prism/`。
   - 源文件：main.go、app_runtime.go、endpoint_runtime.go、inbound_demux.go、inbound_mux.go。
   - 导入路径改为 `prism/...`。
   - 标识符 `resinApp` 改为 `prismApp`；日志和错误文案中的 Resin 改为 Prism。
2. `api.NewServer` 和 `NewServerWithAddress` 的签名与上游一致（已核对），可以直接对接。
3. `service.ControlPlaneService` 中 `Inspection`、`IPPure`、`TorRegistry` 这三个字段先保持 nil。现有 `/api/v1/quality/*` 接口在这种情况下返回 `409 {"error":{"code":"CONFLICT","message":"quality inspection is disabled"}}`（沿用 `inspectionError(quality.ErrDisabled)`）。前端已经能处理"未启用"状态。
4. 上游 `cmd` 的测试（main_test、inbound_mux_test、inbound_demux_test、endpoint_runtime_test、main_restart_integration_test、main_metrics_adapter_test、main_observability_config_test）必须全部通过。只有涉及偏差清单 X1–X4 的断言允许调整，并按 R1 加注释。

## 2. 子命令（`cmd/prism/main.go`）

| 命令 | 行为 |
|---|---|
| `prism` 或 `prism run` | 等同于上游 `run()`：先加载当前目录的 `.env`（godotenv，已存在的环境变量优先），再启动 |
| `prism init [--dir DIR] [--force]` | 生成 `DIR/.env`，权限 0600。内容包含 `PRISM_ADMIN_TOKEN`、`PRISM_PROXY_TOKEN`（各 32 字节随机数的 hex）、`PRISM_LISTEN_ADDRESS=127.0.0.1`、`PRISM_PORT=2260`，以及 state/cache/log 目录。文件已存在时必须加 `--force` 才覆盖；覆盖前先备份为 `.env.bak.<时间戳>`。只在 stdout 打印一次管理令牌，并提示妥善保存 |
| `prism version` | 输出 Version、GitCommit、BuildTime，以及构建标签（通过 `buildinfo.Tags` 在编译期写入，见 §6） |
| `prism check-config` | 加载并校验配置，打印生效配置（令牌和 Key 显示为 `***`）；校验失败时退出码为 1 |
| `prism backup --out DIR` | 在线备份：对 state.db、cache.db、intel.db（存在时）分别执行 `VACUUM INTO 'DIR/<name>.db'`，并写出 `manifest.json`（文件名、大小、sha256、时间、版本）。目录 0700，文件 0600。服务运行中也可以执行（使用独立的只读连接）。**不备份 `.env`** |
| `prism restore --from DIR [--force]` | 如果检测到服务正在运行（端口可连接，或 `PRISM_STATE_DIR/prism.pid` 对应的进程存在）就拒绝执行。先校验 manifest 的 sha256，再把现有文件改名为 `*.pre-restore-<时间戳>`，最后复制备份文件 |
| `prism import-resin` | 见 WP05 |

## 3. 监听布局

- **主监听**：`PRISM_LISTEN_ADDRESS:PRISM_PORT`，默认 `127.0.0.1:2260`，与 Resin 相同。它由 `inbound_demux` 同时承载 `/ui`、`/api`、`/healthz`、HTTP 正向代理与 CONNECT、反向代理、SOCKS5 和 token-action。
- **可选的独立管理监听**：`PRISM_ADMIN_LISTEN=host:port`，默认为空表示不启用。它只提供 `/ui`、`/api` 和 `/healthz`，不提供任何代理功能，适合把管理面单独绑定到内网地址。
- 多接入点（`/api/v1/endpoints`）的运行时由 `endpoint_runtime.go` 原样提供，并在重启时恢复。
- `internal/api/web/server/*.mjs`（Node 静态服务）**仅用于前端开发**。它使用的 `PRISM_UI_HOST` 和 `PRISM_UI_PORT` 只对该开发服务生效，要在 `internal/api/web/README.md` 中写清楚，避免与 Go 服务的配置混淆。

## 4. 组装顺序（写在 `app_runtime.go` 中，并在注释里列出编号）

1. 加载 `.env`，然后调用 `config.LoadEnvConfig()`（`RESIN_*` 兜底由 WP05 实现）。
2. 调用 `state.PersistenceBootstrap(stateDir, cacheDir)` 得到 engine。
3. 打开 intel store（WP08；在此之前跳过）。
4. 加载运行时配置 `loadRuntimeConfig(engine)`。
5. 创建 GeoIP 服务（上游实现；WP09 增加离线库）。
6. 创建节点构建器：先用现有的 `outbound.NewSingboxBuilderWithConfig`，WP06 替换为 `outbound.NewSingboxRuntime`。
7. 创建拓扑相关对象：`topology.NewGlobalNodePool`（`GeoLookup`；`QualityLookup` 暂为 nil，由 WP10 注入）、`SubscriptionManager`、`SubscriptionScheduler`、`EphemeralCleaner`。
8. 创建 `probe.NewProbeManager`，并调用 `SetOnProbeEvent`（指标）。WP08 会接上 `SetOnEgressObserved`。
9. 与上游一致：`pool.SetOnNodeAdded` 中调用 `outboundMgr.EnsureNodeOutbound(hash)` 和 `probeMgr.TriggerImmediateEgressProbe(hash)`；`SetOnNodeRemoved` 中调用 `RemoveNodeOutbound`、`markNodeRemovedDirty` 等。
10. 创建 `routing.NewRouter`、`routing.NewLeaseCleaner`，以及 **`routing.NewScheduledRotator(router, pool)`（Prism 新增，随 router 一起启动和停止）**。
11. 创建指标、请求日志和 `state.NewCacheFlushWorker`。
12. 创建 intel 任务执行器（WP08；在此之前跳过）。
13. 创建 `service.ControlPlaneService`（填入全部依赖）、`api.NewServer`、`api.NewTokenActionHandler`、入站复用器（mux/demux）和 endpoint runtime。
14. 启动后台服务，然后等待信号。

**关闭顺序**：监听服务 → 接入点 → 定时轮换 → 探测 → intel 执行器（WP08）→ 订阅调度 → 最后一次脏数据回写 → 关闭各数据库。每一步的超时都不超过 10 秒，整体不超过 30 秒。

## 5. 定时轮换接线

- 平台的 `scheduled_rotation_enabled`、`scheduled_rotation_interval` 和 `rotation_avoid_previous_ip` 已由 WP02 持久化。接口的读写在 WP10 完成，本 WP 只负责让轮换器运行。
- 轮换器每 7±3 秒扫描一次（现有实现），删除超龄的租约并触发 `LeaseRemove` 事件。

## 6. 构建信息

在 `internal/buildinfo` 中新增 `var Tags string`。Makefile 的 `LDFLAGS` 增加 `-X prism/internal/buildinfo.Tags=$(subst $(space),$(comma),$(BUILD_TAGS))`（需要在 Makefile 中定义 `space` 和 `comma` 两个变量）。`/api/v1/system/info` 返回 `build_tags`。

## 7. 测试

1. 上游 cmd 测试全部通过。
2. 新增 `cmd/prism/subcommands_test.go`，覆盖：
   - `init`：文件权限为 0600；不带 `--force` 时拒绝覆盖；带 `--force` 时生成备份文件。
   - `backup`：生成的 3 个库都能打开，`quick_check` 返回 `ok`，manifest 的 sha256 一致。
   - `restore`：检测到运行中时拒绝；带 `--force` 且未运行时成功。
   - `check-config`：输出中不包含令牌原文。
3. 冒烟脚本 `scripts/smoke.sh`（WP13 会复用）：
   - 用临时目录和随机令牌启动 `bin/prism`；
   - `curl` 检查：`/healthz` 返回 200；`/ui/` 在前端已构建时返回 200、未构建时返回 503；`/api/v1/system/info` 无令牌时返回 401、带令牌时返回 200；
   - 本地起一个 HTTP 目标服务器，加入一个本地订阅（`127.0.0.1:<port>` 纯 HTTP 代理行，指向本地测试 HTTP 代理）；
   - 分别通过 `-x http://`、`--proxy socks5h://` 和反代路径 `/<token>/./http/127.0.0.1:<port>/` 访问成功（反代到 127.0.0.1 这一项要等 WP04 修复 SSRF 回归后才会通过；在此之前脚本中把它标为 `SKIP(WP04)`）；
   - 重启进程后，平台和订阅依然存在。

## 8. 验收

```sh
make web && make backend && make backend-lite
go test -tags "$(TAGS_FULL)" ./cmd/...        # 或 make test
bash scripts/smoke.sh
```

## 9. 不要做

- 不要改变上游的鉴权顺序、错误码和 `X-*-Error` 语义；WP05 只会追加兼容头。
- 不要让 Node 开发服务成为运行所必需的组件。

---

# WP04 · 回归修复与安全整改

**前置**：WP03。**目标**：撤销或修正提交 `bf70428` 等引入的行为回归，把《安全审计完成》文档中声称但实际不存在的措施真正落地，或删除这些不实声明。整改后的原则：安全措施必须真实生效，并且不改变 Resin 的既有语义。

## 任务清单

### 4.1 反向代理 SSRF 检查回退（`internal/proxy/reverse.go`）

- 把 `isValidHost` 恢复为上游实现：只做语法校验，**不拦截私网或回环目标**。经远端节点转发时，私网目标访问的是节点那一侧的网络，不构成本机 SSRF。
- 删除 `isValidHostForBypass` 和 `isValidHostInternal`，也删除 bypass 分支中"尽力创建租约"的代码，恢复上游行为：命中 bypass 的请求**不创建租约**。
- 新增一个可选开关 `PRISM_DIRECT_DENY_PRIVATE=false`，默认关闭，保持 Resin 行为。开启后，**只对本机直连路径**（bypass）生效：先解析目标地址，如果属于回环、私网、链路本地地址，或 `169.254.169.254`、`fd00:ec2::254`，就返回 `403 DIRECT_TARGET_DENIED`。
- 验收：上游的 `TestReverseProxy_E2ESuccess` 和 `TestIsValidHost` 恢复通过。

### 4.2 构造函数恢复 panic（事实 R5）

`node.NewLatencyTable`、`topology.NewGlobalNodePool`、`netutil.NewDirectDownloader` 恢复为上游实现：参数非法时 panic，不返回 nil。

### 4.3 删除账号截断

删除 `internal/proxy/account_matcher.go` 中截断到 256 字节的逻辑，恢复上游行为（请求头大小已经由 `http.Server.MaxHeaderBytes` 限制）。

### 4.4 节点协议筛选（`internal/api/handler_node.go`）

- 删除硬编码的 7 值白名单。`protocol` 参数改为与 `NodeEntry.Protocol` 做精确匹配（小写）。
- `NodeEntry.Protocol` 的取值见 WP06 §1.4，例如 `vless`、`wireguard`、`openvpn-client`、`ssr`。
- 参数校验只保留"长度不超过 32，且只含 `[a-z0-9-]`"。
- 新增 `engine` 参数，取值为 `singbox` 或 `mihomo`，与 `NodeEntry.Engine` 匹配。该字段在 WP06 中新增，所以 **WP06 完成后**才补上这个参数，本 WP 只修正 `protocol`。

### 4.5 质量状态接口契约

WP03 已经停用旧的质量检测，WP08 会用 intel 子系统替换它。本 WP 只修正**字段形状**，不再恢复旧的检测逻辑：

- 删除 `service` 包中有损的 `Status`、`SourceStatus` 和 `inspectionAdapter`（`bf70428` 引入）。
- `/api/v1/quality/status` 返回的结构必须与前端 `QualityStatus` 类型完全一致：
  - `sources[]` 中每项包含：`id`、`name`、`website`、`configured`、`requires_key`、`has_key`、`daily_limit`、`used_today`、`queued`、`running`、`failed`、`paused`、`next_allowed_at`、`error_code`；
  - 另外包含 `manual_sources` 和 `registry_sources`；
  - `storage_error` 必须是字符串。
- 检测停用期间返回 `enabled:false`，各列表为空数组（不是 null）。
- WP08 §9 会用 intel 数据填充同一结构。
- 为这个结构写一个 JSON 快照测试，防止以后再次丢字段。

### 4.6 管理 API 登录失败限流（真正接入）

- 重写 `internal/api/rate_limiter.go`：
  - 键为客户端 IP，默认取 `RemoteAddr` 的主机部分；
  - **只有当** `RemoteAddr` 落在 `PRISM_TRUSTED_PROXIES`（CIDR 列表，默认为空）中时，才采用 `X-Forwarded-For` 的最后一个不可信地址。
- 只统计**鉴权失败**：每个 IP 每分钟最多 10 次失败，超过后在接下来 5 分钟内对该 IP 的管理 API 请求一律返回 `429 RATE_LIMITED`，并带 `Retry-After`。成功的请求不计数。
- 表容量上限 65536 个 IP，满了以后淘汰最旧的条目。
- 接入位置：`AuthMiddleware` 内部，在比较令牌之前先检查是否处于封禁期。
- 代理入口（407）默认不限流，保持 Resin 行为。可以通过 `PRISM_PROXY_AUTH_FAIL_LIMIT`（默认 0，表示关闭）开启同样的逻辑。

### 4.7 审计日志（真正实现）

- 对管理 API 的所有写操作（POST、PUT、PATCH、DELETE，以及 `/actions/*`）在成功后写入 `audit_log`（WP02 已建表）。记录内容：
  - actor：管理员令牌的 sha256 前 8 位；
  - remote_addr；
  - action：`METHOD` 加路由模式，例如 `PATCH /api/v1/platforms/{id}`；
  - target：路径参数；
  - detail：请求体的键名列表（**不记录取值**）。
- 新增 `GET /api/v1/audit-logs?before_id=&limit=`（limit 不超过 200）。
- 每天清理一次：保留 90 天，最多 10 万条。

### 4.8 目录校验（`internal/config/env.go`）

`cleanDirPath` 只拒绝**路径段恰好是 `..`** 的情况，允许绝对路径（Docker 需要，例如 `/var/lib/prism`）。错误文案改为 `PRISM_STATE_DIR: must not contain '..' segments`。

### 4.9 默认端口与示例配置统一

- 代码默认值：`PRISM_PORT=2260`、`PRISM_LISTEN_ADDRESS=127.0.0.1`。
- `.env.example` 和 `deploy/backend.env.example` 统一改为 2260，并写清楚 `PRISM_ADMIN_LISTEN`（可选）。
- `.env.example` 中给 Node 开发服务用的 `PRISM_UI_HOST` 和 `PRISM_UI_PORT` 移到 `internal/api/web/.env.example`。

### 4.10 部署脚本重写（`scripts/deploy.sh`）

- 前置检查：存在 `bin/prism`，否则提示先执行 `make build`。
- 如果 `.env` 不存在，调用 `bin/prism init --dir <部署目录>` 生成；**绝不覆盖已有的 `.env`**。
- 参数：`--listen`、`--port`、`--state-dir`、`--cache-dir`、`--log-dir`。只修改 `.env` 中对应的 `PRISM_*` 键：修改前先备份，并保留其余内容。
- systemd 单元：
  - `ExecStart=<部署目录>/bin/prism run`，`WorkingDirectory=<部署目录>`；
  - `ReadWritePaths=` 包含 state、cache、log 三个目录；
  - `NoNewPrivileges=true`、`ProtectSystem=strict`、`ProtectHome=read-only`、`PrivateTmp=true`；
  - 以专用用户 `prism` 运行，脚本负责创建该用户（`useradd --system`）。
- **不打印令牌，也不 kill 任何占用端口的进程**。端口被占用时直接报错退出。

### 4.11 备份脚本

`scripts/prism-backup.sh` 改为调用 `bin/prism backup --out <目录>/<时间戳>`，并按 `--keep N` 保留最近 N 份。restore 子命令调用 `bin/prism restore`。删除直接用 tar 打包运行中数据库的逻辑。

### 4.12 文档纠正

1. 删除 `SECURITY_AUDIT_COMPLETED.md`，改为新增 `docs/SECURITY.md`，**只描述已实现且已测试**的控制措施，每条都标注对应的测试名。
2. README 需要纠正的内容：
   - 许可证是 GPL-3.0-or-later，不是 MIT；
   - 仓库地址改为 `mycatxl/Prism`；
   - Go 版本为 1.26；
   - 默认端口为 2260；
   - 环境变量使用 `PRISM_*` 名称；
   - 删除不存在的接口（`POST /api/v1/nodes`、`POST /api/v1/quality/probe`、`/metrics/snapshots/summary`、`/ui/docs`）；
   - 目录结构以实际为准。
3. `docs/scheduled-rotation.md` 的接口路径改为 `/api/v1/platforms/{id}`，并加上 `Authorization` 头。
4. `docs/DESIGN.md` 中指向已删除文档的链接，改为指向 `docs/plan/`。

### 4.13 删除死代码

- `internal/service/control_plane_quality_adapter.go`（由 4.5 替代）。
- 如果 `internal/api/web/src/features/dashboard/DashboardPage.tsx` 和 `src/features/quality/QualityPage.tsx` 没有被路由引用，就删除，并同步清理只被它们引用的 i18n 键。

## 验收

```sh
make verify
bash scripts/smoke.sh            # 反代到 127.0.0.1 的检查不再标记为 SKIP
go test ./internal/proxy/ -run 'TestReverseProxy_E2ESuccess|TestIsValidHost' -count=1
go test ./internal/api/ -run 'TestAuthRateLimit|TestAuditLog' -count=1   # 本 WP 新增
```

新增测试要求：

- 限流测试：连续 11 次失败返回 429；伪造 XFF 不能绕过限流；来自可信代理的 XFF 生效；成功请求不计数。
- 审计测试：写操作会写入审计记录；读操作不会；detail 中不包含请求体的取值。
- `PRISM_DIRECT_DENY_PRIVATE` 测试：开启后 bypass 到 127.0.0.1 返回 403；关闭时放行。

---

# WP05 · Resin 兼容层与上游测试全量移植

**前置**：WP03（可以与 WP04 并行）。**目标**：Resin 用户可以直接换用 Prism，行为差异仅限偏差清单 X1–X4；上游 93 个测试文件全部移植并通过。

## 1. 环境变量兜底（偏差 X3）

- 在 `internal/config` 中新增 `lookupEnv(name string) (string, bool)`：先查 `PRISM_<X>`；不存在时再查 `RESIN_<X>`。命中 `RESIN_` 时记录一次弃用告警，格式为 `config: RESIN_<X> is deprecated, use PRISM_<X>`（同一个变量只告警一次）。
- `envStr`、`envInt`、`envDuration`、`envStringSlice`、`envDelimitedStringSlice` 以及令牌的读取全部改走 `lookupEnv`。
- 质量相关变量（`PRISM_QUALITY_*` 等）不需要 RESIN 兜底，因为 Resin 没有这些变量。
- 测试：只设置 `RESIN_PORT=3000` 时生效；`PRISM_PORT` 与 `RESIN_PORT` 同时存在时以 PRISM 为准；告警只出现一次。

## 2. 令牌语义（偏差 X1、X2）

- 与 Resin 相同：`PRISM_ADMIN_TOKEN` 和 `PRISM_PROXY_TOKEN` **必须定义**（可以通过 RESIN 兜底）。
- 空令牌表示关闭认证，但必须同时满足以下条件：
  - 显式设置了 `PRISM_ALLOW_EMPTY_PROXY_TOKEN=true`（或对应的 `PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true`）；
  - `PRISM_LISTEN_ADDRESS` 是 loopback（127.0.0.1、::1 或 localhost），**或者**另外设置了 `PRISM_ALLOW_INSECURE_LISTEN=true`。

  不满足时启动报错，错误信息要说明如何开启。
- 非空令牌：当 `PRISM_ENFORCE_STRONG_TOKENS=true`（默认）时，要求长度至少 16；设为 false 时恢复 Resin 行为，只在 `/api/v1/system/config/env` 中标记 `admin_token_weak` 和 `proxy_token_weak`。**zxcvbn 弱令牌判定始终只做标记，不拒绝启动**（与上游一致）。
- 代理令牌的保留字和禁止字符规则与上游一致（`api`、`healthz`、`ui`，以及 `ValidateProxyTokenForV1`）。

## 3. 请求头与响应头兼容（偏差 X4）

- 反向代理的账号优先级与上游一致：请求头 > URL 中的账号 > 请求头提取规则。请求头层同时接受 `X-Prism-Account` 和 `X-Resin-Account`；两者都存在时取 `X-Prism-Account`。
- 向上游转发前，这两个头都要剥离（同步修改 `reverse.go` 中的剥离列表）。
- 所有写 `X-Prism-Error` 的地方同时写 `X-Resin-Error`，取值相同。
- `Proxy-Authenticate` 的 realm 使用 `Prism`；上游测试中断言 `realm="Resin"` 的地方按 R1 调整并加注释。

## 4. 数据迁移：`prism import-resin`

```text
prism import-resin --from-state DIR --from-cache DIR [--from-log DIR] [--force]
```

1. 如果检测到 Prism 正在运行，拒绝执行（判断方式同 `prism restore`）。
2. 用 `VACUUM INTO` 把 Resin 的 `state.db` 和 `cache.db` 复制到 `PRISM_STATE_DIR` 和 `PRISM_CACHE_DIR`（请求日志库可选），然后执行 Prism 迁移（10–14 号）。
3. 目标文件已存在时必须加 `--force`，并先改名为 `*.pre-import-<时间戳>` 备份。
4. 输出摘要：平台数、订阅数、节点数、租约数、接入点数。
5. 上游 Docker 默认路径是 `/var/lib/resin` 和 `/var/cache/resin`，要在文档中给出示例。
6. 测试：用上游迁移创建一份 Resin 格式的数据库作为夹具，导入后由 Prism 启动；平台、订阅、租约齐全，节点哈希不变（普通 sing-box outbound 的 RawOptions 原样保留，事实 D3）。

## 5. 上游测试全量移植

1. 把上游全部 `*_test.go`（共 93 个，分布在 `cmd/resin` 和 `internal/*`）复制到对应目录。导入路径改为 `prism`；`RESIN_` 改为 `PRISM_`；`X-Resin-` 改为 `X-Prism-`，但**另外保留一组用 `X-Resin-*` 的用例来验证兼容**。
2. 再从本仓库提交 `3633100` 恢复 Prism 自己的测试：
   ```sh
   git show 3633100:<path>
   ```
   包括 quality、inspection（WP08 后改为 intel）、scheduled_rotator、platform quality、capacity 和 resource_safety 等测试。如果与上游同名文件冲突，以上游版本为基础，把 Prism 的用例追加进去。
3. 已知需要改的地方：
   - `outbound` 测试桩要实现 sing-box 1.14 的 `adapter.DNSTransport` 接口，需要补上 `ExchangeAsync` 和 `Reset`；
   - WireGuard 测试改为 endpoint 形式，在 WP06 完成。
4. 唯一允许调整的断言是偏差 X1–X4 相关的断言，必须加 `// PRISM-DEVIATION:` 注释。

## 6. Resin 功能对照清单（验收依据）

每一行都必须有测试覆盖，测试名写入 `docs/MIGRATION_FROM_RESIN.md`：

| Resin 能力 | 覆盖测试（来源） |
|---|---|
| 单端口：UI、API、HTTP 正向代理、反向代理、SOCKS5 | cmd `inbound_mux_test`、`inbound_demux_test` |
| 多接入点热添加和能力开关 | cmd `endpoint_runtime_test`、api `handler_endpoint_test` |
| `Platform.Account:Token` 身份、Default 平台 | proxy `identity_test`、`proxy_test` |
| 粘性租约、同 IP 切换、租约清理、IP 负载 | routing 全部测试 |
| P2C 加按域名延迟、三种分配策略 | routing `router_matrix_test` |
| 正则 ANY/MUST/MUST_NOT、地区过滤、筛选预览 | platform 测试、service `platform_preview` |
| 被动熔断、主动恢复、平台关闭熔断 | topology `health_test` |
| 订阅：远程和本地、增量存活、临时订阅与驱逐、熔断节点清理 | topology、service、api 的 e2e 测试 |
| 请求头提取规则（含 URL 前缀）、miss action、固定账号头 | proxy `account_matcher_test`、api 测试 |
| WebSocket 反代、bypass 直连 | proxy `e2e_test`、`bypass_test` |
| 请求日志（payload 捕获）、指标（实时、历史、快照） | requestlog、metrics、api `handler_metrics_test` |
| GeoIP 自动更新和查询 | geoip 测试 |
| 持久化与重启恢复 | state 测试、cmd `main_restart_integration_test` |
| token-action | api `handler_token_action_test`、cmd `inbound_mux_test` |

## 7. 验收

```sh
make verify
# 统计移植的测试文件数（应不少于 93 个上游文件，再加上 Prism 恢复的文件）
find cmd internal -name '*_test.go' | wc -l
```

---

# WP06 · sing-box 内核改造与协议扩展

**前置**：WP03。**目标**：

- 把 sing-box 运行时改为内嵌 `box.Box`（事实 F5、F6）；
- 支持 endpoint（WireGuard、OpenVPN、OpenConnect）和 detour 串接（ShadowTLS、`dialer-proxy`）；
- 接入 Snell v4；
- 补齐 tuic、hysteria、anytls、wireguard、ssh 分享链接，修复 SIP002 解析；
- 提供解析报告和能力接口；
- 建立协议矩阵测试。

mihomo 兜底在 WP07 实现。本 WP 只负责把"sing-box 无法表示"的节点标记出来，交给 WP07 或写入解析报告。

## 1. 节点文档格式（`RawOptions`）

### 1.1 两种形态

**形态 A：普通 sing-box outbound。** 保持原 JSON 不变，与 Resin 完全一致，哈希不变。

```json
{"type":"vless","tag":"jp-1","server":"1.2.3.4","server_port":443,"uuid":"...","tls":{...}}
```

**形态 B：信封格式。** 顶层含有 `"prism_node": 1` 的对象：

```json
{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"wg-jp","main":{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"...","peers":[{"address":"1.2.3.4","port":51820,"public_key":"...","allowed_ips":["0.0.0.0/0","::/0"]}]}}
{"prism_node":1,"engine":"singbox","kind":"chain","name":"ss-stls","main":{"type":"shadowsocks","method":"2022-blake3-aes-128-gcm","password":"...","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"1.2.3.4","server_port":443,"version":3,"password":"...","tls":{"enabled":true,"server_name":"cloud.tencent.com"}}]}
{"prism_node":1,"engine":"mihomo","kind":"proxy","name":"ssr-hk","proxy":{"type":"ssr","server":"...","port":443,"cipher":"aes-256-cfb","password":"...","obfs":"plain","protocol":"origin"}}
```

规则：

- `kind` 的取值：`endpoint`、`chain`（engine 为 singbox）；`proxy`（engine 为 mihomo）。单个 outbound **一律使用形态 A**，不允许用信封包装。
- `deps` 中每一项的 tag 必须依次为 `d0`、`d1`……`deps` 里的项可以是 outbound，也可以是 endpoint，按 §1.3 判定。`main` 和 `deps` 中的 `detour` 只能引用 `d*`。
- `name` 是显示名，**不参与哈希**。`main.tag` 也不参与哈希。

### 1.2 哈希规则（修改 `internal/node/hash.go`）

- 形态 A：保持现有逻辑（删除 `tag` 后对 JSON 做规范化）。
- 形态 B：删除顶层的 `name`、`main.tag` 和 `proxy.name` 后再规范化。`deps[i].tag` 固定为 `d<i>`，保留即可。
- 测试：
  - 形态 A 的哈希与上游实现逐字节一致（用上游的 hash 测试用例）；
  - 形态 B 中，仅改变 name 或 tag 时哈希不变，改变任意其他字段时哈希改变。

### 1.3 类型判定

- endpoint 类型集合：`wireguard`（仅在形态 B 中）、`openvpn-client`、`openconnect`、`tailscale`。
- 形态 A 中出现 `type=wireguard` 时，视为**旧版 WG outbound**，按 §3.1 转换为 endpoint 后再构建（这样旧数据的哈希不变，也能继续使用）。

### 1.4 `NodeEntry` 新增字段（`internal/node/entry.go`）

- `Engine string`：取值 `singbox` 或 `mihomo`。
- `Protocol`：取主类型，并统一为小写。对 mihomo 类型做归一化映射：`ss` → `shadowsocks`，`socks5` → `socks`，其余保持原样（例如 `ssr`、`mieru`、`vless`）。
- `Chain bool`。
- `ProtocolDetail string`：用于展示，例如 `shadowsocks+shadowtls`、`vless+xhttp`、`vless+reality`。

以上字段都在 `NewNodeEntry` 中通过解析 RawOptions 得到，无需持久化。

## 2. SingboxRuntime（替换 `internal/outbound/builder.go` 中手工拼装的服务图）

新建文件 `internal/outbound/singbox_runtime.go`，保留 `OutboundBuilder` 接口，也保留 `secure_dns.go`。

```go
type SingboxRuntime struct {
    ctx          context.Context
    inst         *box.Box
    logger       log.ContextLogger
    seq          atomic.Uint64
    endpoints    atomic.Int64
    maxEndpoints int64 // PRISM_MAX_ENDPOINTS，默认 256
}

func NewSingboxRuntime(cfg SingboxBuilderConfig) (*SingboxRuntime, error) {
    ctx := include.Context(context.Background())
    reg := service.FromContext[adapter.DNSTransportRegistry](ctx).(*dns.TransportRegistry)
    registerSecureDNSTransport(reg)
    specs, err := secureDNSTransportSpecsForUpstreams(cfg.DNSUpstreams)
    if err != nil { return nil, err }
    servers := make([]option.DNSServerOptions, 0, len(specs))
    for _, s := range specs {
        servers = append(servers, option.DNSServerOptions{Type: s.transportType, Tag: s.tag, Options: s.options})
    }
    inst, err := box.New(box.Options{Context: ctx, Options: option.Options{
        Log:   &option.LogOptions{Disabled: true},
        DNS:   &option.DNSOptions{RawDNSOptions: option.RawDNSOptions{Servers: servers, Final: secureDNSFailoverTransportTag}},
        Route: &option.RouteOptions{DefaultDomainResolver: &option.DomainResolveOptions{Server: secureDNSFailoverTransportTag}},
    }})
    if err != nil { return nil, err }
    if err := inst.Start(); err != nil { _ = inst.Close(); return nil, err }
    return &SingboxRuntime{ctx: ctx, inst: inst /* ... */}, nil
}
```

上面这段 DNS 配置已经实测通过（事实 F6）。

### `Build(raw json.RawMessage) (adapter.Outbound, error)` 的步骤

1. 解析形态 A 或 B。
2. 生成唯一 tag：`base := "n/" + hashHex[:16] + "/" + strconv.FormatUint(r.seq.Add(1), 10)`。
   - 必须带序号：`EnsureNodeOutbound` 可能并发构建同一个节点，而同名 tag 的 `Create` 会替换掉已有实例，导致落败方在 `Close` 时误删胜者。
3. 按类型分别处理：
   - **outbound**：`sJson.UnmarshalContext(r.ctx, raw, &opt)`（`opt` 类型为 `option.Outbound`），然后 `r.inst.Outbound().Create(r.ctx, r.inst.Router(), r.logger, base, opt.Type, opt.Options)`，最后用 `r.inst.Outbound().Outbound(base)` 取回实例。
   - **endpoint**：先检查 `endpoints < maxEndpoints`，超出时返回错误 `ENDPOINT_LIMIT: too many active endpoint nodes`。然后用 `option.Endpoint` 和 `r.inst.Endpoint().Create(...)` 创建，用 `r.inst.Outbound().Outbound(base)` 取回（该方法也会查 endpoint，事实 F7）。
   - **chain**：
     - 深拷贝 JSON，把所有值为 `d<k>` 的 `detour` 字段改写为 `base + "/d<k>"`；
     - 按顺序创建 `deps`，tag 为 `base+"/d<i>"`；
     - 最后创建 `main`，tag 为 `base`。
4. 任何一步失败，都要按逆序 `Remove` 已经创建的实例，再返回错误。
5. 返回句柄：

   ```go
   type singboxHandle struct {
       adapter.Outbound
       rt   *SingboxRuntime
       tags []string // 创建顺序
       isEP []bool
       once sync.Once
   }
   func (h *singboxHandle) Close() error // 按逆序调用 Outbound().Remove / Endpoint().Remove；endpoint 计数减一；幂等
   ```

   `OutboundManager.closeOutbound` 会通过 `io.Closer` 调用它。

### 其他要求

- `SingboxRuntime.Close()` 调用 `inst.Close()`。
- 删除 `builder.go` 中手工创建 endpoint、inbound、outbound manager 和 DNS 的代码。`NewSingboxBuilderWithConfig` 保留为调用 `NewSingboxRuntime` 的包装，以兼容现有调用方和测试。
- 保留原 builder 测试中的 DNS 行为断言，并迁移到新的运行时下验证。例如：域名形式的节点服务器地址经 DoH 解析；本地 bootstrap 生效。

## 3. WireGuard

### 3.1 统一转换为 endpoint

所有来源都输出形态 B，`kind` 为 `endpoint`。

| 来源字段（旧 sing-box outbound / Clash / Surge / URI） | endpoint 字段 |
|---|---|
| `local_address`，或 Clash 的 `ip`/`ipv6`，或 URI 的 `address=`（逗号分隔） | `address`：列表，自动补 `/32` 或 `/128` |
| `private_key`、`private-key`，或 URI 中 userinfo（需 URL 解码） | `private_key` |
| `server` + `server_port`，或 `server` + `port` | `peers[0].address` 和 `peers[0].port` |
| `peer_public_key`、`public-key`、`publickey=` | `peers[0].public_key` |
| `pre_shared_key`、`pre-shared-key`、`presharedkey=` | `peers[0].pre_shared_key` |
| `reserved`（`[1,2,3]`、`"1,2,3"` 或 base64 三字节） | `peers[0].reserved`（3 个 uint8） |
| `allowed-ips`，缺省时为 `0.0.0.0/0` 和 `::/0` | `peers[0].allowed_ips` |
| `persistent-keepalive` | `peers[0].persistent_keepalive_interval` |
| `mtu` | `mtu` |
| 旧格式中的 `peers[]` 或 Clash 的 `peers:` 列表 | 逐项映射到 `peers[]` |
| `system_interface`、`interface_name`、`gso` | 忽略（固定 `system=false`） |

- Clash 节点带有 `amnezia-wg-option` 时，sing-box 无法支持，交给 WP07（mihomo）处理。
- 分享链接：`wireguard://` 与 `wg://` 格式相同，都是 v2rayN 格式：
  ```text
  wireguard://<urlencoded private_key>@host:port?publickey=..&presharedkey=..&reserved=1,2,3&address=10.0.0.2/32,fd00::2/128&mtu=1280#name
  ```
- 测试：五种来源都能得到相同的 endpoint JSON；`Build` 成功；向不可达的对端拨号时返回超时错误，而不是 panic。

## 4. OpenVPN（`.ovpn` 转换为 `openvpn-client` endpoint，需要 `with_openvpn`）

### 4.1 输入识别

满足以下条件时，按 OpenVPN 配置文件解析：

- 内容不是 JSON，也不是 YAML；
- 存在形如 `^\s*remote\s+\S+` 的行；
- 同时存在 `<ca>` 内联块、`secret` 指令或 `<secret>` 块之一；
- 或者满足以下条件之一：存在 `^\s*client\s*$` 行、以 `dev tun` 开头的行、`<ca>` 块。

**批量导入**使用 Prism 捆绑格式（JSON）：

```json
{"prism_openvpn_bundle":1,"profiles":[{"name":"jp-1","ovpn":"<整份 .ovpn 文本>","username":"u","password":"p"}]}
```

### 4.2 指令映射

解析方式：逐行读取；`#` 和 `;` 开头为注释；`<tag>…</tag>` 为内联块；参数支持引号。

| .ovpn 指令 | openvpn-client 字段 |
|---|---|
| `remote host [port] [proto]`，可出现多次，也可以写在 `<connection>` 块中 | `servers[]`：`{server, server_port, network}`，默认端口 1194 |
| `proto udp/udp4/udp6/tcp/tcp-client/tcp4/tcp6` | `network`：`tcp-client` 映射为 `tcp` |
| `port N` | 作为未写端口的 remote 的默认端口 |
| `remote-random` | `remote_random: true` |
| `dev tun` / `dev tunN` | 允许（必须有）；**`dev tap*` 拒绝** |
| `<ca>` | `tls.certificate`（PEM 全文作为单个元素） |
| `<cert>`、`<key>` | `tls.client_certificate`、`tls.client_key` |
| `<tls-auth>` 加 `key-direction 0/1` | `tls.control_wrap = {type:"tls_auth", key:[PEM], direction: 0→"server", 1→"client"}` |
| `<tls-crypt>` / `<tls-crypt-v2>` | `tls.control_wrap.type = "tls_crypt"` / `"tls_crypt_v2"` |
| `<secret>` 或 `secret`（内联）加 `key-direction` | `mode:"static_key"`、`static_key`、`key_direction`；此模式下要求 `ifconfig a b`，映射为 `address:["a/32"]`、`peer_address:"b"` |
| `cipher X` / `data-ciphers A:B` / `data-ciphers-fallback X` / `auth X` | `cipher` / `data_ciphers:[A,B]` / `data_ciphers_fallback` / `auth` |
| `verify-x509-name NAME [name/name-prefix/subject]` | `tls.server_name` 和 `tls.server_name_type`（缺省为 `subject`） |
| `remote-cert-tls server` | `tls.remote_certificate_tls:"server"` |
| `tls-version-min V` / `tls-version-max V` / `tls-cipher X` | `tls.version_min` / `tls.version_max` / `tls.cipher` |
| `comp-lzo [x]` / `compress [alg]` / `allow-compression x` | `compression_lzo` / `compression` / `allow_compression` |
| `tun-mtu N` / `mssfix N` / `fragment N` | `mtu` / `mss_fix` / `fragment` |
| `keepalive a b`，或 `ping a` 加 `ping-restart b` | `ping_interval:"<a>s"`、`ping_restart:"<b>s"` |
| `reneg-sec N` | `renegotiate_interval:"<N>s"`（为 0 时设 `renegotiate_disabled:true`） |
| `pull-filter accept/ignore/reject "text"` | `pull_filters[]` |
| `route-nopull` | `route_no_pull:true` |
| `auth-user-pass` 加内联块 `<auth-user-pass>user\npass</auth-user-pass>`，或捆绑格式中的 username/password | `username` 和 `password` |
| `redirect-gateway`、`route`、`dhcp-option`、`block-outside-dns`、`nobind`、`persist-*`、`resolv-retry`、`verb`、`mute`、`script-security`、`up`、`down` | 忽略（写入解析报告的 `detail` 作为提示） |
| `pkcs12`、`http-proxy`、`socks-proxy`、`plugin`，或 `ca/cert/key/tls-auth/auth-user-pass` 后面跟**文件路径** | **拒绝**，原因写为 `UNSUPPORTED_FEATURE:<指令>`（文件路径类提示"请内联"） |
| 有 `auth-user-pass` 但没有提供凭证 | 拒绝，原因 `INVALID:credentials required` |

- 名称优先取 `profiles[].name`，其次取订阅名加第一个 remote 的主机名。
- 测试夹具放在 `internal/subscription/testdata/openvpn/*.ovpn`，至少覆盖：tls-crypt、tls-auth 加 key-direction 1、多个 remote 加 remote-random、tcp-client、static key、verify-x509-name、compress lz4-v2，以及三种拒绝场景（dev tap、外部文件、缺少凭证）。每个成功用例都要求 `Build` 成功（sing-box 会校验 CA，事实 F8）。

## 5. OpenConnect 与 sing-box `endpoints` 数组

- 解析 sing-box JSON 时，除 `outbounds` 外，还要读取顶层的 `endpoints` 数组。
- 其中类型为 `wireguard`、`openvpn-client`、`openconnect` 的项转换为形态 B 的 endpoint 节点。
- `tailscale`：当前构建不含 `with_tailscale`，写入解析报告，原因为 `ENGINE_NOT_BUILT:tailscale`。

## 6. detour 串接

1. **sing-box JSON**：某个 outbound 或 endpoint 的 `detour` 引用了同一文档中的另一个 tag 时，生成 chain 节点：
   - 递归收集依赖，最大深度 3，出现环时拒绝，原因为 `INVALID:detour cycle`；
   - 依赖按拓扑顺序重命名为 `d0…`，并改写所有引用；
   - 被引用的项如果本身也是可用的代理（即不是 `shadowtls`、`selector`、`urltest`、`direct`、`block`、`dns`），也会作为独立节点导入。
2. **Clash `ss` 加 `plugin: shadow-tls`**（plugin-opts 含 `host`、`password`、`version`）：
   ```text
   d0   = {"type":"shadowtls","tag":"d0","server":S,"server_port":P,"version":V,"password":PW,
           "tls":{"enabled":true,"server_name":host,"utls":{"enabled":true,"fingerprint":<client-fingerprint 或 "chrome">}}}
   main = {"type":"shadowsocks","method":M,"password":SSPW,"detour":"d0","server":S,"server_port":P}
   ```
   version 为 1 时不写 `password`。
3. **Surge `ss` 行中带 `shadow-tls-password`、`shadow-tls-sni`、`shadow-tls-version`**：按第 2 条处理。
4. **Clash `dialer-proxy: X`**，且 X 位于同一文件中：生成 chain，`d0` 为 X 转换后的 sing-box 对象。如果 X 无法用 sing-box 表示，写入报告，原因为 `UNSUPPORTED_FEATURE:dialer-proxy`。
5. 测试：chain 节点 `Build` 成功；向不可达地址拨号时，错误中**不能**包含 `outbound detour not found`。真实的端到端串接验证在 WP13。

## 7. Snell

- 在 `supportedOutboundTypes` 中加入 `snell`。
- Clash 和 Surge 的 snell 节点：`version` 为 4 时转换为：
  ```json
  {"type":"snell","version":4,"psk":..,"obfs_mode":<obfs-opts.mode 或 obfs>,"obfs_host":<obfs-opts.host 或 obfs-host>}
  ```
- version 为 1、2、3 或缺省时，sing-box 不支持，交给 WP07。
- sing-box JSON 中的 snell 按原样导入（仅 v4 和 v6）。
- 删除 Surge 解析器中对 `snell` 的主动拒绝（`parser.go:362`）。`ssr` 和 `naive` 仍然转给 WP07 或报告处理。

## 8. 分享链接补全（sing-box 路径）

| scheme | 映射 |
|---|---|
| `tuic://uuid:password@host:port?congestion_control=&udp_relay_mode=&alpn=h3,..&sni=&allow_insecure=1&disable_sni=1#name` | `{"type":"tuic","uuid","password","congestion_control","udp_relay_mode","tls":{"enabled":true,"server_name":sni,"alpn":[..],"insecure":bool,"disable_sni":bool}}` |
| `hysteria://host:port?protocol=udp&auth=&peer=&insecure=1&upmbps=&downmbps=&alpn=&obfs=xplus&obfsParam=#name` | `{"type":"hysteria","up_mbps","down_mbps","auth_str":auth,"obfs":obfsParam,"tls":{"enabled":true,"server_name":peer,"insecure","alpn":[..]}}`；`protocol` 不是 udp 时写入报告 `UNSUPPORTED_FEATURE:hysteria protocol=<x>` |
| `anytls://password@host:port?sni=&insecure=1&fp=#name` | `{"type":"anytls","password","tls":{"enabled":true,"server_name","insecure","utls":{"enabled":fp!="","fingerprint":fp}}}` |
| `wireguard://`、`wg://` | 见 §3 |
| `ssh://user:pass@host:port#name` | `{"type":"ssh","user","password"}`（URI 中的私钥不支持） |
| `ss://`（修复） | ① SIP002 中 `@host:port/?plugin=` 格式：解析前去掉 `?` 前面的 `/`；② 非 base64 的 userinfo 要做 URL 解码（修复 ss2022 密钥中 `%3D` 的问题）；③ `plugin` 参数值先 URL 解码，再按 `;` 拆分 |
| `ssr://`、`mierus://`，以及 `vless://` 中带 `type=xhttp`/`splithttp`，或 `encryption` 不为空也不为 `none` | 交给 WP07 |

每个 scheme 至少 3 个测试用例，其中包括一个非法用例（应写入报告，而不是 panic）。

## 9. 解析报告

```go
type ParseResult struct {
    Nodes   []ParsedNode  `json:"-"`
    Skipped []SkippedNode `json:"skipped"` // 最多 500 条，超出部分只计数
    Stats   ParseStats    `json:"stats"`
}
type ParseStats struct {
    Total      int            `json:"total"`
    Imported   int            `json:"imported"`
    Skipped    int            `json:"skipped"`
    ByEngine   map[string]int `json:"by_engine"`
    ByProtocol map[string]int `json:"by_protocol"`
}
type SkippedNode struct {
    Name   string `json:"name"`
    Type   string `json:"type"`
    Source string `json:"source"` // singbox|clash|surge|uri|ovpn|plain
    Reason string `json:"reason"` // UNSUPPORTED_PROTOCOL|UNSUPPORTED_FEATURE|ENGINE_NOT_BUILT|INVALID
    Detail string `json:"detail"`
}
```

- 新增 `subscription.ParseWithReport(data []byte) (ParseResult, error)`。原来的 `ParseGeneralSubscription` 保留，内部调用新函数，只返回 `Nodes`。
- 解析器的每个转换函数签名改为 `(ParsedNode, *SkippedNode, bool)` 或等价形式。**不允许再有静默丢弃**：凡是能识别出 type 却没有导入的节点，都必须出现在 `Skipped` 中。
- 订阅每次刷新后调用 `engine.SetSubscriptionParseReport(id, json)`（WP02）。
- 新增 API：
  - `GET /api/v1/subscriptions/{id}/parse-report`
  - `POST /api/v1/subscriptions/actions/preview-parse`：请求体 `{"content":"..."}` 或 `{"url":"..."}`；URL 使用现有下载器（沿用大小上限和超时）；返回 `ParseResult` 以及前 50 个节点的 `{name, engine, protocol, protocol_detail}`；**不落库**。

## 10. 能力接口

`GET /api/v1/system/capabilities` 返回：

```json
{"engines":[
  {"name":"singbox","version":"1.14.0","outbound_types":["anytls","http","hysteria","hysteria2","shadowsocks","shadowtls","snell","socks","ssh","trojan","tuic","vless","vmess"],"endpoint_types":["openconnect","openvpn-client","wireguard"]},
  {"name":"mihomo","built":true,"version":"1.19.31","fallback_types":["ssr","mieru","snell(v1-3)","vless(xhttp/encryption)","masque","trusttunnel","sudoku","shadowquic","gost-relay","wireguard(amnezia)","ss(restls/kcptun/gost-plugin)"]}
],"build_tags":["..."],"share_link_schemes":["vmess","vmess1","vless","trojan","ss","ssd","ssr","hysteria","hysteria2","hy2","tuic","anytls","wireguard","wg","ssh","socks","socks5","socks5h","http","https","tg","netch","mierus"],"file_formats":["singbox-json","clash-yaml","clash-json","surge","uri-lines","base64","plain-proxy-lines","ovpn","prism-openvpn-bundle"]}
```

- 列表写成代码中的常量。
- 加一个测试：遍历 `outbound_types` 和 `endpoint_types`，用 sing-box 注册表的 `CreateOptions(type)` 确认每个类型都存在。

## 11. 协议矩阵测试（`internal/outbound/protocol_matrix_test.go`，名称为 `TestProtocolMatrix`）

采用表驱动，每个用例包含：输入片段、期望 engine、期望 kind、期望 protocol，以及 `Build` 的期望结果（成功或错误码）。**不访问网络。** 至少覆盖以下用例：

| 输入 | 期望 |
|---|---|
| ss（AEAD、2022 原样、2022 百分号编码、SIP002 obfs 插件、v2ray-plugin） | singbox / outbound / 成功 |
| vmess（ws+tls、grpc）、vless（reality+vision、ws、grpc、httpupgrade）、trojan（ws）、hysteria2（salamander、端口跳跃） | singbox / 成功 |
| tuic、hysteria、anytls、ssh 的 URI 以及对应的 Clash 写法 | singbox / 成功 |
| socks5、http、https、`ip:port:user:pass` | singbox / 成功 |
| WireGuard 的五种来源（旧 outbound JSON、endpoint JSON、Clash、Surge、URI） | singbox / endpoint / 成功 |
| `.ovpn` 夹具（全部成功用例） | singbox / endpoint / 成功 |
| sing-box JSON 中 ss 加 detour 到 shadowtls；Clash ss 加 shadow-tls 插件；Clash `dialer-proxy` | singbox / chain / 成功，且拨号错误不含 "detour not found" |
| snell v4（Clash、Surge） | singbox / 成功 |
| ssr、mieru、vless-xhttp、vless-encryption、snell v3、wg amnezia、ss restls | 带 `with_mihomo`：mihomo / proxy / 成功（WP07）；不带：报告 `ENGINE_NOT_BUILT` |
| tailscale endpoint、naive | 报告 `ENGINE_NOT_BUILT:<x>` |
| 非法 URI | 报告 `INVALID`，不 panic |

## 12. 验收

```sh
make protocol-matrix
make verify
```

另外手工核对：导入一个包含上述各类节点的本地订阅，`GET /api/v1/subscriptions/{id}/parse-report` 的统计结果与预期一致。

---

# WP07 · mihomo 兜底（仅限 sing-box 不支持的 Clash 系协议）

**前置**：WP06。**原则**：能用 sing-box 的节点**一律**用 sing-box。mihomo 只接管下面判定表列出的情况，**没有任何配置项**。进程内调用，不做 sidecar。代码放在构建标签 `with_mihomo` 下（完整版默认开启）。

## 1. 依赖

```sh
go get github.com/metacubex/mihomo@v1.19.31
```

- 已实测可以与 sing-box 1.14 共存（事实 F11）。
- 在 `THIRD_PARTY_NOTICES.md` 中登记：GPL-3.0，与 Prism 的许可证兼容。
- mihomo 的包只能被带 `//go:build with_mihomo` 的文件引用，确保 `make backend-lite` 编译出的二进制不包含 mihomo。

## 2. 使用 mihomo 的判定表（唯一依据）

一个节点当且仅当满足下列**任意一条**时，交给 mihomo：

| 条件 | 说明 |
|---|---|
| Clash `type` ∈ {`ssr`, `mieru`, `masque`, `trusttunnel`, `sudoku`, `shadowquic`, `gost-relay`} | sing-box 1.14 不支持这些类型 |
| Clash `type=snell` 且 `version` ∉ {4} | sing-box 只支持 Snell v4 和 v6（事实 F9）；Clash 一般不写 v6 |
| Clash `type=vless` 且（`network` ∈ {`xhttp`, `splithttp`}，或 `encryption` 不为空且不为 `none`） | XHTTP 传输和 VLESS 加密 |
| Clash `type=ss` 且 `plugin` ∈ {`restls`, `kcptun`, `gost-plugin`} | `shadow-tls` 插件走 sing-box 串接（WP06 §6），**不在此列** |
| Clash `type=wireguard` 且带有 `amnezia-wg-option` | AmneziaWG |
| 分享链接 `ssr://`、`mierus://`；`vless://` 带 `type=xhttp` 或 `splithttp`，或 `encryption` 不为空也不为 `none` | 用 `convert.ConvertsV2Ray([]byte(line))` 转成 map（事实 F13） |

**明确不走 mihomo 的情况**：`openvpn`、`wireguard`（普通）、`tailscale`、`hysteria`、`hysteria2`、`tuic`、`anytls`、`vmess`、`trojan`、`ss`（普通或 shadow-tls）、`ssh`、`socks`、`http`。这些类型如果 sing-box 转换失败，就写入报告（原因 `INVALID`），**不要**退回到 mihomo 重试。

## 3. 解析器接线（`internal/subscription`）

- 在 Clash 转换 `convertClashProxyToNode` 的最前面调用 `needsMihomo(proxy map[string]any) (bool, string)`（实现 §2 的判定表）：
  - 返回 true 且带 `with_mihomo` 时：输出形态 B `{"prism_node":1,"engine":"mihomo","kind":"proxy","name":<name>,"proxy":<原 map，去掉 name>}`；
  - 返回 true 但没有 `with_mihomo` 时：写入报告，原因 `ENGINE_NOT_BUILT:mihomo`，detail 填判定原因。
- Surge 行先转换成 Clash map（现有逻辑），再走同一个判定。
- 分享链接行：`ssr://` 和 `mierus://` 直接走 `ConvertsV2Ray`；`vless://` 先检查 query 中的 `type` 和 `encryption`，命中判定表时才走 `ConvertsV2Ray`，否则走现有的 sing-box 解析。
- 构建标签的拆分：`needsMihomo` 和判定表**不依赖** mihomo 包，所以写在普通文件里。只有 `ConvertsV2Ray` 和 `ParseProxy` 的调用放在带 `with_mihomo` 标签的文件中，另外提供一个 stub 文件（`//go:build !with_mihomo`），其中函数返回 `errMihomoNotBuilt`。

## 4. 运行时：mihomo 节点适配器（`internal/outbound/mihomo_outbound.go`，`//go:build with_mihomo`）

```go
type mihomoOutbound struct {
    tag   string
    typ   string   // 归一化后的协议名，例如 "ssr"
    proxy C.Proxy  // github.com/metacubex/mihomo/constant
    once  sync.Once
}

func buildMihomo(tag string, m map[string]any) (adapter.Outbound, error) {
    p, err := mihomoadapter.ParseProxy(m) // github.com/metacubex/mihomo/adapter
    if err != nil { return nil, fmt.Errorf("mihomo: %w", err) }
    return &mihomoOutbound{tag: tag, typ: normalize(m["type"]), proxy: p}, nil
}

func (o *mihomoOutbound) Type() string           { return o.typ }
func (o *mihomoOutbound) Tag() string            { return o.tag }
func (o *mihomoOutbound) Dependencies() []string { return nil }
func (o *mihomoOutbound) Network() []string {
    if o.proxy.SupportUDP() { return []string{N.NetworkTCP, N.NetworkUDP} }
    return []string{N.NetworkTCP}
}
func (o *mihomoOutbound) DialContext(ctx context.Context, network string, dst M.Socksaddr) (net.Conn, error) {
    if N.NetworkName(network) != N.NetworkTCP { return nil, E.New("mihomo outbound: only tcp is supported") }
    md := &C.Metadata{NetWork: C.TCP, Type: C.INNER, DstPort: dst.Port}
    if dst.IsFqdn() { md.Host = dst.Fqdn } else { md.DstIP = dst.Addr.Unmap() }
    return o.proxy.DialContext(ctx, md) // C.Conn 实现了 net.Conn
}
func (o *mihomoOutbound) ListenPacket(ctx context.Context, dst M.Socksaddr) (net.PacketConn, error) {
    return nil, E.New("mihomo outbound: udp is not supported by prism") // Prism 只转发 TCP
}
func (o *mihomoOutbound) Close() error { var err error; o.once.Do(func() { err = o.proxy.Close() }); return err }
```

- `SingboxRuntime.Build` 遇到 `engine=mihomo` 时，转交给 `buildMihomo`（带标签时），否则返回 `errMihomoNotBuilt`。更简洁的做法是新建一个 `CompositeBuilder`，按 engine 分发给两个实现。
- **DNS**：mihomo 节点中域名形式的服务器地址使用系统解析器，**不经过** `PRISM_NODE_DNS_UPSTREAMS`。这是已知限制，写入 `docs/PROTOCOLS.md`。不要为此初始化 mihomo 的全局 resolver。
- **不初始化** mihomo 的 tunnel、全局配置或统计模块，只使用 `adapter.ParseProxy`。
- mihomo 节点内的 `dialer-proxy` 不支持：在解析阶段写入报告，原因 `UNSUPPORTED_FEATURE:dialer-proxy(mihomo)`。

## 5. 测试（带 `with_mihomo` 标签，不访问网络）

1. `TestProtocolMatrix` 中 mihomo 相关的行（WP06 §11）全部通过：构建成功，`Type()` 返回值正确，`Close()` 幂等。
2. 判定表单元测试：每一行至少一个命中用例和一个"相近但不命中"的用例。例如 `vless` 加 `encryption=none` 应走 sing-box；`ss` 加 `shadow-tls` 应走 sing-box 串接。
3. 拨号测试：用 `net.Listen` 起一个本地监听，它接受连接后立即关闭。mihomo 的 `socks5` 类型本身不在兜底列表中，但可以在**测试中直接调用** `buildMihomo`，用它连接本地的 SOCKS5 测试服务器（testutil），验证适配器的数据通路。
4. 精简版（不带标签）：同样的输入全部进入报告，原因为 `ENGINE_NOT_BUILT:mihomo`，而且精简版二进制中不包含 mihomo 的包：

   ```sh
   go list -deps -tags "$(TAGS_BASE)" ./cmd/prism | grep -c metacubex
   # 结果必须为 0
   ```

## 6. 验收

```sh
make protocol-matrix
make verify
make backend && make backend-lite && ls -la bin/   # 记录两个变体的体积
```

---

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

---

# WP09 · 数据源、经节点检测与解锁检测

**前置**：WP08。**目标**：

- 建立可插拔的数据源框架，分三类：离线库、服务端在线查询、经节点查询；
- 接入 DNSBL 黑名单；
- 用规则驱动流媒体和 AI 服务的解锁检测；
- 所有结果写入 intel.db（WP08）。

IPPure 改为可批量、可持久化的经节点数据源（按用户要求）。

## 1. 接口（`internal/intel/providers/provider.go`）

```go
type Kind int
const (
    KindOffline  Kind = iota // 本地数据库查询，无网络
    KindOnlineIP             // 服务端按 IP 查询第三方 API（不经节点）
    KindViaNode              // 经被测节点访问"查询我自己"类接口
)

type Spec struct {
    ID, Name, Website, Terms string // Terms：条款与额度提示，显示在设置页
    Kind              Kind
    Profile           string        // 标准化版本，如 "proxycheck-v3-2"
    RequiresKey       bool
    DefaultEnabled    bool          // 无 Key 也能用时才可能为 true
    DefaultDailyLimit int           // 0 = 不限
    DefaultQPS        float64       // 0 = 不限
    BatchSize         int           // online-ip：一次 Lookup 最多几个 IP；1 = 不支持批量
    DefaultTTL        time.Duration
    SupportsIPv6      bool
}

type Result struct {
    Evidence *quality.Evidence  // status=ok 时非空
    Raw      []byte             // ≤ 32 KiB；为空表示不存
    Err      *ProviderError     // Code: PROVIDER_LIMIT|PROVIDER_AUTH|PROVIDER_UNAVAILABLE|PROVIDER_RESPONSE|UNSUPPORTED_IP|EGRESS_MISMATCH
}

type OfflineProvider interface { Spec() Spec; Lookup(ip netip.Addr) Result }
type OnlineProvider  interface { Spec() Spec; Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result }
type ViaNodeProvider interface { Spec() Spec; Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result }
```

- 数据源的生效设置由三部分合并得到：`Spec` 中的默认值、state.db 中的 `intel_provider_settings`（WP02），以及环境变量。修改 Key 后自动重算 `credential_id`，从而解除 paused 状态（WP08）。
- 环境变量只在**首次启动、数据库里还没有对应设置时**写入一次默认值，之后以数据库（设置页）为准。旧版 Prism 的变量按下表迁移：

  | 旧变量 | 新设置 |
  |---|---|
  | `PRISM_QUALITY_API_KEY` | proxycheck 的 Key |
  | `PRISM_QUALITY_DAILY_LIMIT` | proxycheck 的每日额度 |
  | `PRISM_ABUSEIPDB_API_KEY` | abuseipdb 的 Key |
  | `PRISM_QUALITY_ENABLED` | 运行时配置 `intel_enabled` |
  | `PRISM_QUALITY_WORKERS` | 运行时配置 `intel_node_workers` |
  | `PRISM_QUALITY_QUEUE_SIZE` | 忽略，并打印一次弃用告警 |

  新增变量：`PRISM_MAXMIND_ACCOUNT_ID`、`PRISM_MAXMIND_LICENSE_KEY`、`PRISM_IPINFO_TOKEN`、`PRISM_IPQS_API_KEY`、`PRISM_IPAPI_IS_API_KEY`。
- online-ip 类型的数据源，其 HTTP 客户端**不走代理节点**，也**不继承** `HTTP_PROXY` 环境变量。沿用现有 `NewProxyCheckProvider` 中的严格 Transport：禁止重定向、设置超时、限制响应大小，并且错误信息中不能带出包含 Key 的 URL。
- via-node 类型的数据源只通过被测节点的 `adapter.Outbound` 拨号，沿用现有 `fetchIPPure` 的写法：不走环境代理、不走路由、不走 bypass。

## 2. 证据字段扩展（`internal/quality/model.go` 中的 `Evidence`）

新增以下字段，全部为可选并带 `omitempty`：

```go
ASNNumber      int      `json:"asn_number,omitempty"`
City           string   `json:"city,omitempty"`
Region         string   `json:"region,omitempty"`
RegisteredCC   string   `json:"registered_country,omitempty"`
UsageType      string   `json:"usage_type,omitempty"`     // 数据源原始的用途类型
IsMobile       *bool    `json:"is_mobile,omitempty"`
IsResidential  *bool    `json:"is_residential,omitempty"`
FraudScore     *int     `json:"fraud_score,omitempty"`    // 数据源自己的 0–100 风险分（高 = 风险大）
DNSBLListed    []string `json:"dnsbl_listed,omitempty"`   // 命中的 zone
DNSBLChecked   []string `json:"dnsbl_checked,omitempty"`
```

保留原有的 `RiskScore`、`Signals`、`Native`、`TorRoles`、`AbuseConfidence` 等字段，保证旧的解码器测试继续通过。

## 3. 内置数据源

"默认额度"一律按保守值设定，用户可以在设置页调整。凡是涉及第三方的额度与条款，**实现时必须对照官网核实，并写进 `Terms` 字段**。

| id | 类型 | 需要 Key | 默认启用 | 默认日额度 / QPS | TTL | 输出 |
|---|---|---|---|---|---|---|
| `geo_country` | offline | 否 | 是 | 不限 | — | 国家（Resin 原有的 country.mmdb，保持地区过滤兼容） |
| `dbip_lite` | offline | 否 | 是 | 不限 | 30d | ASN、组织、城市、国家（DB-IP Lite mmdb，CC BY 4.0，每月更新；UI 中注明出处） |
| `maxmind_geolite2` | offline | 是（`account_id` 与 `license_key`） | 有 Key 时 | 不限 | 30d | ASN、城市、`registered_country` |
| `ipinfo_lite` | offline | 是（token） | 有 Key 时 | 不限 | 30d | ASN、国家 |
| `torproject` | offline | 否 | 是 | 不限 | 6h | Tor 角色（沿用现有的 Onionoo 名录实现） |
| `proxycheck` | online-ip | 可选 | 是 | 无 Key 时 80/天；有 Key 默认 900/天，**用户可调到套餐上限（最大 1,000,000）**；1 QPS | 24h | 风险分、类型、ASN、组织、代理/VPN/Tor/机房/被攻陷/爬虫/匿名信号、运营商、攻击历史（现有解码器） |
| `abuseipdb` | online-ip | 是 | 有 Key 时 | 900/天，1 QPS | 24h | 30 天举报置信度、举报数、举报人数（现有解码器） |
| `ipqs` | online-ip | 是 | 有 Key 时 | 150/天，1 QPS | 72h | fraud_score、proxy、vpn、tor、recent_abuse、bot_status、connection_type、ISP、ASN |
| `ipapi_is` | online-ip | 可选 | 否 | 500/天，1 QPS | 72h | is_datacenter、is_vpn、is_proxy、is_tor、is_abuser、company.type、asn |
| `dnsbl` | online-ip | 否 | 是 | 不限；每个 zone 5 QPS | 24h | 命中的黑名单 zone |
| `ippure` | via-node | 否 | 是 | 500/天；1 次/60 秒（全局） | 7d | fraudScore（→ `FraudScore`，仅 IPv4）、isResidential、isBroadcast（→ `Native`）、ASN、组织、国家（现有解码器） |
| `ip_api` | via-node | 否 | 是 | 不限；全局 5 QPS | 7d | 国家、城市、ISP、组织、AS、mobile、proxy、hosting |

### 各数据源要点

- **离线库下载**：统一放在 `$PRISM_CACHE_DIR/geo/`。
  - 每周检查一次更新；下载使用现有的 `netutil.RetryDownloader`（直连失败时可以借用节点下载，沿用 Resin GeoIP 的做法）。
  - 下载后校验：能成功打开 mmdb，并且对 `1.1.1.1` 能查到结果；校验通过后原子替换旧文件。
  - 各数据库的下载地址：
    - MaxMind：`https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz` 和 `.../GeoLite2-City/download?suffix=tar.gz`，使用 HTTP Basic 认证（account_id:license_key）；
    - DB-IP：`https://download.db-ip.com/free/dbip-asn-lite-YYYY-MM.mmdb.gz` 和 `dbip-city-lite-YYYY-MM.mmdb.gz`，按当月文件名下载，找不到时回退到上个月；
    - IPinfo Lite：`https://ipinfo.io/data/ipinfo_lite.mmdb?token=...`。
  - **实现时核对以上三个地址的当前格式**。
- **proxycheck**：删除 `PRISM_QUALITY_DAILY_LIMIT` 最大 900 的硬上限。无 Key 时仍然限制在 80 以内，因为匿名额度按源 IP 共享。支持批量查询时把 `BatchSize` 设为 N，这一点需要对照 v3 文档确认；未确认前按 1 处理。
- **dnsbl**：
  - 默认 zones：`zen.spamhaus.org`、`bl.spamcop.net`、`psbl.surriel.com`，可以在 `config_json.zones` 中修改。**不要**加入已经停止服务的 `dnsbl.sorbs.net`。
  - 查询 IPv4 反序的 A 记录；只要返回 `127.0.0.x` 就算命中。
  - 返回 `127.255.255.x` 表示查询被拒绝（例如 Spamhaus 拒绝经公共解析器的查询），此时记为 `error_code=DNSBL_REFUSED`，**不算命中**。
  - 解析器默认使用系统解析器，可以通过 `config_json.resolver`（形如 `udp://host:53`）指定。
  - UI 中提示：Spamhaus 免费使用仅限非商业、低流量场景，而且不能经公共 DNS 查询。
  - 暂不查 IPv6（记为 unsupported）。
- **ip_api**：免费版只能用 HTTP，且仅限非商业用途；每个源 IP 每分钟 45 次。由于经节点访问，占用的是**各节点出口 IP 自己的额度**，天然适合批量检测。
  - 请求地址：`http://ip-api.com/json/?fields=status,message,country,countryCode,regionName,city,isp,org,as,asname,mobile,proxy,hosting,query`
  - 字段映射：`query` 用于核对出口 IP；`proxy` 映射到 `Signals.Proxy`；`hosting` 映射到 `Signals.Hosting`；`mobile` 映射到 `IsMobile`；`as` 形如 `AS13335 Cloudflare, Inc.`，解析出 `ASNNumber`。
- **ippure**：
  - 删除只存内存的 `IPPureChecker` 缓存和 512 条上限，结果写入 intel.db。
  - 全局限速默认 1 次/60 秒，用户可以在设置页调整。
  - 设置页显示提示："IPPure 条款可能限制批量或系统性使用，请自行确认；Prism 默认低频率调用"。
  - 返回的 IP 与本次出口 IP 不一致时，记为 `EGRESS_MISMATCH`，不写入证据。
- **每个数据源都要有夹具测试**：把真实响应脱敏后保存到 `testdata/<id>/*.json`；解码器必须拒绝字段矛盾、越界和 IP 不匹配的响应（沿用现有 proxycheck 解码器的严格风格）。

## 4. 数据源设置 API

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/intel/providers` | 列出每个数据源的 `spec`、生效设置（**不含 Key**，只有 `has_key`）、今日用量、`next_allowed_at`、`blocked_until`、`paused`、`error_code`、`queued`、`running`、`failed` |
| `PATCH /api/v1/intel/providers/{id}` | 请求体：`{"enabled":bool, "api_key":string\|null, "daily_limit":int, "qps":number, "ttl":"24h", "config":{...}}`。`api_key` 为 `""` 表示清除，为 `null` 表示不修改 |
| `POST /api/v1/intel/providers/{id}/actions/test` | 用 `1.1.1.1`（online-ip）或任意一个健康节点（via-node）做一次真实查询，返回标准化结果。**会消耗 1 次额度**，接口文档中要写明 |
| `POST /api/v1/intel/providers/{id}/actions/resume` | 解除 paused 状态 |

## 5. 解锁检测（`internal/intel/checks/`）

### 5.1 规则格式（YAML）

- 内置规则通过 embed 打包在 `checks/builtin/*.yaml` 中。
- 用户可以把自己的规则放在 `$PRISM_STATE_DIR/checks.d/*.yaml`。与内置规则同 id 时，用户规则覆盖内置规则。每 60 秒检查一次文件变化并热加载。

```yaml
id: claude                 # [a-z0-9_]+，唯一
version: 3                 # 规则变更时递增；与已存结果版本不同则视为过期
name: Claude
category: ai               # ai|video|music|social|search|mail|other
enabled: true
ttl: 24h
timeout: 10s
steps:
  - id: home
    request:
      method: GET
      url: https://claude.ai/
      follow_redirects: false
      headers: {Accept-Language: "en-US,en;q=0.9", User-Agent: "<浏览器 UA，统一常量>"}
      max_body_bytes: 262144
  # 也支持 tcp 步骤：- id: smtp; tcp: {host: smtp.gmail.com, port: 25}
outcomes:                  # 按顺序匹配，第一条命中即采用
  - when: {step: home, header_contains: {Location: "app-unavailable-in-region"}}
    outcome: blocked
  - when: {step: home, status_in: [403, 503], body_contains: "challenge-platform"}
    outcome: captcha
  - when: {step: home, status_in: [200, 301, 302, 307]}
    outcome: available
default: unknown
region:                    # 可选：从某一步抓取地区码（两位大写字母）
  step: home
  body_regex: '"countryCode":"([A-Z]{2})"'
```

### 5.2 匹配器

每个 `when` 可以组合多个条件，条件之间为 AND：

- `step`
- `status_in`、`status_not_in`
- `header_contains: {Name: substr}`、`header_regex: {Name: re}`
- `body_contains`、`body_not_contains`、`body_regex`
- `redirect_host`（Location 头中的 host）
- `connected: true|false`（仅用于 tcp 步骤）
- `error: timeout|refused|tls|any`

### 5.3 执行

- 每个检测的每一步都用绑定被测节点的 `http.Client`（写法同 `fetchIPPure`）。
- 不自动跟随重定向（除非规则中写明）。
- 响应体读取不超过 256 KiB；整个检测的超时以规则中的 `timeout` 为准。
- 并发控制：同一节点同一时刻只跑 1 个检测；同一条规则全局并发不超过 `intel_check_concurrency_per_check`（默认 2）。

### 5.4 内置初始规则

下表中的判定信号来自社区常用检测脚本的公开做法。**上线前必须对每条规则抓取真实响应（至少包括可用地区和不可用地区两类），脱敏后作为夹具写成测试，并据此校准匹配器。** 规则文件里要注明校准日期。

| id | 判定信号（需校准） |
|---|---|
| `google_captcha` | `https://www.google.com/search?q=prism&hl=en`：状态码 429，或重定向到 `/sorry/` 时判为 captcha；200 判为 available |
| `youtube_premium` | `https://www.youtube.com/premium`（带 `Accept-Language: en`）：正文含 "Premium is not available in your country" 判为 blocked；地区从 `"countryCode":"XX"` 或 `"GL":"XX"` 中提取 |
| `netflix` | 请求一部 Netflix 自制剧和一部非自制剧的标题页（两个 `/title/<id>` 在校准时选定，写进规则注释）：两者都返回 200 为 available，只有自制剧返回 200 为 region_limited，返回 403 或被拦截为 blocked；地区从重定向路径 `/xx-en/` 中提取 |
| `chatgpt` | 先请求 `https://chatgpt.com/cdn-cgi/trace` 取 `loc`；再请求 `https://ios.chat.openai.com/`，正文含 `unsupported_country` 判为 blocked，含 `VPN` 判为 captcha |
| `claude` | 见 §5.1 的示例 |
| `gemini` | `https://gemini.google.com/`：以不可用提示或跳转作为 blocked 依据，**信号必须在校准时确定** |
| `tiktok` | `https://www.tiktok.com/`：地区从正文 `"region":"XX"` 中提取；被屏蔽页判为 blocked |
| `smtp25` | 用 tcp 步骤连接 `smtp.gmail.com:25`：能连上判为 available（出口允许 25 端口），否则判为 blocked |

- 可以通过 `PATCH /api/v1/intel/checks/{id}` 修改 `enabled`（写入 `intel_provider_settings`，provider_id 为 `check:<id>`）。
- `GET /api/v1/intel/checks` 列出所有规则的元信息、来源（builtin 或 user）和校准日期。

## 6. 测试

1. 每个数据源的解码器都要有夹具测试：成功、字段缺失、越界、IP 不匹配、429、401。
2. DNSBL：用本地 DNS 测试服务器（`miekg/dns` 起在 127.0.0.1）模拟命中、未命中和 `127.255.255.254`。
3. 离线库：用最小 mmdb 夹具（可以用 `maxmind/mmdbwriter` 在测试中生成）。
4. 检测引擎：
   - 用 httptest 服务器，配合 `testutil` 中的"直连 outbound"来模拟节点；
   - 覆盖每一种匹配器、默认结果、区域提取、超时、响应体上限；
   - 用户规则覆盖内置规则、热加载。
5. 内置规则：每条规则至少有 "available" 和 "blocked" 两个夹具，放在 `checks/testdata/<id>/`。

## 7. 验收

```sh
make verify
```

手工验收：在设置页填入 proxycheck 的 Key，对 20 个节点创建 `kind=full` 任务：

- intel.db 中的 `evidence`、`node_checks`、`ip_assessment` 都有数据；
- 重启后 `GET /api/v1/intel/ip/{ip}` 仍能返回完整证据。

---

# WP10 · 纯净度评估、平台质量准入与轮换增强

**前置**：WP09。**目标**：

- 用多来源证据计算可解释的纯净度评估（`prism-purity-v2`）；
- 让平台质量准入真正可用，默认失败即拒绝（fail-closed）；
- 在证据变化或过期时自动重新评估；
- 定时轮换支持"避开上一个出口 IP"。

## 1. 评估算法 `prism-purity-v2`（`internal/intel/assess`，纯函数）

```go
func Assess(ip netip.Addr, ev []quality.Evidence, now time.Time, enabled map[string]bool) Assessment
```

- 只使用**有效**证据：`status=ok` 且 `now < valid_until`。
- `enabled` 表示当前启用的数据源，用于计算覆盖率。
- 评估结果必须是确定的：相同输入得到相同输出，便于测试。

### 1.1 洁净分项（0–100，越高越干净）

| 分项 | 来源与换算 | 权重 |
|---|---|---|
| `proxycheck` | `100 - RiskScore` | 3 |
| `ippure` | `100 - FraudScore`（仅 IPv4） | 3 |
| `ipqs` | `100 - FraudScore` | 3 |
| `abuseipdb` | `100 - AbuseConfidence` | 2 |
| `ipapi_is` | 按标志换算：`is_abuser` 为 40，`is_proxy`、`is_vpn` 或 `is_tor` 为 60，都不是为 100 | 1 |
| `ip_api` | `proxy` 为 60，否则为 100 | 1 |
| `dnsbl` | `100 - 34 × 命中 zone 数`（下限 0）；只统计成功查询的 zone | 1 |

- 分数计算：`base = Σ(w_i × c_i) / Σ w_i`，只对有效分项求和。
- **扣分**：只要有任一有效来源给出某个标志，就在 base 上扣一次，不重复叠加。扣分结果截断到 0–100。

  | 标志 | 扣分 |
  |---|---|
  | compromised | −40 |
  | tor（任一来源，含 torproject 名录中的 exit 角色） | −30 |
  | vpn | −10 |
  | proxy | −10 |
  | scraper | −10 |
  | abuse（AbuseConfidence ≥ 25） | −10 |

- 没有任何有效分项时，`purity_score` 为 NULL，`state` 为 `pending`；如果所有已启用的来源都失败，`state` 为 `unsupported`。

### 1.2 置信度

- 覆盖率：`coverage = Σ 有效分项的权重 / Σ 已启用的计分来源的权重`。
- 一致度：`agreement = 1 - min(1, 分项标准差 / 50)`。
- 置信度按下表判定：

  | 置信度 | 条件 |
  |---|---|
  | `high` | 至少 2 个来源，`coverage ≥ 0.6`，且 `agreement ≥ 0.7` |
  | `medium` | 至少 2 个来源，或 `coverage ≥ 0.4` |
  | `low` | 只有 1 个来源 |
  | `none` | 没有来源 |

### 1.3 纯净度区间（沿用现有区间，前端已有样式）

| 区间 | 分数 |
|---|---|
| excellent | ≥ 95 |
| clean | ≥ 90 |
| fair | ≥ 80 |
| mixed | ≥ 60 |
| poor | < 60 |
| unknown | 分数为 NULL |

### 1.4 IP 类型（与纯净度无关，单独维度）

- 投票来源和映射：
  - proxycheck `network.type`：residential、business、wireless、mobile 原样映射；hosting 映射为 datacenter；
  - ipqs `connection_type`：Residential 映射为 residential；Mobile 映射为 mobile；Corporate 映射为 business；Data Center 映射为 datacenter；Education 映射为 business；
  - ipapi_is：`is_datacenter` 映射为 datacenter；`company.type` 为 isp 时映射为 residential，为 business 时映射为 business，为 hosting 时映射为 datacenter；
  - ip_api：`hosting` 映射为 datacenter；`mobile` 映射为 mobile；
  - IPPure：`isResidential=true` 映射为 residential，`false` 映射为 non_residential。
- 投票规则：
  - 在 {residential, mobile, business, wireless, datacenter} 中取票数最多的类型，平票时取非 datacenter 的一方；
  - non_residential 只在没有其他票时生效；
  - 同时出现 residential 或 mobile 与 datacenter，且双方票数都 ≥ 1、最多票数小于总票数的 2/3 时，判为 `conflicting`。
- 离线兜底：没有任何在线票时，如果 ASN 属于内置的云厂商 ASN 列表（`assess/hosting_asn.go`，至少包含 AWS、GCP、Azure、Oracle、Alibaba、Tencent、Huawei、DigitalOcean、Linode/Akamai、Vultr、Hetzner、OVH、Cloudflare），判为 `datacenter`，并在 reasons 中写入 `ASN_HOSTING_HEURISTIC`。

### 1.5 原生 IP

- 有 IPPure `isBroadcast` 时以它为准，`native = !isBroadcast`。
- 否则，如果离线库同时给出 `country` 和 `registered_country`：两者一致时 `native=true`，不一致时 `native=false`，并在 reasons 中写入 `NATIVE_HEURISTIC`。
- 两者都没有时，`native` 为 NULL。

### 1.6 判定（按顺序，第一条命中即采用）

1. 满足以下任一条件 → `high_risk`：compromised；tor 出口（torproject 名录中的 exit 角色，或信号 tor=true）；AbuseConfidence ≥ 75 且举报数 > 0；`purity_score < 60`。
2. IP 类型为 `conflicting` → `conflicting`。
3. 存在 proxy、vpn、scraper 或匿名信号，或 proxycheck 有攻击历史，或 DNSBL 命中 → `review`。
4. `purity_score` 为 NULL → `pending`。
5. `confidence` 为 `low` 且 `coverage < 0.3` → `incomplete`。
6. `purity_score < 80` → `caution`。
7. 其余 → `favorable`。

### 1.7 输出

- **有效期**：`valid_until` 取所有有效证据 `valid_until` 中的最小值；没有有效证据时取 `now + 1h`。
- **`components_json`**：列出每个分项的 `{source, raw, clean, weight, observed_at}`，以及每一项扣分。
- **`reasons`**：原因码数组，例如 `TOR_EXIT`、`RECENT_ABUSE`、`DNSBL_LISTED:zen.spamhaus.org`、`PROXY_DETECTED`、`NATIVE_HEURISTIC`。
- 与经节点检测无关的字段，都**按 IP 计算**。
- **测试**：表驱动，不少于 25 个用例，覆盖每条判定分支、每个扣分项、置信度边界、平票、冲突、离线兜底、IPv6（IPPure 分不计入）。

### 1.8 触发时机

- 证据写入后（WP08 的第 6 步和数据源队列工作者）。
- **过期巡检**：每 5 分钟扫描 `ip_assessment.valid_until_ns <= now`（只扫上次巡检以后到期的记录），重新计算，并调用 `NotifyEgressIPDirty`。
- **启动时**：如果库中存在 profile 不等于 `prism-purity-v2` 的记录，在后台以限速方式全量重算（每秒 500 个 IP）。

## 2. 平台质量准入（`internal/platform`）

模型见 WP02 §2（`model.QualityPolicy`）。评估函数：

```go
func (p *Platform) qualityAdmit(entry *node.NodeEntry, snap intel.SnapshotReader, now time.Time) (ok bool, reason string)
```

- **策略为空**（`IsEmpty()`）：直接放行，与 Resin 相同。
- **策略非空**时，按以下规则逐条检查，**默认失败即拒绝**：
  1. 取节点的出口 IP：优先 v4；平台 `ip_types` 或 `required_checks` 只关心 v6 的情况暂不支持。
  2. 取该 IP 的评估。没有评估，或 `state` 为 `pending`、`unsupported`、`stale` 时，按 `unknown_action` 处理；`unknown_action` 为空时视为 `exclude`，原因 `QUALITY_UNKNOWN`。
  3. `exclude_tor`（为 nil 时视为 true）且带 tor 标志 → 拒绝，原因 `QUALITY_TOR`。
  4. `exclude_high_risk`（为 nil 时视为 true）且判定为 `high_risk` → 拒绝，原因 `QUALITY_HIGH_RISK`。
  5. `allowed_verdicts` 非空且判定不在列表中 → 拒绝，原因 `QUALITY_VERDICT`。
  6. `min_purity` 已设置，且分数为 NULL 或低于阈值 → 拒绝，原因 `QUALITY_MIN_PURITY`（**分数为 NULL 同样拒绝**）。
  7. `ip_types` 非空且类型不在列表中 → 拒绝，原因 `QUALITY_IP_TYPE`。
  8. `min_confidence` 已设置且置信度低于它 → 拒绝，原因 `QUALITY_CONFIDENCE`。
  9. `require_native` 为 true 且 `native` 不为 1 → 拒绝，原因 `QUALITY_NATIVE`。
  10. `required_checks`：对每一条 `{check_id: outcome}`，节点的检测结果不存在、已过期，或结果与期望不符 → 拒绝，原因 `QUALITY_CHECK:<id>`。
  11. `max_assessment_age` 已设置且 `now - computed_at` 超过它 → 拒绝，原因 `QUALITY_STALE`。
  12. `max_egress_age` 已设置且 `now - LastEgressUpdate` 超过它 → 拒绝，原因 `QUALITY_EGRESS_STALE`。
- **接入方式**：
  - 在 `platform.evaluateNode` 的现有筛选（正则、地区）之后调用；
  - `GlobalNodePool` 的 `QualityLookup` 改为注入 `intel.SnapshotReader`；
  - 删除旧的 `QualityPolicy.Evaluate` 及其占位逻辑（`MinConfidence` 空实现、`actionAllows` 默认放行）。
- **出口 IP 反向索引**：`GlobalNodePool` 新增 `map[netip.Addr]map[node.Hash]struct{}`，在 `UpdateNodeEgressIP` 和节点删除时维护。新增 `NotifyEgressIPDirty(ip)`，对所有相关节点调用现有的 `notifyAllPlatformsDirty(hash)`。
- **时间型条件的巡检**：`max_assessment_age` 和 `max_egress_age` 与时间有关，由上面的 5 分钟巡检一并处理。对于策略中配置了这两项的平台，巡检时对其视图里超龄的节点执行 `NotifyDirty`。

### 2.1 API

- 平台的创建（POST）、修改（PATCH）和查询（GET）都新增 `quality_policy` 字段（对象）。创建和修改时校验：
  - 取值必须在枚举范围内；
  - `min_purity` 在 0–100 之间；
  - duration 字符串可以解析；
  - `required_checks` 中的 id 必须存在于当前规则中。
- `POST /api/v1/platforms/preview-filter`（上游已有接口）增加可选字段 `quality_policy`。返回值增加：
  ```json
  {"excluded_by": {"regex": n, "region": n, "QUALITY_MIN_PURITY": n}}
  ```
- 新增 `GET /api/v1/platforms/{id}/nodes/{hash}/explain`，返回每一条规则的检查结果：
  ```json
  [{"rule":"regex","passed":true}, {"rule":"quality.min_purity","passed":false,"detail":"score=72<80"}]
  ```

## 3. 定时轮换增强（`internal/routing`）

- API 字段已在 WP02、WP03 就位：`scheduled_rotation_enabled`、`scheduled_rotation_interval`，以及新增的 `rotation_avoid_previous_ip`（默认 true）。
- **轮换墓碑**：`Router` 新增一个内存 LRU（上限 100,000 条），键为 `platformID+"\x00"+account`，值为上一个出口 IP 及过期时间。过期时间取 `max(interval, sticky_ttl)`。
  - 轮换器删除租约时，写入一条墓碑；
  - 手动轮换接口也会写入墓碑。
- **选路**：为该账号创建新租约时，如果平台开启了 `rotation_avoid_previous_ip` 且存在墓碑，就在 P2C 的候选集中**排除**出口 IP 等于墓碑 IP 的节点。只有排除后没有候选时，才退回到不排除的结果，并在请求日志中记录 `rotation_fallback_same_ip`。
- **新 API**：`POST /api/v1/platforms/{id}/leases/{account}/actions/rotate`，执行"删除租约 + 写入墓碑"，返回 204。
- 墓碑不持久化（丢失的代价只是可能分到同一个 IP），这一点要写进文档。
- **测试**：
  - 开启后，连续轮换 10 次都不会连续两次拿到同一个出口 IP（候选池有 3 个 IP）；
  - 只有 1 个 IP 时退回到原 IP 并记录 fallback；
  - 墓碑达到上限时淘汰最旧的条目。

## 4. 节点列表中的质量信息

- `GET /api/v1/nodes` 和 `/nodes/{hash}` 的每个节点增加 `intel` 字段：
  ```json
  {
    "egress_ipv4":"", "egress_ipv6":"", "colo":"", "asn":0, "as_org":"", "country":"", "city":"",
    "ip_type":"", "native":null, "purity_score":null, "purity_band":"unknown",
    "confidence":"none", "verdict":"pending", "flags":[],
    "checks":{"chatgpt":"available"}, "assessed_at":""
  }
  ```
  数据全部从投影和 `node_egress` 中读取；列表场景按批次一次性读取，避免 N+1 查询。
- 新增筛选参数：`purity_min`、`purity_max`、`verdict`（可逗号分隔多值）、`confidence_min`、`native`（true/false）、`asn`、`country`、`check=<id>:<outcome>`（可重复）。原有的 `ip_type`、`purity_band` 改为从新评估读取。
- 新增排序字段：`purity_score`、`latency`、`assessed_at`。

## 5. 验收

```sh
make verify
```

新增测试中：assess 的表驱动测试不少于 25 个用例；平台准入的每条规则至少 1 个"通过"用例和 1 个"拒绝"用例；轮换墓碑测试；出口 IP 反向索引在节点删除后清理干净。

---

# WP11 · 导出（sing-box / mihomo / v2rayN / URI / CSV / JSON）与订阅输出

**前置**：WP06、WP07、WP10。**目标**：

- 把筛选后的节点（例如"纯净度 ≥ 90 的住宅节点"）一键导出为主流客户端能直接使用的格式；
- 可以生成带令牌的订阅链接，供客户端自动更新。

## 1. 包与入口

- 新建包 `internal/export`，统一入口：

  ```go
  func Export(nodes []Item, format string, opt Options) (body []byte, contentType string, report Report, err error)
  ```

- `Item` 包含：`Name`（按模板渲染后的名称）、`Doc`（节点文档，形态 A 或 B）、`Intel`（WP10 的节点 intel 摘要）。
- `Report` 记录导出结果：`{exported:int, skipped:[{name, reason}]}`。无法用目标格式表示的节点**跳过并记入报告**，不报错。

## 2. 各格式规则

### 2.1 `singbox`（JSON）

```json
{
  "outbounds": [
    {"type":"selector","tag":"PROXY","outbounds":["<name1>","<name2>","AUTO"]},
    {"type":"urltest","tag":"AUTO","outbounds":["<name1>","<name2>"],"url":"https://www.gstatic.com/generate_204","interval":"5m"},
    ...节点 outbound...
  ],
  "endpoints": [...WireGuard/OpenVPN/OpenConnect endpoint...]
}
```

- 形态 A 的节点：原样输出，并把 `tag` 设为节点名。
- 形态 B：
  - endpoint 节点放进 `endpoints`，`tag` 设为节点名；
  - chain 节点：deps 的 tag 改为 `<name>#d<i>`，并改写 detour 引用；deps 一并输出，但不加入 selector；
  - mihomo 节点：跳过，原因 `NOT_REPRESENTABLE:singbox`。
- 节点名如有重复，依次追加 ` #2`、` #3`。

### 2.2 `mihomo`（Clash Meta YAML）

```yaml
proxies: [...]
proxy-groups:
  - {name: PROXY, type: select, proxies: [AUTO, ...所有节点名]}
  - {name: AUTO, type: url-test, url: https://www.gstatic.com/generate_204, interval: 300, proxies: [...所有节点名]}
rules:
  - MATCH,PROXY
```

- mihomo 节点：直接输出原 `proxy` map，并把 `name` 设为节点名。
- sing-box 节点：通过**反向映射器** `internal/export/clashmap.go` 转换，至少支持下表中的类型；不支持的类型跳过，原因 `NOT_REPRESENTABLE:mihomo`。

  | sing-box | Clash 字段要点 |
  |---|---|
  | shadowsocks | `type: ss`、`cipher`、`password`、`plugin`/`plugin-opts`（obfs 与 v2ray-plugin 互转）、`udp-over-tcp` |
  | vmess | `uuid`、`alterId`、`cipher`、`tls`、`servername`、`skip-cert-verify`、`network` 与 `ws-opts`/`grpc-opts`/`h2-opts`/`http-upgrade`、`client-fingerprint` |
  | vless | 在 vmess 的基础上增加 `flow`，以及 `reality-opts{public-key,short-id}` |
  | trojan | `password`、`sni`、`skip-cert-verify`、`network` 与各传输选项 |
  | hysteria2 | `password`、`sni`、`obfs`、`obfs-password`、`ports`、`up`、`down` |
  | hysteria | `auth-str`、`up`、`down`、`obfs`、`sni`、`alpn` |
  | tuic | `uuid`、`password`、`congestion-controller`、`udp-relay-mode`、`alpn`、`sni` |
  | anytls | `password`、`sni`、`client-fingerprint` |
  | ssh | `username`、`password` |
  | socks、http | `type: socks5` 或 `type: http`，并带 `username`、`password`、`tls` |
  | wireguard endpoint | `type: wireguard`、`ip`、`ipv6`、`private-key`、`public-key`、`pre-shared-key`、`reserved`、`allowed-ips`、`mtu` |
  | snell v4 | `type: snell`、`version: 4`、`psk`、`obfs-opts` |
  | openvpn-client endpoint | `type: openvpn`，字段按 mihomo `OpenVPNOption` 反向映射：`ca`、`cert`、`key`、`tls-auth`、`key-direction`、`tls-crypt`、`cipher`、`data-ciphers`、`auth`、`username`、`password`、`proto`、`server`、`port` |
  | chain（ss 加 shadowtls） | `type: ss` 加 `plugin: shadow-tls`，`plugin-opts{host, password, version}` |
  | 其余 chain | 跳过，原因 `NOT_REPRESENTABLE:mihomo(chain)` |

### 2.3 `v2rayn`（对 URI 行整体做 base64）与 `uri`（明文 URI 行）

- 支持的类型与写法：

  | 类型 | 写法 |
  |---|---|
  | vmess | v2rayN JSON v2：`{"v":"2","ps","add","port","id","aid","scy","net","type","host","path","tls","sni","alpn","fp"}`，做 base64 |
  | vless、trojan | 标准 query：`type`、`security`、`sni`、`fp`、`pbk`、`sid`、`flow`、`path`、`host`、`serviceName`、`alpn`、`allowInsecure` |
  | ss | SIP002：userinfo 为 `base64url(method:password)`；有插件时写成 `/?plugin=` |
  | hysteria2 | `hysteria2://` |
  | hysteria | `hysteria://` |
  | tuic | `tuic://` |
  | anytls | `anytls://` |
  | wireguard | `wireguard://`（与 WP06 §3 的格式相同） |
  | socks | `socks://` |
  | http | `http://`、`https://` |
  | ssr | mihomo 节点，输出 `ssr://` |

- **与解析器互为逆运算**：对每种类型 T 执行"导出 → 再解析"，得到的节点哈希必须与原节点相同。测试逐一覆盖。
- 无法写成分享链接的类型（openvpn、openconnect、chain、mieru 等）：跳过。

### 2.4 `csv` 与 `json`（用于分析，不是客户端配置）

- 列：name、engine、protocol、protocol_detail、subscription、egress_ipv4、egress_ipv6、colo、country、city、asn、as_org、ip_type、native、purity_score、purity_band、confidence、verdict、flags，以及每个检测的结果、latency_ms、healthy、assessed_at。
- **不包含任何节点凭证**。

## 3. 名称模板

- 默认模板为 `{name}`。可用变量：`{name}`、`{flag}`（国旗 emoji）、`{country}`、`{city}`、`{asn}`、`{org}`、`{ip_type}`、`{purity}`、`{band}`、`{verdict}`、`{engine}`、`{protocol}`、`{latency}`、`{index}`。
- 未知值渲染为空字符串；渲染后压缩多余空格，并截断到 64 个字符。

## 4. API

### 4.1 一次性导出（管理员）

```text
GET /api/v1/nodes/export?format=singbox|mihomo|v2rayn|uri|csv|json&name_template=...&<WP10 中所有节点筛选参数>&healthy_only=true&limit=5000
```

- 使用 `Content-Disposition: attachment` 下载。
- 报告放在响应头中：`X-Prism-Export-Exported: N`、`X-Prism-Export-Skipped: M`。
- json 格式把报告放在响应体的 `report` 字段中。

### 4.2 订阅配置（管理员）

- `GET`、`POST /api/v1/export-profiles`；`GET`、`PATCH`、`DELETE /api/v1/export-profiles/{id}`；`POST /api/v1/export-profiles/{id}/actions/rotate-token`。
- 请求体：`{"name", "format", "platform_id", "filter":{同导出筛选}, "name_template", "enabled"}`。
- 创建或轮换令牌时，在响应中**只返回这一次**订阅地址 `"url": "<scheme>://<host>/sub/<token>"`，服务端只保存令牌的 sha256。

### 4.3 公开订阅（在主监听上注册，不经过管理员鉴权）

```text
GET /sub/{token}
```

1. 计算令牌的 sha256，查找对应配置；找不到或配置已停用时返回 404（不区分两种原因）。
2. 限流：每个令牌每分钟 60 次，每个 IP 每分钟 120 次；超出时返回 429。
3. 如果设置了 `platform_id`，只导出该平台当前可路由视图中的节点，并叠加 `filter`。
4. 更新 `last_access_at` 和 `access_count`，并写审计日志（actor 记为 `export:<id>`）。
5. 响应头：`Cache-Control: no-store`、`Content-Type` 按格式设置。v2rayN 格式额外带 `Profile-Update-Interval: 12`，mihomo 格式额外带 `Content-Disposition: inline; filename=prism.yaml`。

- 接入点（Endpoint）如果关闭了 `allow_management`，同样**不提供** `/sub/*`（与管理面一致）。

## 5. 测试

- 每种格式：
  - 与 golden 文件比对；
  - sing-box 格式输出后，能用 `option.Options` 反序列化并 `box.New` 成功（不启动）；
  - mihomo 格式输出后，每个 proxy 都能被 `adapter.ParseProxy` 解析（`with_mihomo`）；
  - v2rayN 格式与解析器互为逆运算（哈希一致）。
- 跳过报告正确。
- `/sub/{token}`：错误令牌返回 404，停用后返回 404，限流返回 429，平台视图过滤生效，访问计数递增。

## 6. 验收

```sh
make verify
```

手工验收：

- 把导出的 mihomo YAML 导入 mihomo 客户端，把 sing-box JSON 用 `sing-box check` 校验，把 v2rayN 订阅导入 v2rayN，三者都能识别所有已导出的节点；
- 记录验证时使用的客户端版本。

---

# WP12 · 前端改造（`internal/api/web`）

**前置**：WP04–WP11 的接口已经就位。**技术栈沿用现有方案**：React 19、TypeScript、Vite、@tanstack/react-query、@tanstack/react-table、react-hook-form 加 zod、i18next、lucide-react、recharts。页面结构和组件风格参照现有的 `features/*`（包括 `api.ts`、`types.ts`、页面组件、`components/ui/*`）。**不引入新的 UI 框架。**

## 1. 通用要求

- 所有请求都通过 `lib/api-client.ts` 发出。每个 feature 都有自己的 `api.ts`（封装 fetch）和 `types.ts`（与后端 JSON 字段一一对应，使用 snake_case）。
- 新增文案必须同时提供中文和英文（`i18n/translations.ts`；如需分文件，参照 `i18n/quality.ts` 的写法）。
- 支持深色和浅色主题、窄屏布局（≤ 768px）、键盘操作；所有图标按钮都要有 `aria-label`。
- SSE 使用 `EventSource`。由于 `EventSource` 不能带 `Authorization` 头，**后端 SSE 接口同时接受查询参数 `?access_token=`**：
  - 只对 `/events` 这类接口生效；
  - 服务端用常量时间比较；
  - 请求日志中这个参数要脱敏；
  - 这一点需要在 WP08 的 API 中同步实现。

## 2. 导航（`lib/navigation.ts`）

在"工作区"分组中，把"检测任务"（`/jobs`，图标 `ListChecks`）放在"节点池"之后。在"观测与配置"分组中新增三项：

| 名称 | 路径 | 图标 |
|---|---|---|
| 数据源与检测 | `/intel` | `ShieldCheck` |
| 导出与订阅 | `/exports` | `Share2` |
| 审计日志 | `/audit` | `ScrollText` |

## 3. 页面与改动

### 3.1 节点池（`features/nodes`）

- **多选**：表格第一列改为复选框，支持全选当前页；另有"按当前筛选条件全选"（选中的是筛选条件本身，由后端展开）。
- **批量操作栏**（有选中项时出现）：
  - 探测出口：对应 `kind=egress`；
  - 批量检测 IP：对应 `kind=intel`；
  - 解锁检测：对应 `kind=checks`；
  - 完整检测：对应 `kind=full`；
  - 导出：打开导出对话框，见 §3.6。

  点击后调用 `POST /api/v1/intel/jobs`，scope 为 `node_hashes`，或者在"全选筛选结果"时为 `filter`。成功后弹出通知，带"查看任务"链接。
- **新增列**（可在"列设置"中显示或隐藏，设置保存在 localStorage）：
  - 引擎徽标（singbox 或 mihomo）；
  - `protocol_detail`；
  - 出口 IPv4 和 IPv6，以及 colo；
  - 国家和城市；
  - ASN 和组织；
  - IP 类型；
  - 原生 IP；
  - 纯净度（分数、区间色块、置信度小点）；
  - 判定；
  - 解锁摘要（每个检测一个小图标，悬停显示结果和地区）。
- **新增筛选项**：engine、protocol（下拉选项来自 `/system/capabilities`）、纯净度范围滑块、verdict 多选、ip_type、native、confidence_min、ASN、country，以及"检测项 = 结果"（例如 chatgpt 为 available）。
- **节点详情抽屉**新增"IP 情报"标签页，数据来自 `GET /api/v1/intel/nodes/{hash}` 和 `/intel/ip/{ip}`：
  - 纯净度卡片：分数、区间、置信度、判定、原因码的中文说明；
  - 分项明细表：来源、原始值、洁净分、权重、观测时间；
  - 各数据源证据（可折叠）；
  - 解锁结果列表；
  - 出口 IP 历史；
  - 按钮："重新检测"（创建单节点 `full` 任务）和"IPPure 复核"（沿用现有的 `IPPureReview` 组件，接口改为兼容别名）。
- 删除旧的"出口记录"视图中依赖已删除字段的代码；该视图改为按 IP 聚合展示 `GET /quality/assessments`（兼容接口）或新的 `/intel` 列表。

### 3.2 检测任务（新建 `features/jobs`）

- **列表页**：状态、类型、范围摘要、进度条（done/failed/skipped/total）、`pending_online_lookups`、创建者（手动、订阅自动、定时刷新）、创建时间、耗时。支持按状态筛选。
- **新建任务对话框**：
  - 类型：egress、intel、checks、full；
  - 范围：全部节点、按订阅（多选）、按平台（多选）、按当前节点筛选（从节点页带入）；
  - 数据源：多选，默认全部已启用；
  - 检测项：多选，默认全部已启用；
  - `force` 开关。
- **详情页**：
  - 实时进度（SSE）；
  - 取消和重试失败项两个按钮；
  - 失败项列表（节点名、步骤、错误码、错误信息），点击可跳转到节点详情。

### 3.3 数据源与检测（新建 `features/intel`）

- **数据源卡片**：每个数据源一张，数据来自 `GET /api/v1/intel/providers`。
  - 显示：名称、类型（离线、在线、经节点）、启用开关、Key（只写输入框，显示"已设置/未设置"，可清除）、每日额度、QPS、TTL、今日用量进度、状态（正常、暂停、限流至某时间）、排队数、失败数、条款提示（`Terms`）。
  - 操作："测试"（提示会消耗 1 次额度）和"恢复"（解除暂停）。
  - IPPure 卡片固定显示黄色提示："请自行确认 IPPure 条款；默认每分钟 1 次"。
- **离线库状态**：各 mmdb 的版本和更新时间，外加"立即更新"按钮（沿用现有的 GeoIP 页面，把入口合并到这里；原 `/resources` 路由保留并重定向到这里）。
- **检测规则表**：来自 `GET /api/v1/intel/checks`。显示 id、名称、类别、版本、来源、校准日期、启用开关。
- **总览**：来自 `GET /api/v1/intel/status`。显示已知 IP 数、已评估数、各判定和各区间的分布（使用 recharts 柱状图）、intel.db 大小、丢弃计数。

### 3.4 订阅管理（`features/subscriptions`）

- **新增字段**：`auto_intel` 开关（默认开）、`user_agent`。
- **"导入预览"**：在保存之前调用 `POST /subscriptions/actions/preview-parse`，展示统计（按引擎、按协议）、前 50 个节点，以及跳过清单和原因（原因码显示中文说明）。
- **解析报告**：订阅详情中新增一个标签页，展示 `GET /subscriptions/{id}/parse-report`。
- **OpenVPN 导入**：新增"导入 OpenVPN"按钮。
  - 可以选择一个或多个 `.ovpn` 文件，文件在浏览器本地读取；
  - 可以统一填写用户名和密码，也可以逐个填写；
  - 前端生成 `prism_openvpn_bundle` JSON，作为本地订阅内容提交；
  - 需要凭证但未填写的配置标红，禁止提交。

### 3.5 平台详情（`features/platforms`）

- **"质量准入"表单**，对应 `quality_policy`：
  - 最低纯净度（数字或滑块）；
  - IP 类型多选；
  - 允许的判定多选；
  - 最低置信度；
  - 仅原生 IP；
  - 必需检测（键值对列表）；
  - 评估最长有效期和出口最长有效期；
  - 未知时的处理方式（排除或允许，默认排除）；
  - 排除 Tor 和排除高风险两个开关（默认开）。

  表单为空时，显示"不做质量准入（与 Resin 相同）"。
- **预览**：调用 `preview-filter`（带上 `quality_policy`），展示 `excluded_by` 各原因的计数。
- **"定时轮换"区块**：启用开关、间隔、"避开上一个出口 IP"开关。
- **租约列表**：每一行新增"轮换"按钮，调用 `POST /leases/{account}/actions/rotate`。
- **单节点"为什么被排除"**：在平台视图的节点列表中，点击节点即调用 `explain` 接口并显示结果。

### 3.6 导出与订阅（新建 `features/exports`）

- **导出对话框**（节点页调用）：
  - 格式：sing-box、mihomo、v2rayN、URI、CSV、JSON；
  - 名称模板：带变量说明，并实时预览前 3 个节点的名称；
  - 只导出健康节点（开关）；
  - 下载后显示"导出 N / 跳过 M"，跳过原因可以展开查看。
- **订阅配置列表**：增删改查。
  - 创建或轮换令牌后，只显示一次订阅地址，并提供"复制"按钮，同时提示"关闭后无法再次查看"；
  - 列表显示最近访问时间和访问次数。

### 3.7 系统

- **系统配置页**新增"检测"分组，对应 `intel_enabled`、`intel_node_workers`、`intel_check_concurrency_per_check`、`intel_max_running_jobs`、`intel_auto_checks`、`intel_refresh_schedule`。
- **"关于"区块**：展示 `/system/info` 中的版本和 `build_tags`，以及 `/system/capabilities` 中的协议列表（按引擎分组）。
- **审计日志页**：基于 `GET /api/v1/audit-logs`，使用游标分页（`before_id`）。

## 4. 兼容与清理

- 质量相关的旧组件如果依赖已删除的字段，迁移到新接口；如果不再使用，直接删除。
- `QualityStatus` 等旧类型只保留兼容接口仍在使用的那部分。

## 5. 测试与验收

```sh
npm --prefix internal/api/web run lint
npm --prefix internal/api/web run build
npm --prefix internal/api/web run test:config
make backend && npm --prefix internal/api/web run test:e2e   # 使用真实后端
```

- 扩展 `scripts/check-ui-live.mjs`（Playwright）：
  - 登录；
  - 导入一个本地订阅（使用 WP13 的本地测试节点），检查导入预览；
  - 在节点页多选节点并创建 intel 任务，等待任务完成；
  - 在节点详情中看到 IP 情报；
  - 配置平台质量准入，检查预览计数发生变化；
  - 导出 v2rayN 订阅，并用获得的订阅地址 `GET` 到 200；
  - 在深色主题和 375px 宽度下各截图一次。
- 截图输出到 `internal/api/web/test-results/`，该目录已被 gitignore。

---

# WP13 · 端到端测试、文档与发布

**前置**：全部 WP。**目标**：

- 用进程内的真实协议服务端做离线端到端验证；
- 补齐文档；
- 完成发布流程；
- 逐项核对用户的最终目标。

## 1. 离线协议端到端（`internal/e2e/protocols_test.go`，构建标签与完整版一致）

### 1.1 本地服务端

在测试进程内用 `box.New` 启动一个 sing-box 实例作为"服务端"，inbound 全部监听在 127.0.0.1 的随机端口上。同时用 `httptest.NewServer` 起一个 HTTP 目标服务（返回固定正文和请求来源信息）。

| 协议 | 服务端 inbound 配置要点 |
|---|---|
| shadowsocks（aes-128-gcm、2022-blake3-aes-128-gcm） | `type: shadowsocks` |
| vmess（tcp、ws） | `type: vmess`，ws 走 `transport` |
| vless（tcp、reality 需要 `with_reality_server`，可选） | 默认只测 tcp 和 tls（自签证书）；reality 不作要求 |
| trojan | tls 使用自签证书，客户端设 `insecure` |
| hysteria2、tuic | `with_quic`，自签证书 |
| anytls | 自签证书 |
| shadowtls v3 加 ss 串接 | `type: shadowtls`，`handshake` 指向本地 `httptest.NewTLSServer`；后端接 ss inbound |
| socks、http | `type: socks` 和 `type: http` |
| wireguard | 服务端是一个 wireguard endpoint（带 `listen_port`），客户端节点也是 endpoint；目标地址通过服务端的出站访问 |
| openvpn | `openvpn-server` endpoint（`with_openvpn`），证书用测试中生成的 CA、服务端证书和客户端证书；客户端通过 `.ovpn` 夹具导入 |

### 1.2 流程

1. 生成本地订阅内容：分别用 sing-box JSON、Clash YAML、分享链接三种格式描述上述节点，OpenVPN 用 `.ovpn`。
2. 启动 Prism（在测试中构造 app，使用临时目录）。
3. 调用 API 创建本地订阅，等待节点就绪（`has_outbound=true`）。
4. 对每个节点，分别通过 Prism 的 HTTP 正向代理和 SOCKS5 访问目标服务，断言返回 200 和固定正文。平台用正则 `^<订阅名>/<节点名>$` 精确指定节点。
5. 断言解析报告中没有意外跳过的节点。

### 1.3 mihomo 兜底的端到端（`with_mihomo`）

- 由于 sing-box 没有 ssr 和 mieru 的服务端，这两种协议**不做端到端验证**，只做构建测试（WP07）。
- 数据通路改为用 mihomo 的 `socks5` 节点指向本地 SOCKS 服务端来验证：测试中直接调用 `buildMihomo`，因为 socks5 不在兜底列表中。

### 1.4 检测的端到端

- 用伪造的数据源和伪造的检测目标跑完整的 `full` 任务。数据源的 `baseURL` 和检测规则的 URL 通过**构造函数注入**，只在测试代码中使用；**不要**为此新增环境变量或配置项。
- 断言 intel.db 中有证据、检测结果和评估。
- 断言平台的质量准入生效：低分节点被排除。
- 重启后结果仍在。

## 2. 性能与容量冒烟（`go test -run Capacity -tags ...`，不进入 `make verify`，只在 CI 的 nightly job 中运行）

- 恢复历史中的 `router_capacity_test`、`platform_capacity_test`、`pool_capacity_test`，使用 stub builder，规模为 10 万个节点。记录以下指标：
  - 导入耗时；
  - 内存占用；
  - P2C 选路的 P99 延迟。
- intel：模拟 10 万个节点、4 万个 IP 的评估，从投影全量加载的耗时要求 < 3 秒；10 万个 job_items 的领取吞吐要求 ≥ 2000 条/秒（伪造数据源、无网络）。
- 结果写入 `docs/PERFORMANCE.md`，注明机器配置。**不写没有实测过的数字。**

## 3. 文档（全部使用中英双语，或中文加英文摘要）

| 文件 | 内容 |
|---|---|
| `README.md` | 重写：特性、快速开始（Docker 和二进制两种方式）、端口说明（2260）、接入方式（沿用 Resin 的写法，并加上 `X-Prism-Account`/`X-Resin-Account`）、许可证 GPL-3.0-or-later、文档索引 |
| `docs/PROTOCOLS.md` | 协议支持矩阵：由 `TestProtocolMatrix` 的用例表生成，或至少与它保持一致。包括：引擎划分规则（WP07 判定表）；各格式的导入支持和导出支持；不支持的协议及原因（Naive、Tor、Tailscale、XHTTP 在精简版中）；mihomo 节点使用系统 DNS 的限制 |
| `docs/INTEL.md` | 数据源清单（额度、条款、是否经节点）、纯净度算法（公式、权重、判定表）、检测规则的编写方法、IPPure 条款提示、如何关闭自动检测 |
| `docs/MIGRATION_FROM_RESIN.md` | 偏差清单 X1–X6、`prism import-resin` 的用法、环境变量对照表、Resin 功能对照清单与测试名 |
| `docs/deployment.md` | 使用 `prism init`、systemd（与 WP04 的 `deploy.sh` 一致）、Docker、反向代理加 TLS 的示例、`PRISM_ADMIN_LISTEN` 的用法 |
| `docs/backup-restore.md` | `prism backup` 和 `prism restore` 的用法 |
| `docs/SECURITY.md` | 见 WP04 §4.12 |
| `docs/API.md` | 按模块列出全部 `/api/v1` 接口（路径、方法、请求、响应示例）；可以只提供 OpenAPI 3.1 的 `docs/openapi.yaml`，二选一 |
| `THIRD_PARTY_NOTICES.md` | sing-box 1.14.0、mihomo 1.19.31、DB-IP（CC BY 4.0，需要署名）、MaxMind GeoLite2（按其 EULA）等 |

## 4. 发布

- 打 tag `v3.0.0-rc.1` 触发 `release.yml`：产出 5 个平台的完整版，以及 linux 的 lite 版和 Docker 镜像。
- 编写发布说明 `docs/release-notes/v3.0.0.md`：新增功能、偏差清单、升级步骤（从 Prism 旧版本升级，或从 Resin 迁移）。

## 5. 最终验收清单（对应用户目标，全部勾选才算完成）

- [ ] `make verify` 通过；CI 为绿色；上游 Resin 的 93 个测试文件全部移植并通过（偏差处有注释）。
- [ ] 按 README 从零部署（Docker 或二进制）可以成功启动；UI 能登录；HTTP 代理、SOCKS5、反代三种方式都能访问目标。
- [ ] 用 Resin 的数据目录执行 `prism import-resin` 后，平台、订阅、租约齐全，旧客户端使用 `X-Resin-Account` 时粘性会话依然生效。
- [ ] WireGuard、OpenVPN、ShadowTLS 串接、Snell v4、TUIC、Hysteria、AnyTLS、SSH 节点都能导入，并在离线端到端测试中连通。
- [ ] SSR、Mieru、VLESS-XHTTP、VLESS 加密、Snell v3 节点在完整版中由 mihomo 构建成功；在精简版中出现在解析报告里，原因为 `ENGINE_NOT_BUILT`。
- [ ] 导入订阅后自动生成 intel 任务；每个节点的出口 v4/v6、ASN、城市、IP 类型、纯净度、判定都写入 intel.db，重启后不丢；任务中途重启后能续跑。
- [ ] 手动批量检测（按订阅、平台、筛选条件、勾选节点）和解锁检测可用，进度实时刷新。
- [ ] 平台质量准入生效（fail-closed），`explain` 能说明每个节点被排除的原因。
- [ ] 能导出 sing-box、mihomo、v2rayN 格式，并被对应客户端识别；订阅链接可用、可停用、可轮换令牌。
- [ ] 文档与实际行为一致，不包含未实现的声明。
