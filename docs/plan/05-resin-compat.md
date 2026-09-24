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
