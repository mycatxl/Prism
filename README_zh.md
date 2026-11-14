[English](README.md) | [简体中文](README_zh.md)

# Prism

**本次发布版本：v0.5.0** · 容器镜像：`ghcr.io/mycatxl/prism:0.5.0`

Prism 是集单端口代理路由与多租户管理于一体的服务。它统一管理上游节点、平台路由和账号粘性租约，提供 HTTP/HTTPS 转发、SOCKS5 及反向代理接入，并内置面向管理员和租户的 Web 控制台。

## 功能特性

- **多租户管理：**管理员维护用户、套餐、订阅和密钥；租户查看自己的套餐、密钥、用量和租约。权限边界由服务端执行。
- **AccessKey 鉴权：**每个代理密钥归属于一个用户，可限制平台、来源 IP 和有效期。完整凭据仅在创建时展示一次，存储中只保留使用 pepper 保护的验证哈希。浏览器会话采用 HttpOnly Cookie、CSRF 防护及可选 TOTP 二次验证。
- **居中弹窗 WebUI：**支持中文和英文界面，提供独立的管理员工作区与租户自助工作区。创建、编辑、详情查看和操作确认使用居中弹窗。
- **清晰的节点指标：**分别统计节点库存、健康节点、去重后的健康出口 IP 及各平台实际可路由节点。订阅刷新诊断、探针计数与代理业务请求、流量指标分别记录。
- **有证据的纯净度：**仅使用与当前出口 IPv4 匹配的有效 IPPure 证据，按 `纯净度 = 100 - fraudScore` 计算。其他供应商提供网络事实和风险标记。缺失、失败或过期的评分保持“未评分”并说明原因；真实测得的零分仍是有效评分。
- **粘性路由与轮换：**租约按平台、用户和账号隔离。请求续期空闲期限，租约总寿命最多七天。故障切换可保留原出口 IP；手动轮换释放租约，由下一次请求重新分配。
- **离线管理员恢复：**停止服务后，`recover-admin` 新建管理员并写入审计记录，保留既有账号、二次验证、订阅和用量。

Default 平台不设置最低纯净分，但仍要求当前有效评分及其他路由检查通过。节点池的健康数量不代表所有节点都满足某个平台的准入策略。订阅启用状态和探测结果仍会影响节点健康与路由。

## 快速开始

### 本地构建

需要 Go **1.27.0+**、Node.js **22.12.0+**、npm 和 make。构建过程会安装前端依赖、打包控制台，并将其嵌入 Go 二进制。

```bash
make build VERSION=v0.5.0
./bin/prism init --admin-username admin
./bin/prism run
```

打开 `http://127.0.0.1:2260/ui/`。初始化会创建首个管理员、数据库、密钥 pepper 和私有 `.env`。管理员随机密码仅在本地终端展示一次，请妥善保存。自助注册默认关闭。`init` 仅用于全新安装；数据库已有用户时会拒绝初始化。

### Docker Compose

需要 Docker 和 Compose。仅在**全新部署**时，复制示例并创建私有的宿主机 `.env`（以下命令使用 OpenSSL 生成必需的旧版配置 token）：

```bash
cp docker-compose.yml.example docker-compose.yml
(umask 077; set -C; printf 'PRISM_PROXY_TOKEN=%s\n' "$(openssl rand -hex 32)" > .env)
```

选择镜像来源：

- **从当前源码构建：**保留 `build:`，执行 `docker compose build`。
- **使用已发布镜像：**删除整个 `build:` 配置块，保留 `image: ghcr.io/mycatxl/prism:0.5.0`，待该版本发布后执行 `docker compose pull`。

随后在共用的状态卷中初始化一次，再启动服务：

```bash
docker compose run --rm prism prism init --dir /tmp --admin-username admin
docker compose up -d
```

保存生成的管理员密码，在 `http://127.0.0.1:2260/ui/` 登录。容器入口脚本准备目录并以 `prism` 用户运行，不会自动创建管理员。初始化容器中的 `/tmp/.env` 是临时文件，运行服务所需的配置 token 由宿主机 `.env` 提供。启动校验仍要求此 token，但它不能用于代理流量鉴权。

示例将宿主机 2260 端口绑定到回环地址，并通过命名卷保存状态、缓存和日志。Git 发布标签为 `v0.5.0`，对应容器标签为 `0.5.0`，不带 `v` 前缀。

### 准备代理接入

1. 导入上游节点，等待当前出口、正延迟、有效评分及适用的隐私检查完成。
2. 创建允许目标平台的套餐，为用户分配订阅。邀请注册可授予邀请码关联的套餐；仅开放注册不会授予套餐。
3. 在用户的密钥页或管理员的用户管理中签发 **proxy 范围的 AccessKey**，保存创建时仅展示一次的完整 `pk_<id>_<secret>` 凭据。

## 协议与路由

默认监听器在同一端口提供 HTTP 正向代理、通过 CONNECT 建立的 HTTPS 隧道、带鉴权的 SOCKS5 TCP CONNECT、HTTP/HTTPS 反向转发、API 和 `/ui/`。

| 接入方式 | 客户端配置 |
| --- | --- |
| HTTP 代理 / CONNECT | Basic 代理鉴权：用户名为 `Platform.Account`，密码为完整代理 AccessKey |
| SOCKS5 | 用户名与密码鉴权：用户名为 `Platform.Account`，密码为完整代理 AccessKey |
| 反向代理 | 在 Prism 监听地址使用路径 `/<ACCESS_KEY>/Platform.Account/https/example.com/path` |

`Platform` 选择有权限使用的路由平台；`Account` 标识该租户在此平台内的粘性租约。账号为空时可随机路由，反向代理还受平台的账号提取规则约束。不同租户的同名账号不会共享租约。接入还须满足账号和密钥启用、有效期、平台权限、IP 白名单及额度要求。流量额度由同一用户的全部密钥共享；当前请求速率、并发和带宽限制按密钥执行。

SOCKS5 仅支持 TCP CONNECT。内嵌 **sing-box** 引擎支持 Shadowsocks、Trojan、VMess、VLESS、AnyTLS、Hysteria/Hysteria2、TUIC 等上游协议，以及 WireGuard、OpenVPN 和 OpenConnect 接入节点。可导入 Clash、sing-box、Surge、分享链接、Base64 订阅和 OpenVPN 配置。能够识别导入格式不代表可以运行或符合隐私准入策略，请以能力报告和导入结果为准。本版未构建 Mihomo 回退协议和 VLESS xhttp。

## 部署架构

```text
浏览器 / API / 支付回调             HTTP CONNECT / SOCKS5 客户端
           | HTTPS                            | TCP
     HTTP 反代 / Tunnel                 直连或保留来源的 L4
           |                                  |
     回环管理监听器                        公网数据接入点
           |                                  |
      WebUI + API                    鉴权 -> 粘性租约 -> 上游
           +------------ 同一个 Prism 进程 -----+
                       SQLite 状态/缓存/情报
```

HTTP 反代或普通 Cloudflare Tunnel 公开主机名用于管理 HTTP。HTTP CONNECT 与 SOCKS5 客户端需要直达 TCP 接入点或使用适合的 L4 路径。当前 Prism 不解析 PROXY protocol v1/v2；普通 L4 前置若替换 TCP 来源，也会丢失客户归因。可信 XFF、端口策略和防火墙示例见[入站指南](docs/INGRESS.md)。

每个状态目录由一个 Prism 进程独占。SQLite 保存账号、订阅、用量和审计数据，并配有缓存库和情报库；密钥快照和连接注册表保存在进程内。目录锁阻止两个进程同时打开同一状态目录。v0.5.0 不提供共享状态集群、跨实例撤销或配额同步。

额外接入点可按需启用代理协议。`PRISM_ADMIN_LISTEN=host:port` 增加仅提供 `/ui/`、`/api/` 和 `/healthz` 的管理监听器；主监听器仍提供管理路由。

单机设计点为 10,000 个注册用户和客户侧双向合计 1 Gbps。[容量规划](docs/CAPACITY.md) 给出起测硬件、并发与出口 IP 预算、进程内基准证据，以及形成该硬件承载声明所需的同步记账实测。

## 配置与安全

- 使用 `PRISM_*` 环境变量。[`.env.example`](.env.example) 和 [`deploy/backend.env.example`](deploy/backend.env.example) 列出部署配置。初始化和运行必须使用相同存储路径，生成的 `.env` 应保持私有。
- 通过 `./bin/prism check-config` 检查实际生效配置，敏感值会被遮蔽。控制台支持的运行时设置可免重启修改。
- 在运营设置中配置 Telegram 运维通知，token 不回显，覆盖存储、数据库锁、用量失败和节点池故障；支付网址保存会提示可达性警告。详见[运维指南](docs/OPERATIONS.md)。
- 远程 Web 访问应由反向代理终止 TLS，并将 `PRISM_TRUSTED_PROXIES` 限定为该代理。浏览器会话要求 HTTPS 或本地回环 HTTP；监听器仅向预期客户端开放。
- 自动化可使用 admin 范围的 AccessKey 读取管理接口并执行允许的操作。签发凭据、修改支付配置和隐私准入设置要求管理员浏览器会话及 CSRF 校验。管理员密钥和浏览器会话不能替代代理密钥。
- 默认网络防护在本地直连路径拒绝私网、元数据地址及常见 SMTP 端口。节点准入另行检查禁止的节点地址、已知自有资产出口及配置的隐私规则。
- 可选的匿名性 echo 测量默认关闭，未测量的路径不作匿名性保证。确认身份泄露的节点永久删除；普通健康失败继续复探，低纯净度按平台策略分类处理。

## 升级与恢复

升级到 v0.5.0 前，应协调备份 **state.db、cache.db、intel.db 和 pepper**，并单独保存私有 `.env`、服务或容器配置及反向代理配置。详见[恢复指南](docs/RECOVERY.md)。保留现有存储卷和配置；升级时不执行 `init`，也不删除卷。

使用已发布镜像的部署，将镜像标签改为 `0.5.0` 后执行：

```bash
docker compose pull
docker compose up -d
```

源码部署执行 `make build VERSION=v0.5.0`，再按现有部署流程替换服务二进制。

唯一管理员丢失访问权时，先停止该部署并备份。在受保护的本地 Bash 终端中，使用该安装的实际状态和缓存路径、事件编号，新建恢复管理员：

```bash
read -r -s -p '请输入恢复管理员的新密码：' PRISM_RECOVERY_PASSWORD
printf '\n'
printf '%s\n' "$PRISM_RECOVERY_PASSWORD" | ./bin/prism recover-admin \
  --state-dir /srv/prism/state --cache-dir /srv/prism/cache \
  --username recovery-admin --reason INC-1234 \
  --password-stdin --confirm-offline
unset PRISM_RECOVERY_PASSWORD
```

选择未使用的用户名和至少 12 位的随机密码，输入时关闭 shell 命令跟踪。命令要求已有的私有数据库和 pepper 文件，并拒绝对运行中的安装执行。既有密码、TOTP 因子和防重放水位保持不变。本地登录后启用新的二次验证，再停用失联管理员。恢复指南还包含时钟校正、完整恢复、未结算流量预留和证书续期。

## 构建与验证

```bash
make build VERSION=v0.5.0  # 前端包与服务二进制
make verify                # vet、测试、竞态、覆盖率、协议与前端检查
make test-ui               # Chromium 界面检查，须先安装 Playwright Chromium
make smoke                 # 一次性本地测试环境中的二进制端到端检查
make billing               # 本地支付模拟环境中的真实 HTTP 商业流程
```

后三个命令使用已构建的二进制。运行 `make test-ui` 前，通过 `npx --prefix internal/api/web playwright install --with-deps chromium` 安装浏览器。

离线检查不能证明真实出口或支付验收通过。[`docs/LIVE_ACCEPTANCE.md`](docs/LIVE_ACCEPTANCE.md) 说明显式三协议验证流程，其命令行工具默认返回 `NOT_RUN`。`/healthz` 仅报告进程存活。生产验收还要求核实 TLS 与时钟、备份恢复、可售库存、供应商额度，以及使用持久记账路径在目标硬件上测得的容量；启用支付时须另行完成真实验收。

## 仓库结构

```text
cmd/prism/           进程入口、初始化与离线恢复
internal/api/        HTTP 接口与内嵌 Web 控制台
internal/node/       节点文档、健康状态与引擎能力
internal/proxy/      正向代理、CONNECT、SOCKS5 与反向代理路径
internal/routing/    平台路由、粘性租约与轮换
internal/intel/      测量证据与节点评估
internal/state/      SQLite 状态、缓存与迁移
deploy/ docker/      部署配置参考与容器辅助脚本
docs/                运维与验收指南
```

## 文档

- [`docs/CAPACITY.md`](docs/CAPACITY.md)：单机规格、出口 IP 供给、本地基准与持久记账容量边界。
- [`docs/INGRESS.md`](docs/INGRESS.md)：管理 Tunnel、公网数据接入点、可信来源 IP 与 PROXY protocol 边界。
- [`docs/OPERATIONS.md`](docs/OPERATIONS.md)：Telegram 告警、支付网址检查和节点筛选。
- [`docs/MULTI_TENANT.md`](docs/MULTI_TENANT.md)：角色、注册、套餐、密钥、用量、计费及运维边界（中文）。
- [`docs/RECOVERY.md`](docs/RECOVERY.md)：受保护的离线管理员恢复、备份、时钟校正、用量核账和证书续期（英文）。
- [`docs/LIVE_ACCEPTANCE.md`](docs/LIVE_ACCEPTANCE.md)：获授权的 HTTP/SOCKS5/反向代理真实验收与证据记录（英文）。

## 参与贡献

提交拉取请求前运行 `make verify`、`make test-ui` 和 `make smoke`。保持改动范围明确，行为变化时更新相关运维文档。发布内容包含源码、测试、构建配置、两版 README、许可证及运维文档；凭据、私钥、`.env`、数据库、日志、生成的前端包和临时审计产物不进入提交。

## 许可证

GPL-3.0-or-later。源自 Resin 的代码保留 [`LICENSES/Resin-MIT.txt`](LICENSES/Resin-MIT.txt) 中的 MIT 声明；来源说明见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。
