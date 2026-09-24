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
