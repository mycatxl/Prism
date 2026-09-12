# 免费 IP 质量来源选择

核查时间：2026-09-07 至 2026-09-08。目标：为个人部署的 Prism 选择一到两个免费、能持续调用且可解释的数据源。实装选择 **ProxyCheck v3 为主，AbuseIPDB 为可选补充**。没有找到足以证明某家“绝对最准”的独立对照基准；下面区分接口实测、官方说明和社区个案。

## 接入选择

| 来源 | 核实的能力和免费条件 | 在 Prism 中的用途 |
| --- | --- | --- |
| [ProxyCheck](https://proxycheck.io/api/) | 可按指定 IPv4/IPv6 查询网络类型、代理/VPN/Tor/hosting 等特征、风险和置信度。官方说明无 Key 按请求来源 IP 每日 100 次，免费 Key 每日 1,000 次；已实测 v3 匿名查询返回这些字段 | 默认主源。本机预算 80 次/日；配置免费 Key 后默认 900 次/日，给同一来源的其他使用留余量 |
| [AbuseIPDB](https://www.abuseipdb.com/pricing) | [FAQ](https://www.abuseipdb.com/faq)确认免费账户可持续使用 API，每日 1,000 次检查/报告，需注册取得 Key。字段包括举报置信度、举报数量、独立举报者和最近举报时间 | 可选补充，只调用 check，不提交举报；查询近 30 天，单独预算 900 次/日。无 Key 不调用 |
| [IPPure](https://ippure.com/MyIP-Info-API) | 公共 `https://my.ippure.com/v1/info` 无 Key 单次实测为 200；只查请求当前出口，返回 fraudScore、住宅判断等。接口仍在测试阶段，没有明确公开免费次数或 SLA | 保留人工复核入口；浏览器须使用目标节点。暂不作为后台批量来源 |
| [ipapi.is](https://ipapi.is/free-tier.html) | 匿名接口实测已不返回所需代理/风险标志；免费账户 Key 可提供完整字段。开发者文档与新的免费层说明存在更新差异，不能沿用旧脚本的免 Key 字段假设 | 保留为后续可替换主源候选；不因为旧帖子称“免 Key 可用”就依赖已移除字段 |

预算以 **IP + 来源 + 查询版本** 去重，多个节点共用同一出口时不重复消耗额度。它是本实例的调用预算，不能保证同一个公网地址或 API Key 在其他工具中没有消耗；来源返回 429 时仍暂停并遵守 Retry-After。注册、充值和购买套餐没有被自动执行。

选择两家是为了覆盖不同信息：ProxyCheck 侧重连接属性及其风险模型，AbuseIPDB 侧重近期举报。举报置信度不是代理概率，网络类型也不是恶意程度，因此不平均分数，不把未举报填成“绝对安全”。Prism 保留原始风险值和独立举报信息，以来源定义的风险等级辅助阅读，不生成未经校准的纯净度百分比。

## IPPure 的具体边界

主代理已直接阅读官方 API、FAQ 与条款原文。[FAQ](https://ippure.com/faq.html)说明：IPPure 使用风险信息库和蜜罐统计，无记录的地址会依据网段和关联信息推断；IPv6 不计算 IPPure 系数。Cloudflare 风控系数另为 `100 - Bot Score`，与 IPPure 自有分数不同，且针对当前请求；公开 API 的 `fraudScore` 不能标为 Cloudflare 分数。

[使用条款](https://ippure.com/terms-privacy.html)（2025-12-17 生效）限制未经授权的高频/批量访问及系统性抓取、存储、再分发。公共 API 文档没有明确说明上述限制的豁免范围。因此“单次匿名调用成功”不足以支持后台自动批量接入与持久缓存。技术上可经节点调用其当前出口接口；这项自动接入未启用。

## 社区反馈及证据强度

| 来源 | 读到的反馈 | 能支持的结论与局限 |
| --- | --- | --- |
| [Reddit / r/techsupport：IPQS 高风险](https://www.reddit.com/r/techsupport/comments/s3yd2m/why_is_my_ip_address_high_risk_and_what_does_it/) | 用户报告 Fraud score 83，自称未使用 VPN，询问是否误报 | 是误报疑问个案，网络环境未经独立核实；不能据此证明该服务普遍不准 |
| [Reddit：IPQS 与 AbuseIPDB 结果不同](https://www.reddit.com/r/techsupport/comments/1eh3opd/ip_address_lookup_said_abuse_on_ip_quality_score/) | 同一用户说 AbuseIPDB 为零，IPQS 有风险 | 两家衡量和覆盖不同；AbuseIPDB 为零不能直接证明另一家错了 |
| [Reddit / r/sysadmin：AbuseIPDB](https://www.reddit.com/r/sysadmin/comments/dnfweq/block_ips_from_abuseipdb_in_fail2ban/) | 用户认为有用，同时指出公开 DNS 等也可能被举报 | 举报可以提供线索，不应见到报告就当成已证实恶意；材料较旧 |
| [Reddit / pfBlockerNG：举报名单](https://www.reddit.com/r/pfBlockerNG/comments/1qusas1/abuseipdb_blocklist_feed_to_pfblockerng/) | 有实践者认为有效，其他评论建议用 30 天或更短窗口控制陈旧记录 | 支持重视举报时效，不构成准确率实验 |
| [Linux.do：IPPure 检测问题](https://linux.do/t/topic/1536108) | 用户混淆 Cloudflare 分数方向，回复说明其等于 100 减 Bot Score，并报告刷新波动 | 与当前请求有关的风控不能当作固定的 IP 信誉指标；该帖不证明 IPPure 自有分数错误 |
| [Linux.do：Ping0 实验](https://linux.do/t/topic/942959) | 作者报告分数/共享人数可能受到本站访问行为影响，并给出自述对照 | 比单张截图信息更多，但并非本项目复现的实验，不能断言因果机制；作者有节点使用者群体，存在利益相关 |
| [Linux.do：多库综合评分](https://linux.do/t/topic/2012806) | 有用户反例质疑共享代理得到 100 分，也有回复反对简单多数投票 | 多库不自动等于准确；首帖带聚合产品推介，不能视为独立基准 |

Reddit 通过 Exa 读取公开索引正文/评论片段，未读取完整讨论串。Linux.do 上述主题读取了全文或首帖及前 20 条，具体原始记录在 `/tmp/prism-research-linuxdo/`；长主题没有全部翻页。未找到可核实的 Reddit IPPure 独立讨论，不能据此推断没人使用或口碑差。

Twitter/X 已做公开索引检索，也尝试了安全包装的已配置凭据搜索：包装禁用了浏览器 Cookie 自动回退，接口返回 HTTP 404。没有取得足够可靠的独立推文，**本选择不声称得到了 Twitter 共识**，也没有用厂商宣传替代它。

## 实际实现

代码位于 `internal/inspection/`、`internal/quality/` 与 `internal/state/repo_quality.go`。新证据在 state.db 中持久化，默认复用 24 小时；过期、未检测、失败和来源冲突分别处理。检测错误保留上次成功证据及原有效期。401/403 暂停来源，429 合并禁用期；不同任务的较弱限制不会解除较强限制。

前端提供节点质量列、过滤、检测操作、来源明细、举报复核和 `/quality` 页面。评级仅作查看与筛选；本轮没有将质量规则接入代理路由硬准入，也未实现 rotate。大量不同 IP 的首次检测受免费额度约束，不承诺立即完成十万级库存扫描。
