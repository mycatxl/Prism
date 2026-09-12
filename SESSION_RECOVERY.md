# Prism 会话恢复记录

恢复日期：2026-09-07。依据本机原始 Codex 会话、会话索引和现存项目文件整理。

## 本轮已完成并部署（2026-09-12）：IPPure 集成与质量 UI 复审

用户要求深度整改质量评分体系，最终确定 **IPPure 主评分 + ProxyCheck 网络/匿名特征**。目标节点自行请求 IPPure；没有有效 IPPure 结果不生成节点主分，综合判定不混分。详情见 `docs/design/QUALITY_UI_REAUDIT_2026-09-08.md`。

本轮已修复4个问题（pending 状态丢失、部分证据误标齐全、过期提示覆盖当前高风险、旧 tag 参数无法清空），完成构建、验证、备份和部署。

- **后端功能**：IPPure 固定目标/选定 Outbound/全局限流（每实例一次并发、最短60秒间隔）/匹配出口后短期内存共享（最多512条、24小时有效+24小时过期提示）。综合评估、Business/Wireless 类型映射和真实协议字段。Tor Project Onionoo 公共角色名录的后台缓存（按需刷新、每小时检查）。IPPure 不写数据库、不自动批量扫描。
- **前端界面**：节点池合并线路与出口记录视图，重排六列表格（节点/出口/纯净度/网络特征/延迟/操作）。增加 IPPure 主分展示、原始风险和独立综合结论。设置页改为八类卡片入口，修正非法草稿丢失判断和 JSON 静默覆盖。十二类业务面板恢复桌面 24px/手机 18px 内距。
- **验证通过**：2026-09-09 10:54-11:29 UTC 完成 backend/race/vet/lint/candidate/browser-candidate 全通过。2026-09-12 11:01-11:03 UTC 部署后 browser-live 再次全通过，包括真实 IPPure 查询验证。
- **部署记录**：2026-09-12 11:00:17 UTC 重启服务，PID 3391837，二进制校验值 `1096670e4d...659c1d8e`。备份位置 `.local/backups/20260912T110005Z-before-ippure-restart`。实际测试节点 `86235360...` 成功获取 IPPure 证据：purity_score=69, band=mixed, network_type=non_residential。
- **实际状态**：已知428个出口IP，ProxyCheck已检测80个（今日额度80/80已用完），IPPure当前缓存1个IP结果。ProxyCheck数据库有400条证据，IPPure无持久化（仅内存共享）。
- **截图验收**：`web/test-results/screenshots/` 包含 quality-live.png、quality-live-detail.png、nodes-reviewed-fixture.png、settings-categories-light.png 等，覆盖桌面/移动、浅深色、质量详情、节点复核和设置分类。

## 本轮已完成并部署（2026-09-08）

本轮范围：实现节点纯净度/风险显示，根据 `gzcj.verifying.cc.cd` 和 StyleKit 模板重构前端，并核查 Reddit、Twitter、Linux.do 对免费数据源的反馈。功能、验证、备份和本机部署均已完成；下方 9 月 7 日记录保留为历史，不必重复实施。

- 已实际取图查看 gzcj 的公开登录页、StyleKit warm-dashboard/glass-landing。截图与页面数据：`/tmp/prism-design-references/`。设计依据和交付范围见 [出口质量工作台重构](docs/design/FRONTEND_QUALITY_WORKBENCH.md)。
- 数据源选择：ProxyCheck v3 主检测，AbuseIPDB 可选免费 Key 补充近 30 天举报记录；原始风险分和举报置信度独立显示，不生成未经校准的纯净度百分比。IPPure 公开 API 只查访问出口，条款限制批量/系统性存储，保留人工复核入口。完整证据、免费额度和社区材料局限见 [免费 IP 质量来源选择](docs/design/QUALITY_PROVIDER_RESEARCH_2026-09-08.md)。Twitter 直接搜索返回 HTTP 404，没有足够可靠的独立推文，不声称获得其社区共识或找到“绝对最准”的来源。
- 新模块：`internal/quality/`、`internal/inspection/`、`internal/state/repo_quality.go` 和 state migration 10；已接入 app/probe/service/API。任务按 IP/provider/profile 去重，预算、租约与 generation 持久化；证据复用 24 小时，失败保留原证据及原有效期，旧出口证据不附着到新出口，较弱限制不解除来源暂停。有界任务和证据存储，定期清理过期且不再被使用的历史记录。质量显示与筛选不改变节点健康或路由准入。
- ProxyCheck 默认本机预算 80 次/天，配置免费 Key 后默认 900；AbuseIPDB 有 Key 才启用，预算 900。配置变量：`PRISM_QUALITY_ENABLED`、`PRISM_QUALITY_API_KEY`、`PRISM_ABUSEIPDB_API_KEY`、`PRISM_QUALITY_DAILY_LIMIT`、`PRISM_QUALITY_WORKERS`、`PRISM_QUALITY_QUEUE_SIZE`。真实 `.env` 保持不变，目前两个来源的 Key 均未配置，主源匿名模式已启用。
- 新 API：`GET /quality/status`、`GET /quality/assessments`、`GET /quality/ip/{ip}`、`POST /quality/ip/{ip}/actions/probe`、`POST /nodes/{hash}/actions/probe-quality`（均在 `/api/v1` 下且要求管理员令牌）。节点列表/详情新增 `quality`；过滤支持 `ip_type`、`quality_state`、`risk_grade`（含 review）。手动检测会先刷新缺失或超过 15 分钟的出口信息。
- 节点池已加入“出口记录”视图、节点质量列/筛选/检测操作/证据详情，并重做 AppShell、登录页、总览与 `vibrancy.css` 为深蓝导航、白色数据区和蓝色质量概况。桌面/移动、浅深色、键盘交互、浏览器存储兼容和真实管理操作已回归。最终截图在 `web/test-results/screenshots/`，包括 `quality-live.png`、`quality-live-detail.png`。
- 本轮完整 `./cmd/... ./internal/...` 测试及 race、vet、前端 lint、构建和真实后端浏览器回归均通过；对应结果在 `.local/verification/` 的 `backend.json`、`race.json`、`vet.json`、`lint.json`、`candidate.json`、`browser-live.json`。最终 browser-live 于 11:00:36 UTC 完成，隔离实例真实查询 `1.1.1.1`，返回 Cloudflare/AS13335、机房类型、风险 33/100，并验证持久化和页面显示。真实库存没有写入验收样本。
- 升级前备份：`.local/backups/20260908T105928Z-before-quality/`，共 7 个文件，包含原 `.env`、原二进制和数据库。SQLite 使用在线 backup API，逐库 quick_check 通过；备份目录 0700，配置/数据 0600。各文件校验值与方式保存在 `manifest.json`；多个数据库不是跨文件原子快照。
- 已将验证过的 `.local/verification/prism-candidate` 原子替换到 `bin/prism`，2026-09-08 11:17:16 UTC 启动 detached standalone 进程，PID `2202160`，记录在 `.local/prism.pid`，日志 `.local/prism-runtime.log`。原配置校验值保持一致。尚未安装系统自启动；以后恢复时应确认进程状态，不要把历史 PID 当成仍在运行的保证。
- 部署后核验：1262/2260 健康及管理员 API 为 200，匿名/代理令牌访问管理 API 为 401，管理端口代理请求为 405，未鉴权 CONNECT 为 407；17 个配置/数据库文件权限为 0600。质量 API 在两个端口均通过鉴权检查；`/ui/`、`/ui/quality` 及主资源、质量页面资源与构建产物逐一校验一致。state migration 10 已干净应用，数据库 quick_check 通过。
- 2026-09-08 11:26:52 UTC 的实际库存状态：60 个已知出口 IP 均已有质量结果，ProxyCheck 已用 60/80 次，无来源暂停或存储错误；AbuseIPDB 等待配置。部署核验文件：`.local/verification/quality-deployment.json`、`local-instance.json`、`quality-local-instance.json`、`quality-migration.json`。
- 使用入口：`http://127.0.0.1:1262/ui/`；节点池的“出口记录”视图或节点中的“检测质量”。管理令牌继续使用原 `.env` 的 `RESIN_ADMIN_TOKEN`，不在交接记录复制值。质量路由准入、同出口优先级/故障切换、rotate 与 10 万/30 万容量认证仍是后续工作，不能算作本轮已完成。

## 上一轮已完成进度（2026-09-07）

- 用户最新协作约束：子代理不设额外数量限制，按任务需要与环境并发额度使用；承担独立探索或核验，修改和最终验证由主代理负责。
- Codex 配置已备份，用户要求的全局规则已安装到 `/home/ermit/.codex/AGENTS.md`。备份目录：`/home/ermit/.codex/backups/20260907T063919Z-before-global-agents-6l669p_f`。此项和原会话目录关联修复均已完成，无需重复实施。

- 实际项目已迁入 `/opt/Prism`；`web/`、`references/Resin/`、`.local/` 已就位，原 `/opt/Prismx` 已归档。
- 原开发副本和旧 SaaS 骨架完整保存在 `/opt/Prism-backups/20260907T065358Z-prism-migration-fmDEovmF/`。迁移核对了 20,912 个原项目条目和 56 个旧骨架条目，数据及令牌保留。
- Prism 品牌、配置兼容、浏览器存储迁移和 Glassmorphism/macOS Vibrancy 工作台已实现。最新原生 Go standalone 浏览器回归通过，覆盖桌面/移动、浅深色、键盘、受限存储和真实管理操作；截图在 `web/test-results/screenshots/`。
- 后端已迁为根目录 `prism` 模块：入口 `cmd/prism`，业务代码 `internal/`，嵌入 `web/dist`。`init`、默认 `standalone`、`backend` 和 `version` 已实现；同一进程独立监听管理 1262、代理 2260。当前 `bin/prism` 已由根模块重新构建，已替换旧参考程序。
- 已实现的安全修复：空令牌拒绝启动、默认 loopback 和 32 个探测 worker、资源响应大小限制、订阅 URL 错误脱敏、跨源跳转凭证清理、数据库文件 0600、新目录 0700、state.db FULL 同步持久化，以及各凭证校验点的恒定时间比较。
- 新请求日志移除 URL 的 userinfo/query/fragment，对请求及响应头在副本上脱敏后截断，错误中的 URL 也脱敏。业务头提取的账户仅在日志中使用 HMAC 标识，实际转发和租约身份保持兼容；历史日志、URL 路径、正文详情及租约原始账户尚未完成加密/脱敏迁移。
- 最终全量 Go 测试、race、vet、前后端构建、前端 lint、6 项配置测试和原生浏览器回归均已通过。结果统一记录在 `.local/verification/`，不要因进程句柄丢失而忽略已经落盘的验证。
- 前端最新 `npm audit` 为 0 项漏洞。Go 原先 6 项可达公告及后续有修复版本的依赖告警已对应升级；最终复扫没有符号可达或已导入包级告警，仅剩未导入的 OpenPGP 包对应模块级公告 `GO-2026-5932`。详情见 `vulnerabilities-summary.json` 与 [审计报告](docs/AUDIT_2026-09-07.md)。
- 当前 Go 最低版本为 1.26.0（安全依赖要求），本机工具链 Go 1.27.0。标准标签必须保留 `with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy`。sing-box 仍为 v1.12.21；WireGuard beta.7 的本地关闭锁顺序补丁在 `third_party/wireguard-go/`，重复创建/关闭及全量竞态测试均已通过。`http2legacy` 的真实 TLS HTTP/2 重置回归也通过。
- 已启动保留原 `.env`/数据的本地实例。2026-09-07 16:14:51 UTC：1262 UI/两个端口健康检查均为 200，管理员认证 API 为 200，匿名及代理 token 访问管理 API 均为 401；管理端口拒绝代理请求，13 个配置/数据库文件权限均为 0600。结果与二进制 hash 见 `.local/verification/local-instance.json`，启动日志为 `.local/prism-runtime.log`。未安装系统自启动服务，下一次恢复时需重新确认进程状态。
- 本轮目录迁移、界面改造、安全审计与独立运行验证已收尾。后续按 v2 实施计划推进剩余安全/库存基础、IP 质量检测与信誉来源、同出口优先级及 rotate；这些新增业务能力不计作本轮已完成，10 万/30 万容量也尚未验收。
- 面板配置主名称改为 `PRISM_UI_HOST`、`PRISM_UI_PORT`、`PRISM_API_TARGET`，兼容旧 `PRISMX_*`；后端数据路径已指向 `/opt/Prism/.local/`。
- Resin 参考基线：`9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`。`references/Resin/.git` 已保留；其迁移前已有的修改仅为 `.env.example`。
- 下文“中断时待办”记录恢复当时的情况；当前实施状态以本节和后续实际验证为准。

## 原会话

- 会话名称：**检查项目设计文档可见性**。
- 初始提问：能看到本项目下的相关设计文档嘛。
- 会话 ID：`01a070f9-e5bc-76e2-96e6-0670e2b0f14d`。
- 保存的工作目录：`/opt/Prism`（已从原 `/opt/Prismx` 修复）。
- 索引状态：未归档；已验证原会话出现在 `/opt/Prism` 的会话列表中。
- [原始会话记录](/home/ermit/.codex/sessions/2026/09/05/rollout-2026-09-05T05-50-20-01a070f9-e5bc-76e2-96e6-0670e2b0f14d.jsonl)。
- 最后一条用户消息：2026-09-06 12:03:48（US/Eastern），内容为“继续”。

原会话因关联旧目录而被当前目录的会话列表过滤。2026-09-07 已修复目录关联；重新打开 `/resume`，选择“检查项目设计文档可见性”即可。也可以在终端明确指定原会话：

```sh
codex resume 01a070f9-e5bc-76e2-96e6-0670e2b0f14d -C /opt/Prism
```

命令已通过本机 `codex resume --help` 和 [OpenAI 官方文档](https://developers.openai.com/codex/cli/reference#codex-resume)核对。修复只更新这条会话在状态数据库及存档头部中的 `cwd`；全部对话正文、历史数据库记录和分页偏移均保持不变。已通过两个独立 Codex 会话列表客户端验证目录筛选和会话读取。备份及校验结果保存在 `/tmp/prism-session-repair-7u232ajz/`。

## 两个目录的实际状态

| 目录 | 本次核实的内容 |
| --- | --- |
| 原 `/opt/Prismx` | 原实际开发副本，现已迁入 `/opt/Prism`，原件保存在上述备份目录。 |
| `/opt/Prism` | 当前实际开发目录；原有早期 SaaS 骨架已备份归档。 |

原会话中断时停在比较两个目录、准备迁移的位置，当时 README、包名和界面仍使用 PrismX。目录和命名迁移现已完成；根目录 `.git` 仍不可用且受保护，构建已兼容无有效 Git 元数据的源码目录。`references/Resin/.git` 有效且已保留。

恢复前目录旧 README 中的 SaaS、rpure 合并及其完成勾选不能作为后续依据。有效设计已随实际项目迁入当前目录：

- [项目说明](/opt/Prism/README.md)
- [总设计](/opt/Prism/docs/DESIGN.md)
- [个人开源版实施计划](/opt/Prism/docs/IMPLEMENTATION_PLAN_V2.md)
- [质量检测与路由](/opt/Prism/docs/design/QUALITY_AND_ROUTING.md)
- [同出口分组、容量与切换](/opt/Prism/docs/design/SCALE_AND_ROTATION.md)
- [数据、日志与恢复](/opt/Prism/docs/design/DATA_AND_LOGGING.md)
- [安全设计](/opt/Prism/docs/design/SECURITY.md)
- [性能与运维](/opt/Prism/docs/design/PERFORMANCE_AND_OPERATIONS.md)

## 已确定的需求

- 做个人自用加开源版本。用户已明确取消商业版，不实现 SaaS、套餐、支付、多租户；不合并 rpure。
- 性能和安全是最高优先级。保留 Resin 代理与调度核心，在此基础上增加检测、分类、平台筛选和日志。
- 导入节点后识别健康和实际出口，定时检测住宅/机房等 IP 类型、信誉、黑名单和纯净度；分数或区间需要证据，未知、失败和过期分别表示。
- 健康按线路保存，质量结果按出口 IP 去重和复用。同出口的多条线路保留独立状态，并支持优先级与故障切换。
- 平台保留 ANY/MUST/MUST_NOT 标签规则，扩展质量条件、筛选预览与排除原因；固定会话和切换 IP 需要继续遵守平台准入条件。
- 默认单机 SQLite。常规 10 万节点库存、峰值 30 万为设计验收目标，百万仅作可选冷库存压力测试，尚未证明实际容量。
- 权威库存、配置和证据放入 `state.db`；可重建运行快照放入 `cache.db`，指标与滚动日志独立。采用紧凑内存索引、有界 Outbound 热缓存、有界检测队列和批量写入。
- 管理面板使用 `127.0.0.1:1262/ui/`，后端 API/代理服务独立配置，当前为 `127.0.0.1:2260`。环境变量可配置端口和令牌。
- 前端要适合高频使用：连续分区、对象列表、主从详情、就地操作，兼顾浅深色、移动端、键盘和减少动态效果；操作连接真实 API。
- 最后追加的视觉方向是 Glassmorphism 与 macOS Vibrancy，并要求项目和目录统一命名为 Prism。

## 恢复时已存在的工作与验证记录（历史）

- 个人开源版 v2.1 设计和专项文档已经落盘。
- `web/` 是独立 React/TypeScript 管理前端，已接入 Resin 现有的节点、订阅、平台、探测、接入点、日志和设置 API。
- 面板有独立 Node 静态服务及 `/api` 反代，启动和配置说明见 [web/README.md](/opt/Prism/web/README.md)。
- 面板配置在 `web/.env`；后端实际配置在 `references/Resin/.env`。本次确认管理员与代理令牌均已设置，本记录不复制其值。
- 原会话的工具结果显示，2026-09-06 的前端 build、lint、4 项配置测试和最后一轮浏览器端到端检查通过。配置测试和端到端检查曾失败，后续修复后重跑通过。
- 早期记录中的 7 个 Resin 核心包测试通过只代表当时已有测试的结果，不代表完整审计或容量验收完成。

## 中断时尚未完成的工作

1. **目录迁移和统一改名。** `/opt/Prism` 已有旧骨架，需要保留其内容再完成迁移。原后端 `.env` 中 state/cache/log 仍使用 `/opt/Prismx/.local/...` 绝对路径，迁移时需一并处理。
2. **最后追加的前端重构。** 已有一轮工作台改造，但针对最后提出的 Glassmorphism/macOS Vibrancy 要求，没有找到完成交付和验收记录。原会话尝试读取参考网站失败。
3. **整个项目的深度审计。** 用户已提出要求，原记录有开始检查的过程；未找到完整的最终审计报告。
4. **新增后端功能。** 质量检测、信誉评分、住宅类型、同出口优先级、rotate 服务及容量改造仍主要处于设计阶段，不能把现有 Resin API 或前端页面视作这些功能已经实现。

## 最初会话恢复时的核验（历史）

- 只读核对了原会话索引、原始记录、设计文档、代码目录和配置状态。
- 最初恢复检查时，本机 `1262` 和 `2260` 均无法建立连接，沙箱外复核结果相同；该历史结果已被上面的本轮启动验证更新。
- 最初会话恢复动作没有重跑业务测试或改动业务代码。此后的开发、测试与服务状态以上面的“当前开发进度”和审计报告为准。
