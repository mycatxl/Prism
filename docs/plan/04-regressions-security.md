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
