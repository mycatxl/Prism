# 个人版 API 与管理界面

状态：接口草案，优先服务个人管理员和自动化脚本，不包含用户注册、套餐、支付和多租户。

## 1. 入口

- 管理 API：`/api/v1/*`，默认只绑定 loopback，使用管理 Bearer token 或本地会话。
- 代理入口：HTTP 正向、HTTP CONNECT、SOCKS5 CONNECT、反向代理，使用代理 token；代理 token 不能访问管理 API。
- `/healthz`：仅返回存活状态，不需要管理 token，不泄漏版本、路径和节点数量。
- 写接口：JSON object、未知字段拒绝、请求体有上限；PATCH 不接受 `null` 删除字段。

成功响应返回资源或 action 状态，错误统一为 `{error:{code,message,request_id}}`。动作支持 `Idempotency-Key`，查询和列表使用 keyset cursor。

## 2. 首版资源

| 资源 | 端点示例 | 说明 |
|---|---|---|
| 系统 | `GET /system/info`、`GET/PATCH /system/config` | 版本、监听、检测和日志配置 |
| 凭证 | `GET/POST/DELETE /credentials` | 管理/代理/动作 token 摘要、范围、轮换 |
| 订阅 | `GET/POST/PATCH/DELETE /subscriptions` | URL、本地内容、刷新周期、启停、替换/合并模式 |
| 导入 | `POST /imports`、`GET /imports/{id}` | 文件/文本/URL，逐条解析结果和错误原因 |
| 平台 | `GET/POST/PATCH/DELETE /platforms` | Resin 标签规则、地区、质量、分组和租约配置 |
| 节点 | `GET /nodes`、`GET /nodes/{hash}` | 摘要、来源、健康、出口、质量、排除原因 |
| 检测 | `POST /nodes/{hash}/actions/probe`、`GET /inspection-tasks` | 单节点、出口、质量和目标检测 |
| 质量 | `GET /quality/ip/{ip}`、`GET /quality/assessments` | 证据、来源、区间、有效期和冲突 |
| 出口组 | `GET /egress-groups` | IP、成员线路、组内优先级和稳定性 |
| 租约 | `GET/DELETE /platforms/{id}/leases` | 查看、释放粘性会话 |
| 切换 | `POST /platforms/{id}/leases/{account}/actions/rotate` | 排除旧 IP、CAS generation、keep/close 旧连接 |
| 日志 | `GET /request-logs`、`GET /inspection-logs`、`GET /audit-events` | 分页、过滤和脱敏详情 |
| 备份 | `POST /backups`、`POST /restore` | 管理员动作，恢复前检查并停止 listener |

## 3. 平台配置

平台规则字段：`regex_filters`、`region_filters`、`subscription_ids` 或手动分类范围、质量 profile、IP 类型、分数下限/区间、名单要求、证据最大年龄、未知/冲突动作、分组策略、优先级、sticky TTL 和是否允许调试节点。

保存前返回预览：可见节点、独立出口 IP、合格节点、排除原因、样例标签和配置版本。保存后异步更新视图，数据面在版本不一致时执行最终检查，不按旧宽松规则放行。

## 4. 导入与协议兼容

支持 sing-box JSON、Clash JSON/YAML、URI 行、纯文本 HTTP/SOCKS 行和 base64 包裹形式。解析器必须返回每条输入的成功、重复、缺字段、不支持协议和安全拒绝原因；不得静默丢弃。

协议矩阵单独展示：节点出站协议、传输层参数、构建标签和客户端入站方式不是同一个能力。当前基线继承 Resin 的 HTTP/SOCKS、SS、VMess、VLESS、Trojan、Hysteria/Hysteria2、TUIC、WireGuard、AnyTLS、SSH 等适配，逐项以构建和真实节点测试确认。

个人版客户端入口首期支持 HTTP forward、HTTP CONNECT、SOCKS5 CONNECT、HTTP reverse/WebSocket。UDP ASSOCIATE、HTTP/3 入站、任意自定义协议不列为已支持；解析成功不代表连接能力已验证。

## 5. 会话和换 IP

客户端通过 `Platform.Account` 或接入点固定会话名获得粘性租约。Account 是会话标识，不是管理员权限。一个会话对应一个出口组；同 IP 线路优先切换只改变节点路径，出口切换才改变外部 IP。

```http
POST /api/v1/platforms/{platform_id}/leases/{account}/actions/rotate
Authorization: Bearer <action-token>
Idempotency-Key: <operation-id>
```

```json
{"require_different_ip":true,"expected_generation":17,"existing_connections":"keep"}
```

无替代出口返回 `409 NO_ALTERNATE_EGRESS` 并保留原租约。`keep` 只影响新连接，原 TCP 隧道自然结束；`close` 是明确中断动作。GET 链接只展示状态，不能触发切换。

## 6. 页面边界

首版页面按工作流组织：总览、订阅/导入、节点池、出口组、平台、质量检测、请求日志、审计、系统设置。节点详情显示“为什么合格/排除”，平台页面显示标签和质量规则预览，检测页面显示队列和来源限流。

页面不展示节点 secret、完整订阅 token、认证头和未脱敏原始响应。导出是单独动作，确认后生成短期文件。高频图表读取 metrics，不查询原始日志全表。

## 7. 错误码

除通用 `INVALID_ARGUMENT`、`UNAUTHORIZED`、`NOT_FOUND`、`CONFLICT`、`INTERNAL` 外，首版定义：`NO_AVAILABLE_NODES`、`NO_ELIGIBLE_NODE`、`QUALITY_UNKNOWN`、`QUALITY_EXPIRED`、`UPSTREAM_CONNECT_FAILED`、`UPSTREAM_TIMEOUT`、`TARGET_BLOCKED`、`AUTH_REQUIRED`、`AUTH_FAILED`、`NO_ALTERNATE_EGRESS`、`STALE_GENERATION`、`IMPORT_PARTIAL`。

数据面只返回必要错误和 request ID；管理 API 可以返回排除原因和检测状态。错误码不能泄漏其他订阅或平台存在性。
