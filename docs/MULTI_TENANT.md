---
id: prism-multi-tenant
type: module-design
status: active
title: Prism 多租户运营层
tags: [prism, multi-tenant, operations]
parent: prism-architecture
references: [prism-goal]
---

# 多租户运维指南

Prism 从一个「单人自用」的代理池长成了一个多租户控制台：一个管理员维护节点与平台，若干租户只看见自己的套餐、密钥和用量。本文是这一层的运维说明——谁能做什么、怎么开号、怎么下发流量、以及每个开关落在哪个环境变量上。

## 1. 两类角色与边界

| 角色 | 能到达的接口 | 能看到的页面 |
| --- | --- | --- |
| 管理员 `admin` | 全部 `/api/v1/*` | 全部管理台页面 + 账户安全 |
| 用户 `user` | 仅 `/api/v1/auth/*` 与 `/api/v1/me/*` | 自助面板 + 账户安全 |

边界由 `internal/api/rolegate.go` 的 `requireAdmin` 中间件执行：它挂在身份中间件内侧，因此到达它时身份已经确定——**没有身份给 401，身份合法但角色不对给 403**，控制台可以据此区分「请重新登录」和「你没有权限」。除自助路径前缀（`/api/v1/auth`、`/api/v1/me`）与公开的登录/注册路由外，所有路由默认只有管理员可达：**新加的管理接口天然是私有的，不需要记得去保护它**。

前端是同一份包：`lib/navigation.ts` 按角色过滤导航，`features/auth/RequireAdmin.tsx` 把走旧链接的租户送回自己的页面，`AppShell` 对租户会话不发起任何运维查询（实例信息、环境配置都是管理员接口）。**前端的过滤只是体面，控制权始终在服务端。**

## 2. 会话与凭据

- 控制台登录用用户名 + 密码（可选 TOTP），会话放在 HttpOnly cookie `prism_session` 里；第二个 cookie `prism_csrf` 是双提交令牌，任何写操作都必须把它回填到 `X-Prism-CSRF` 头。cookie 只在 HTTPS 下、或 loopback 上的明文 HTTP 下签发（`HTTPS_REQUIRED` 之外的地址一律拒绝，避免会话被同链路的人读走）。Prism 自己没有 TLS 监听，所以反代终止 TLS 的部署要把反代地址写进 `PRISM_TRUSTED_PROXIES`：只有来自受信代理的连接，Prism 才采信 `X-Forwarded-Proto: https`（取最右侧的值）。没列进去的话，这条请求在 Prism 眼里就是明文 HTTP，远程面板会因为拿不到安全 cookie 而登录失败。
- 自动化（脚本、CI）不要复用共享令牌：**登录一次、带着 cookie 与 CSRF 头工作**。`scripts/smoke.sh` 与 `.github/workflows/ci.yml` 就是这么做的。
- 数据面凭据是访问密钥（`pk_…`）：明文只在创建响应里出现一次，库里只留哈希，内存里的 `keys.Store` 持有密钥状态（含所有者状态与套餐快照）。
- 初始管理员由 `prism init` 创建：用户名默认 `admin`，密码来自 `--admin-password`，不传就随机生成、**只在标准输出打印一次**。它不会覆盖已有部署——库里已有用户就直接拒绝初始化（`--force` 只覆盖生成的 `.env`，旧文件先挪成 `.env.bak.<时间戳>`），要重来就备份并删掉数据目录。旧文档提到的 `ADMIN_PASSWORD` 并不是 Prism 读取的环境变量（仓库里的脚本只是拿这个 shell 变量给 `--admin-password` 传值），`PRISM_ADMIN_TOKEN` 也不存在：控制台会话走 cookie，自动化走管理员访问密钥。

## 3. 开号：注册模式

`PRISM_REGISTRATION` 决定自助注册的门是关、是邀请制、还是敞开：

| 值 | 行为 |
| --- | --- |
| 缺省 / 空 / `off` | 关闭。`POST /api/v1/auth/register` 一律 `403 REGISTRATION_CLOSED` |
| `invite` | 必须带邀请码；兑换成功即建号、按邀请码绑定的套餐授予订阅，并**自动登录**（201 `authenticated:true` + 会话 cookie） |
| `open` | 不需要邀请码，建号成功但**不授予任何套餐**（没有码，就没有可继承的授予） |

空的 `PRISM_REGISTRATION` 按关闭处理——读不到策略时不开门。密码至少 12 位，用户名 3–64 字符。

**面板可以覆盖这个部署策略。**管理台「系统设置 → 账户注册」写的是运行时配置 `registration_mode`（`system_config` 表，`PATCH /api/v1/system/config`）：非空且合法时优先于 `PRISM_REGISTRATION`，保存即生效、无需重启，注册接口和登录页的注册入口读的是同一份解析结果。留空表示不覆盖、回到环境变量——这是把决定权交还部署侧的唯一方式，所以面板上的「改回跟随部署环境」提交的是空字符串，而不是当前看到的值。旧部署升级后该字段为空，行为与升级前完全一致。

邀请码在管理台的「邀请码管理」页创建：可绑定套餐、限制使用次数、设置有效期与备注。兑换是**一个条件 UPDATE**：状态、有效期、剩余次数三条一起判，因此两个并发请求不会各自拿到最后一次使用机会。若建号失败，事务回滚，次数不会被白吃。

拒绝语义是刻意统一的：**未知、已撤销、已过期、已用尽四种情况返回同一句话**，否则这个端点就成了枚举有效邀请码的工具。

## 4. 套餐与订阅

套餐字段（管理台「套餐管理」页，`POST/PATCH /api/v1/admin/plans`）：

| 字段 | 含义 |
| --- | --- |
| `name` | 唯一名称 |
| `platforms` | 该套餐可用的平台列表 |
| `ip_tiers` | 节点质量档位，格式 `ip_type:purity_band`，与质量模型共用词汇表 |
| `traffic_bytes` | 计费周期内的流量额度，0 表示不限 |
| `period` | 计费周期（默认 `30d`） |
| `max_concurrent` / `max_rps` / `max_bandwidth_bps` | 并发、速率、带宽上限 |
| `max_keys` | 用户可持有的活跃密钥数（0 表示不限） |
| `duration_days` | 分配订阅时的默认天数 |
| `status` | `active` / `disabled` |

**订阅持有的是套餐快照，而不是套餐的引用。** 因此改套餐不会悄悄改掉存量订阅的承诺；只有带上 `apply_to_existing: true` 时才把新参数推给现有订阅。订阅状态为 `suspended` 或 `expired` 时，快照状态优先——用户不会因为套餐本身是 active 就继续免费用。

分配订阅走 `POST /api/v1/admin/subscriptions`（`user_id` + `plan_id` + 可选 `duration_days`）；给同一用户再分配一次会先结束上一个活跃订阅，并把新快照重新发布到数据面。

订阅到期不只靠读取时判断：后台收口每 5 分钟跑一趟（`StartSubscriptionExpirySweeper`，间隔 `SubscriptionSweepInterval`），把 `expires_at_ns` 已过、状态还写着 `active` 的行**一条 UPDATE** 收成 `expired`。访问判定本来就不依赖这个状态（密钥状态、套餐快照、数据面都拿 `expires_at_ns` 与时钟比），收口是为了让「谁还挂着套餐」的统计口径与数据面一致——活跃订阅列表、控制台概览的活跃人数与「即将到期」人数、按套餐分布读的都是这个状态——并让周期轮转不再给已经结束的订阅续期。`expires_at_ns = 0` 是「永不失效」的哨兵（`000021` 给历史账号写的正是它），`suspended` 的行同样不会被碰。

## 5. 密钥与「停用即刻生效」

- 管理员可以在用户详情页代发密钥；用户也可以在自己的面板里自助创建，数量由套餐的 `max_keys` 约束（超限 409）。
- 每一次影响访问权的写入——停用用户、改套餐、暂停订阅、删除密钥——最后都会走到 `republishUserKeys`：把新的用户状态与套餐快照推给 `keys.Store`。数据面读的是这份内存状态，所以**停用是「现在」，不是「下次请求」**。
- 停用账号会同时结束它的全部会话；删除账号会级联删掉它的密钥，但**保留用量记录**（账单与审计需要它）。

## 6. 用量与保留窗口

用量按日表与小时表记录，再按用户、平台、套餐与订阅周期聚合。四个视图给管理台与运维脚本用：全站概览（`/api/v1/admin/usage/summary`）、按用户（`usage/users`）、按平台（`usage/platforms`）、按套餐（`usage/plans`）、每日趋势（`usage/daily`），以及单用户窗口（`/api/v1/admin/users/{id}/usage?days=N`）。按套餐视图把「有多少账号挂在每个套餐上、他们这个窗口用了多少、当前周期用掉多少」放在同一行；有流量但没有活跃订阅的账号会归到一个 `plan_id` 为空的行里，这样表格的合计仍然等于全站用量。

**重启不重置额度**：内存里的用量聚合活不过进程，所以启动时会按每条活跃订阅的 `period_used_bytes` 把额度播回聚合。播种记的是**水位线**——「这份聚合里已经落盘了多少」——每次冲刷只把真正写进库的字节推高水位线，播种补的是「已落盘 − 已计入」的差额，补完把水位线推到已落盘总量：同一份数据播一次和播两次结果相同，既不会把同一段流量算两遍，也不会让重启变成「又发一份月度额度」。第一次播种读不到库时，补种循环按冲刷节拍重试到补齐为止；周期轮转（`ResetSubscriptionPeriod`）把聚合与水位线一起归零，上个周期的用量不会渗进新周期。

两个保留窗口可在「系统设置」页热改，无需重启：

- `audit_retention_days`，默认 90 天；审计表另有 10 万条的硬上限，与窗口无关。
- `usage_retention_days`，默认 400 天（与出厂行为一致）；它管的是**日表**。小时表固定保留 30 天后汇总成日行，这个 30 天不可配置。

**没有「永久保留」这个取值**：0 或负数表示「用出厂窗口」（90 / 400 天），这正是旧部署里那两个 0 的含义——「没设过」，不是「全都留着」。要留更长的历史就写更大的天数；审计表无论如何只留最近 10 万条。

保留窗口在**每次清理时**重新读取，所以改完不需要重启：审计清理每 24 小时跑一趟、用量清理每小时跑一趟，改动最迟在下一趟清理时生效。10 万条的硬上限不随配置走。

## 7. 质量管理阈值

节点 IP 纯净度的四档阈值（出厂 95 / 90 / 80 / 60）可以在「系统设置」页调整，落到 `purity_bands`。可配置的档位只有 `excellent` / `clean` / `fair` / `mixed` 四个（代码里的 `config.PurityBands`）；`poor` 是评估结果里的标签，不是阈值档位，写成第五个阈值不会有任何效果。作为**筛选值**（平台/套餐的节点选择条件）能用的词汇表更宽：`unknown` / `excellent` / `clean` / `fair` / `mixed` / `poor` / `review`（`internal/platform/model_codec.go` 的 `nodePurityBands`，`review` 是节点列表沿用的旧值），写别的值会在 API 边界被拒，而不是默默筛不出节点。阈值本身写入时必须**严格递减**且 `excellent <= 100`（即 `0 < mixed < fair < clean < excellent <= 100`），非法组合整体回落到出厂值而不是部分生效——半个阈值集比没有阈值更难解释。

## 8. 页面清单

管理台（仅管理员）：

| 页面 | 路径 | 主要接口 |
| --- | --- | --- |
| 用户管理 | `/admin/users` | `GET|POST /api/v1/admin/users` |
| 用户详情 | `/admin/users/:id` | 用户 CRUD、密钥、订阅、单用户用量 |
| 套餐管理 | `/admin/plans` | `GET|POST|PATCH|DELETE /api/v1/admin/plans` |
| 邀请码管理 | `/admin/invites` | `GET|POST|DELETE /api/v1/admin/invites` |
| 用量统计 | `/admin/usage` | `GET /api/v1/admin/usage/*`（含按套餐分布）、`GET /api/v1/admin/alerts` |
| 系统设置 | `/admin/settings` | `GET|PATCH /api/v1/system/config`（注册准入、保留周期、纯净度阈值）、`GET /api/v1/system/config/env`（部署侧注册策略） |

租户自助面板（登录即可）：

| 页面 | 路径 | 说明 |
| --- | --- | --- |
| 概览 | `/me` | 套餐、周期用量、剩余额度、实时并发、活跃会话 |
| 我的密钥 | `/me/keys` | 自助签发与删除，明文只显示一次 |
| 接入指南 | `/me/guide` | 可用平台与由运营提供的接入地址 |

## 9. 滥用防护

一个对外开放的部署会把别人的流量从你的 VPS 送出去，所以默认值按「先拒绝、再解释」来设。

**目的地端口**：`PRISM_BLOCKED_DESTINATION_PORTS` 默认 `25,465,587`（SMTP 家族——被拿来发垃圾邮件的头号入口），逗号分隔，写空值表示不屏蔽任何端口。它与 `PRISM_DIRECT_DENY_PRIVATE` **相互独立**：关掉私网拒绝不会顺带开放发信端口。执行点在本地直连守卫 `internal/proxy/direct_dial_guard.go`：拨号前按目标 `host[:port]` 判一次，socket 层的 `ControlContext` 拿到真实地址再判一次，因此解析器在中途换答案也躲不过；域名与 IP 字面量一视同仁。被拒时错误信息点名端口与那个设置名，方便使用者知道是谁拒绝的、怎么放行。注意这张表是**全局**的，目前还不能按套餐放开：要按套餐细分，需要给套餐模型加字段并加一次迁移。

**内网与元数据地址**：同一守卫由 `PRISM_DIRECT_DENY_PRIVATE` 控制（默认开），拒绝回环、私网、链路本地、CGNAT、保留段与云元数据地址；节点侧另有 `internal/addrpolicy`（`NodeTargetIsForbidden`）。这两层是既有能力，P7 只是复用它，没有另起一套策略。

**用量异常告警**：检测器是 `internal/service/control_plane_alert.go` 的 `ScanUsageAlerts`，由 `StartUsageAlertScanner` 随进程每分钟扫一次。扫描失败只记日志、下个周期重试——一个遇到数据库抖动就退出的告警循环，恰好会在它最该说话的时候沉默。判定按**每条活跃订阅**独立做：把该账号在窗口内落桶的用量加起来（日表与小时表一起读），达到 `比例 × 套餐额度` 就写一条告警；额度为 0（不限量）的套餐永不告警。出厂比例 0.3、窗口 60 分钟，也就是「一小时内用掉三成额度」。

**窗口的粒度要说清楚**：用量行是整小时的桶（小时表按 UTC 自然小时切），窗口按**重叠**取桶——某个小时只要与窗口有交集，这一桶就整个计入，因此窗口起点所在的那个小时会被完整算进来。换句话说 60 分钟窗口精确到小时、不精确到分钟：它看到的是「当前这一小时 + 窗口覆盖到的上一个小时」，而不是「最近 60 分钟整」；跨过整点时按新落桶的用量重新累积，不是把上一小时的量一直带着走。要做「精确最近 N 分钟」需要按请求落账的明细，那是另一件事——这层不假装有它。

**去重与确认**：同一订阅、同一类型、且**尚未确认**的告警只留一条，持续超量不会刷屏；确认之后再越线会重新报一条。告警落在 `alerts` 表（迁移 `000019`），超过保留窗口的记录在下一次扫描时顺手清掉，这张表由写它的那次调用自己兜住上限。

**三个参数**都能在「系统设置」页热改：`alert_usage_ratio`（取值 (0,1]）、`alert_window_minutes`（≥1）、`alert_retention_days`（≥1，出厂 90 天）。「热改」的准确含义是**下一次扫描生效**：扫描器随进程每分钟跑一次、每趟重新读这三个值，所以设置页改完最多一分钟后按新值判定，既不需要重启，也不是当次请求立刻生效。也可以用环境变量在启动时钉住：`PRISM_ALERT_USAGE_RATIO`、`PRISM_ALERT_WINDOW_MINUTES`、`PRISM_ALERT_RETENTION_DAYS`——**带值的变量在每次启动时覆盖库里那条，空值或缺省则沿用库里的值**（部署文件里一行空的 `PRISM_ALERT_USAGE_RATIO=` 不会悄悄顶掉控制台改过的设置）。旧版本留下的 `state.db` 里，这三个字段解码出来是 0，启动时会被补成出厂值：0 会被设置页的校验拒绝，不补的话，升级上来的部署连一次设置改动都存不下去。三个变量也认旧拼写 `RESIN_ALERT_*`（启动时打一条弃用警告）：解析顺序与其它设置一致，`PRISM_` 优先、取不到才回退旧拼写；**校验与应用读的是同一份解析结果**，所以旧拼写不会被「校验通过、静默忽略」。

**被看见的路径**：`GET /api/v1/admin/alerts?limit=&since_ns=&unacknowledged=` 列出告警（默认 50 条、上限 500，按检测时间倒序；账号名与套餐名在读取时解析，账号或套餐删掉后告警仍留着当时的 id 与数字），`POST /api/v1/admin/alerts/{id}/actions/acknowledge` 确认（重复确认同一条返回 404——「已经处理过」不是错误，是事实）。控制台的读入口在用量统计页，见 §8。

## 10. 验收与回归

多租户这一层的自动化证据在四处，改动后请一起跑：

- `internal/api/rolegate_test.go`：角色矩阵（用户/管理员/无凭据/密钥持有人在管理路径与自助路径上的结果）。
- `internal/api/multi_tenant_admin_e2e_test.go`：走真实 HTTP 栈的全链路——建套餐 → 建用户 → 发密钥 → 查用量 → 证明租户读管理接口得到 403 → 停用（会话失效、密钥在数据面不可用）→ 删除（密钥清空、账号 404），以及邀请码注册的正反两面。
- `internal/service/control_plane_tenant_test.go` 与 `internal/state/repo_invite_test.go`：服务层与状态层行为，含邀请码兑换的原子性。

- `internal/service/control_plane_alert_test.go`、`internal/state/repo_alert_test.go` 与服务层的检测语义：越线边界（恰好达到报一条、差一点不报、不限量套餐永不报）、去重与确认后的重报、保留窗口清理。端口屏蔽的证据在 `internal/config/blocked_destination_ports_test.go` 与 `internal/proxy/direct_dial_guard_ports_test.go`：默认名单、空值放行全部、非法端口名报错，以及「关掉私网拒绝也照样拦发信端口」。

- 用量记账与到期收口：`internal/usage/flush_test.go`（水位线只认真正落盘的字节、播种幂等、周期轮转清空水位线）、`cmd/prism/app_runtime_usage_seed_test.go`（补种循环补上「落盘总量还没被覆盖」的账号）、`internal/state/repo_user_subscription_expiry_test.go`（`expires_at_ns = 0` 不动、边界值、重复跑幂等、收口后释放活跃槽位）与 `internal/service/control_plane_subscription_sweep_test.go`（一趟收口只碰窗口已过的那条）。
