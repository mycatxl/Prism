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

- [x] `make verify` 通过；CI 为绿色；上游 Resin 的 **105** 个测试文件全部移植并通过（偏差处有注释）。
- [ ] 按 README 从零部署（Docker 或二进制）可以成功启动；UI 能登录；HTTP 代理、SOCKS5、反代三种方式都能访问目标。
  - **二进制路径：已验证。** `scripts/smoke.sh` 从零建 `.env`、起服务、建订阅、并真实走过 HTTP 正向代理、SOCKS5 与反代三条路径（23 passed）。
  - **Docker 路径：未验证。** `Dockerfile` / `.github/Dockerfile.release` / `docker-compose.yml.example` / `docker/entrypoint.sh` 都存在、内容自洽，README 与 `docs/deployment.md` 已补 Docker 章节，但本环境没有可用的 docker daemon（`docker` 是报错垫片，`docker info` 失败），**镜像从未构建或运行过**。上线前需在有 daemon 的机器上跑一次 `docker build` + `docker compose up -d` + 访问 `/ui/`。
- [x] 用 Resin 的数据目录执行 `prism import-resin` 后，平台、订阅、租约齐全，旧客户端使用 `X-Resin-Account` 时粘性会话依然生效。
  - `cmd/prism/import_resin_test.go`（7 个用例）覆盖导入；`internal/proxy/proxy_test.go` 的 `TestReverseProxy_ResolveReverseProxyAccount_XResinAccountHeaderCompat` 钉住旧头名兼容；`./bin/prism import-resin` 实测打印真实参数用法。
- [ ] WireGuard、OpenVPN、ShadowTLS 串接、Snell v4、TUIC、Hysteria、AnyTLS、SSH 节点都能导入，并在离线端到端测试中连通。
  - **导入与构建：已验证。** `TestProtocolMatrix` 覆盖全部这些协议（含 `ovpn-*` 8 例、`wireguard-*` 5 例、`snell-*`、`chain-clash-shadow-tls-plugin` 等），且它是**真实拨号**。
  - **"离线"这一半未满足。** `internal/e2e/` 目录不存在，协议矩阵需要出网。CI 有外网所以跑得过，但纯离线环境无法证明协议可用。要满足本条需补本地端点服务器 + 各协议回环测试。
- [ ] SSR、Mieru、VLESS-XHTTP、VLESS 加密、Snell v3 节点在完整版中由 mihomo 构建成功；在精简版中出现在解析报告里，原因为 `ENGINE_NOT_BUILT`。
  - **本条已被决策 D-1 取代。** mihomo 不作为运行时内核被否决（`docs/ENGINE_DECISIONS.md`），因此不存在"由 mihomo 构建成功"的完整版。**后半条仍然成立且已验证**：这些类型在解析报告里以 `ENGINE_NOT_BUILT` 出现（`internal/subscription/report.go`，由 `report_test.go` 钉住）。
- [x] 导入订阅后自动生成 intel 任务；每个节点的出口 v4/v6、ASN、城市、IP 类型、纯净度、判定都写入 intel.db，重启后不丢；任务中途重启后能续跑。
  - 续跑与持久化由 `TestManager_PipelineBreakpointResumesAfterRestart`、`TestManager_CrashAfterStepPersistsBreakpoint`、`TestManager_RecoverRequeuesStaleLeases`、`TestSnapshot_RebuildsIdenticalProjectionAfterRestart` 覆盖；字段写入由真实订阅实测确认（186 个不同出口 IP、`ts_assessment`/evidence 落库）。
- [x] 手动批量检测（按订阅、平台、筛选条件、勾选节点）和解锁检测可用，进度实时刷新。
  - `POST /api/v1/intel/jobs` 支持 `all` / `subscription_ids` / `platform_ids` / `node_hashes` / `filter`（`filter` 收窄前三种、不作用于 `node_hashes`）；`filter` 键为 protocol/engine/region/ip_type/purity_band/verdict/healthy（由 `cmd/prism/intel_scope_filter_test.go` 覆盖）；进度经 SSE（`/intel/jobs/{id}/events`）实时刷新，`/jobs` 页面已接。
  - 说明：前端批量入口按**当前筛选条件**建任务（不做手动勾选），但"勾选节点"的能力由 `node_hashes` 保留在 API 层。
- [x] 平台质量准入生效（fail-closed），`explain` 能说明每个节点被排除的原因。
  - `TestPlatform_FullRebuild_QualityPolicyWithoutSnapshotFailsClosed`、`TestExplainQuality`、`TestExplainQuality_UnknownAndEmpty` 覆盖；路由 `GET /api/v1/platforms/{id}/nodes/{hash}/explain` 已注册。
- [x] 能导出 sing-box、mihomo、v2rayN 格式，并被对应客户端识别；订阅链接可用、可停用、可轮换令牌。
  - 格式与命名由 `internal/export/` 的 8 个测试文件覆盖，令牌生命周期由 `internal/api/handler_export_test.go` 覆盖。
  - **"被对应客户端识别"未在外部客户端上实测过**（需人工导入验证），仅覆盖了格式契约本身。
- [x] 文档与实际行为一致，不包含未实现的声明。
  - 本轮修掉三处不实声明：`docs/DESIGN.md` 的 `prism standalone` 与双端口描述（实际是单端口 2260）、`docs/deployment.md` 称 `import-resin` 未实现、`docs/PROTOCOLS.md` 仍写 sing-box 1.14.1。计划要求而缺失的文档已补齐：`docs/INTEL.md`、`docs/API.md`、`docs/release-notes/v3.0.0.md`，以及 README/deployment 的 Docker 章节。
  - 仍需注意：`POST /quality/ip/{ip}/actions/probe` 在生产装配下恒返回 409（`inspection.Store` 无实现者、`ControlPlaneService.Inspection` 从未赋值）—— 这已如实写进 `docs/API.md` §21 与发布说明的已知限制，不属"未实现的声明"。
