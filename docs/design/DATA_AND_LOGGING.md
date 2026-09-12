# 个人版数据、日志与恢复

状态：实现基线 v2.1。数据库选型已确定，表结构为工程设计，实际 migration 在 M1 实现并测试。关联：[总设计](../DESIGN.md)、[安全](SECURITY.md)、[性能](PERFORMANCE_AND_OPERATIONS.md)。

## 1. SQLite 的定位

单机个人版采用 SQLite，常规验收 10 万库存、峰值 30 万库存；百万只作为可选冷库存压力场景。该选择基于本地单写、批量更新和索引查询的工作负载，不表示已完成这些规模的压测。数据库行数不是 Outbound 数量，也不是并发代理连接数。

使用四类文件，但按可靠性分层，不要求每类必须很小。原始节点、来源关系和成功检测证据放入可靠存储；不能因为沿用 Resin 的 cache 命名就允许唯一一份已导入节点丢失。

| 文件 | 权威内容 | 写入与丢失语义 |
|---|---|---|
| `state.db` | 配置、节点库存、订阅与标签、凭证摘要、检测 profile、质量证据/评级、持久化任务、管理操作与审计 | WAL + synchronous=FULL；提交后才确认成功，受文件系统可靠性边界约束 |
| `cache.db` | 节点健康/延迟快照、出口观测、候选索引、普通路由租约 | WAL + NORMAL；有界 dirty set 批量写回，可丢最近窗口并重建 |
| `metrics.db` | 流量、连接、探测统计 bucket | WAL + NORMAL；可丢失未刷窗口，不影响路由 |
| `request_logs-*.db` | 有限保留的诊断记录、可选受限 payload | 异步批量、滚动、丢弃计数 |

本地原始导入内容若落临时文件，仅作 staging；导入完成必须以已提交节点库存为恢复依据，不能依赖文件仍存在或远程订阅能再次下载。

每库一个写连接和独立有界工作队列，读连接少量池化；所有连接启用 foreign_keys 和有界 busy_timeout。StateEngine 统一管理配置、库存、质量及缓存的写入口；metrics/requestlog 使用自己的仓储与队列，避免日志阻塞配置。模块不得自行绕过所属写入口打开数据库。

SQLite WAL 允许读写并行但单库仍只有一个 writer。不把 ATTACH 下多个 WAL 数据库当成跨文件原子事务，不在不支持所需锁语义的网络共享目录运行。

## 2. 身份与版本

- 平台、订阅、任务和证据使用稳定 UUID；名称用于展示或入口解析。
- 节点保留 Resin 规范化配置 hash，忽略 tag 去重。hash 不是认证材料；对相同 hash 校验规范化配置一致，发现碰撞记录冲突并拒绝错误合并。
- 一节点在同一订阅中可以有多个原始 Tag，标签来自引用关系，不属于全局节点的单个字符串。
- IP 使用规范化网络字节与 family，IPv4-mapped IPv6 先 Unmap；分组成员带 probe profile、node revision、观测 generation 和时间。
- `policy_version`、`reference_revision`、`assessment_version` 分别标识变化，不能只靠时间戳决定哪个结果更新。
- API 的运行期会话 generation 与 `runtime_id` 一起使用；重启产生新 runtime_id，旧 CAS 请求不能因计数重置而命中。
- 节点、来源认证与受限原始证据存加密数据和 key version；列表中的摘要字段不含 secret。

## 3. state.db 逻辑表

以下是表契约，非可直接执行的 SQL；字段范围、外键和索引必须落到 migration 测试。

| 表 | 主键/唯一约束 | 主要字段与用途 |
|---|---|---|
| system_config | 单行主键 + version | 运行配置、发布版本、更新时间 |
| subscriptions | id | 名称、来源、secret_ref、启停、刷新/合并模式、published_generation |
| nodes_static | hash | encrypted_options、key_version、protocol、created_at、config_revision |
| subscription_generations | subscription_id + generation | import_id、状态、计数、完整性摘要 |
| subscription_nodes | subscription_id + generation + node_hash | 引用、evicted、引用版本 |
| subscription_tags | subscription_id + generation + node_hash + tag | 每个原始标签独立保存 |
| node_annotations | node_hash + key | 手动分类与版本，不覆盖原始标签 |
| platforms | id；name 唯一 | sticky TTL、标签/地区/质量/优先级/来源范围、policy_version |
| credentials | id | kind、digest、版本、平台/动作范围、过期与撤销时间 |
| quality_profiles | id + version | 不可变评级/名单/来源配置与启用状态 |
| quality_evidence | id | IP、provider、query/profile version、来源分数/等级、状态、时间、有效期 |
| quality_assessments | id；IP + profile version + assessment_version 唯一 | 合成评级、类型、confidence、区间、有效期、状态 |
| assessment_evidence | assessment_id + evidence_id | 评级使用了哪些来源证据 |
| evidence_lists | evidence_id + list_id + category | listed/clear/unknown/not_applicable，与单次证据绑定 |
| inspection_tasks | dedupe_key 唯一 | IP/节点、kind、next_run、attempt generation、工作状态、lease expiry |
| operations | credential_scope + idempotency_key 唯一 | 请求摘要、状态、结果、runtime_id、保留期限 |
| audit_events | id | 操作者凭证 ID、action、object、前后版本、脱敏摘要、结果 |
| imports | id | 来源、staging、计数、状态、输入摘要和逐条报告引用 |

外键在同库生效；subscription_tags 必须引用对应的 subscription_nodes。节点删除不得级联删除仍被其他来源引用的节点。发布视图只读取 published generation，staging 数据不可用于路由。

质量数值字段包括 score_exact/score_low/score_high/upper_inclusive，可整体为空。约束为 0 <= low <= high <= 100；非闭区间要求 low < high；exact 存在时必须在区间内。等级来源不伪造数值，缺证据不填 0 或 100。状态和可信程度为受限枚举，valid_until 不早于 observed_at。

不存在当前节点引用的 IP 历史仍可在保留期内存在；没有引用不是数据损坏，不在一致性修复中立即删除。

## 4. cache.db 与运行态

| 表 | 关键约束 | 用途 |
|---|---|---|
| node_dynamic | node_hash + config_revision | 健康、失败数、最近探测、受限错误摘要 |
| node_latency | node_hash + profile/domain | TD-EWMA、样本时间 |
| egress_observations | node_hash + probe_profile + family | IP、observed_at、valid_until、generation、稳定性 |
| egress_members | IP + family + probe_profile + node_hash | 可重建的出口成员反向索引 |
| leases | platform_id + session_key | IP、节点、expiry、last_access、generation、runtime_id |
| candidate_cache | policy_version + scope_revision + node_hash | 可选预计算摘要，不是授权真源 |

分组无须有一个“整个组唯一的探测时间”，每个成员有自己的观测，组内质量引用来自 state.db。陈旧成员不能因另一条线路刚检测成功而变新鲜。

只有运行态短期数据进入 dirty set。flush 用版本化交换批次，成功后只移除对应旧版本，失败合并重排；新更新不能被旧 flush 的清理覆盖。队列同时限制条数与估算字节数，满载时暂停后台生产者或丢弃允许丢失的旧快照并计数，不阻塞代理。

候选视图只载入紧凑摘要，原始配置按需由后台预热为有界 Outbound。当前没有热候选时，代理返回可重试的暂不可用并触发去重预热，不在请求线程读取全库存或初始化全部节点。

## 5. 导入、质量与动作事务

### 5.1 导入与订阅刷新

先限制输入/解压大小并解析，按小批把节点、引用和标签写到新 generation。staging 可以分多次事务提交，避免几十万行持锁一个大事务。全部批次完成后，短事务校验 manifest、发布 generation、更新订阅和审计；提交前旧 generation 继续有效。

发布后先标记订阅引用版本失效，再异步构建平台视图；新路由检查版本，不得使用旧已删除引用。崩溃前未发布批次可清理或按 import_id 恢复，不能误计为导入成功。

旧引用分批清理。增量合并使用上一次 published generation 构造新集合，不依赖可能被清空的运行缓存。订阅失败保留现有库存；部分格式损坏导致零节点/明显异常时保持旧版本并报告，清空需要显式确认操作。

### 5.2 检测结果

任务 claim、成功证据、评级和任务完成使用 state.db 有界事务；网络请求不在事务或节点锁内。旧 attempt 结果可以记录历史，但只有匹配最新任务 generation 才能发布 current assessment。提交完成才报告检测成功，写入失败保持存储错误并限制后续查询，不能不断消耗付费额度。

健康/出口观测是路径相关运行数据，更新缓存及即时内存版本；IP 信誉证据不因节点换出口而被删除，也不能继续挂到新出口上。

### 5.3 管理与 rotate

配置和审计同事务提交，API 成功需新准入版本已生效。普通自动租约保留 Resin 弱恢复语义，不为每次代理请求增加数据库事务。

rotate 属于低频管理操作：持久化幂等请求，在无网络 I/O 的会话 CAS 点更新绑定，确认记录和审计后才返回成功。操作进行中同会话新分配有界等待或返回暂不可用；不持全局锁等待 SQLite。提交与内存发布间崩溃的操作恢复为“需核对”，不能盲目再切一次。

幂等结果是历史操作结果，不证明重启后当前租约未变化；响应含 runtime_id 和 generation，查询当前绑定再决定后续动作。普通租约过期、重启丢失缓存或上游换 IP 后可重新分配，产品明确不承诺重启永远保持原 IP。

## 6. 日志与统计

| 数据 | 最小字段 | 留存与可靠性 |
|---|---|---|
| 访问日志 | request_id、协议、platform/session 摘要、节点、观测 IP/版本、policy/assessment 版本、耗时、错误阶段、字节 | 普通异步诊断，可丢并计数 |
| 检测历史 | task/attempt、节点/出口版本、provider/profile、时间、有效期、结果、错误、响应大小 | 成功证据可靠保存，原始诊断摘要有上限 |
| 操作审计 | credential、action、对象、前后版本、操作 ID、结果 | 管理变更同事务；不存 secret |
| metrics | 时间桶、协议/平台级计数、连接、流量、探测、资源 | 聚合，无账务用途 |

普通 Account 可以展示；从认证头提取的会话值按安装密钥 HMAC，格式带版本。访问日志默认不存完整 query、认证头、请求/响应正文。原始捕获必须显式开启且限时限量。observed_egress_ip 是最近观测值，不伪装为该请求已验证的真实出口。

新请求在开始生成 ID，结束时写日志。CONNECT/SOCKS5 日志耗时是隧道寿命，不能当成 HTTP 请求延迟。长连接期间用计数器报流量，不等日志落盘；HTTP 连接池复用时全局传输计数和按请求归属的统计分别定义，不能给连接永久贴首个请求的平台。

默认访问日志保留最多 7 天且最多 1 GiB，总量或时间任一达到即滚动淘汰；单文件目标 128 MiB。质量历史保留当前仍有效引用及最近 30 天，另设 512 MiB 历史预算；预算不够时停止新扩展查询并告警，不为腾空间删除当前准入依赖。metrics 默认 30 天聚合，审计默认 90 天。所有默认值可调，有容量告警。

删除记录与 checkpoint/空间回收分开安排，禁止每次清理后执行全库 VACUUM。导出和长查询有时限，不能无限固定 WAL 读快照。

## 7. 启动、备份与故障

启动顺序：独占进程锁 -> 目录/密钥/数据库检查 -> migration -> 校验库存 published generation -> 加载配置与紧凑摘要 -> 恢复并校验运行快照 -> 重建当前平台所需视图 -> 恢复未过期租约 -> 有界预热与后台任务 -> listener 就绪。冷库存剩余重建在后台进行。

损坏的 state.db 默认停止入口，用本地 CLI 修复/恢复；不能把唯一库存直接删除当成缓存。损坏的 cache.db 可隔离后重建，但启动日志必须说明会话/健康需要恢复，严格节点重新取得有效观测前不放行。

孤儿跨库引用按权威状态版本清理。异常大规模修复需要 dry-run/人工确认，不自动大面积删除。检查用分批/索引查询，不能启动时每个节点做一次独立 SQL。

在线备份使用 SQLite backup API 或等效一致快照，不能直接复制活动 .db 并忽略 WAL。多库备份带 manifest、schema version、时间、摘要和加密 key ID；权威 state.db 保证单库一致，运行库允许恢复后修复。停机复制需完成 checkpoint 并确认没有 writer。恢复在独立目录校验后替换，保留旧文件。

支持 down 的 migration 也需测试；无法无损映射时不提供虚假的 down。升级前备份，不因迁移失败删除旧库。测试覆盖掉电模拟、磁盘满、截断副本、缓存全丢、源订阅不可达及主密钥缺失。

## 8. 索引与容量门槛

优先索引：node hash；订阅 published generation + node hash；IP/family/profile + evidence time；task state + next_run + id；日志 time + id 及受支持过滤项。正则不由普通 B-tree 加速，必须冷路径评估；不给每个高频变化的计数器盲目加索引。

分页走可索引的 keyset，列表只返回摘要。相同筛选并发限量，完整计数/导出异步执行。JSON 只存配置，常用筛选字段有规范化列，避免每次列表全量解析 JSON。

若检测生产速率长期超过实测持续写速率、权威写入 p99/积压或恢复时间超预算，先分析事务批次、索引、长读和磁盘；解决后仍无法满足，才评估额外质量库或 PostgreSQL。依据见性能文档，不能仅因 30 万行就更换数据库。
