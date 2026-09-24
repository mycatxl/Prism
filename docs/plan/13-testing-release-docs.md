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
