# WP12 · 前端改造（`internal/api/web`）

**前置**：WP04–WP11 的接口已经就位。**技术栈沿用现有方案**：React 19、TypeScript、Vite、@tanstack/react-query、@tanstack/react-table、react-hook-form 加 zod、i18next、lucide-react、recharts。页面结构和组件风格参照现有的 `features/*`（包括 `api.ts`、`types.ts`、页面组件、`components/ui/*`）。**不引入新的 UI 框架。**

## 1. 通用要求

- 所有请求都通过 `lib/api-client.ts` 发出。每个 feature 都有自己的 `api.ts`（封装 fetch）和 `types.ts`（与后端 JSON 字段一一对应，使用 snake_case）。
- 新增文案必须同时提供中文和英文（`i18n/translations.ts`；如需分文件，参照 `i18n/quality.ts` 的写法）。
- 支持深色和浅色主题、窄屏布局（≤ 768px）、键盘操作；所有图标按钮都要有 `aria-label`。
- SSE 使用 `EventSource`。由于 `EventSource` 不能带 `Authorization` 头，**后端 SSE 接口同时接受查询参数 `?access_token=`**：
  - 只对 `/events` 这类接口生效；
  - 服务端用常量时间比较；
  - 请求日志中这个参数要脱敏；
  - 这一点需要在 WP08 的 API 中同步实现。

## 2. 导航（`lib/navigation.ts`）

在"工作区"分组中，把"检测任务"（`/jobs`，图标 `ListChecks`）放在"节点池"之后。在"观测与配置"分组中新增三项：

| 名称 | 路径 | 图标 |
|---|---|---|
| 数据源与检测 | `/intel` | `ShieldCheck` |
| 导出与订阅 | `/exports` | `Share2` |
| 审计日志 | `/audit` | `ScrollText` |

## 3. 页面与改动

### 3.1 节点池（`features/nodes`）

- **多选**：表格第一列改为复选框，支持全选当前页；另有"按当前筛选条件全选"（选中的是筛选条件本身，由后端展开）。
- **批量操作栏**（有选中项时出现）：
  - 探测出口：对应 `kind=egress`；
  - 批量检测 IP：对应 `kind=intel`；
  - 解锁检测：对应 `kind=checks`；
  - 完整检测：对应 `kind=full`；
  - 导出：打开导出对话框，见 §3.6。

  点击后调用 `POST /api/v1/intel/jobs`，scope 为 `node_hashes`，或者在"全选筛选结果"时为 `filter`。成功后弹出通知，带"查看任务"链接。
- **新增列**（可在"列设置"中显示或隐藏，设置保存在 localStorage）：
  - 引擎徽标（singbox 或 mihomo）；
  - `protocol_detail`；
  - 出口 IPv4 和 IPv6，以及 colo；
  - 国家和城市；
  - ASN 和组织；
  - IP 类型；
  - 原生 IP；
  - 纯净度（分数、区间色块、置信度小点）；
  - 判定；
  - 解锁摘要（每个检测一个小图标，悬停显示结果和地区）。
- **新增筛选项**：engine、protocol（下拉选项来自 `/system/capabilities`）、纯净度范围滑块、verdict 多选、ip_type、native、confidence_min、ASN、country，以及"检测项 = 结果"（例如 chatgpt 为 available）。
- **节点详情抽屉**新增"IP 情报"标签页，数据来自 `GET /api/v1/intel/nodes/{hash}` 和 `/intel/ip/{ip}`：
  - 纯净度卡片：分数、区间、置信度、判定、原因码的中文说明；
  - 分项明细表：来源、原始值、洁净分、权重、观测时间；
  - 各数据源证据（可折叠）；
  - 解锁结果列表；
  - 出口 IP 历史；
  - 按钮："重新检测"（创建单节点 `full` 任务）和"IPPure 复核"（沿用现有的 `IPPureReview` 组件，接口改为兼容别名）。
- 删除旧的"出口记录"视图中依赖已删除字段的代码；该视图改为按 IP 聚合展示 `GET /quality/assessments`（兼容接口）或新的 `/intel` 列表。

### 3.2 检测任务（新建 `features/jobs`）

- **列表页**：状态、类型、范围摘要、进度条（done/failed/skipped/total）、`pending_online_lookups`、创建者（手动、订阅自动、定时刷新）、创建时间、耗时。支持按状态筛选。
- **新建任务对话框**：
  - 类型：egress、intel、checks、full；
  - 范围：全部节点、按订阅（多选）、按平台（多选）、按当前节点筛选（从节点页带入）；
  - 数据源：多选，默认全部已启用；
  - 检测项：多选，默认全部已启用；
  - `force` 开关。
- **详情页**：
  - 实时进度（SSE）；
  - 取消和重试失败项两个按钮；
  - 失败项列表（节点名、步骤、错误码、错误信息），点击可跳转到节点详情。

### 3.3 数据源与检测（新建 `features/intel`）

- **数据源卡片**：每个数据源一张，数据来自 `GET /api/v1/intel/providers`。
  - 显示：名称、类型（离线、在线、经节点）、启用开关、Key（只写输入框，显示"已设置/未设置"，可清除）、每日额度、QPS、TTL、今日用量进度、状态（正常、暂停、限流至某时间）、排队数、失败数、条款提示（`Terms`）。
  - 操作："测试"（提示会消耗 1 次额度）和"恢复"（解除暂停）。
  - IPPure 卡片固定显示黄色提示："请自行确认 IPPure 条款；默认每分钟 1 次"。
- **离线库状态**：各 mmdb 的版本和更新时间，外加"立即更新"按钮（沿用现有的 GeoIP 页面，把入口合并到这里；原 `/resources` 路由保留并重定向到这里）。
- **检测规则表**：来自 `GET /api/v1/intel/checks`。显示 id、名称、类别、版本、来源、校准日期、启用开关。
- **总览**：来自 `GET /api/v1/intel/status`。显示已知 IP 数、已评估数、各判定和各区间的分布（使用 recharts 柱状图）、intel.db 大小、丢弃计数。

### 3.4 订阅管理（`features/subscriptions`）

- **新增字段**：`auto_intel` 开关（默认开）、`user_agent`。
- **"导入预览"**：在保存之前调用 `POST /subscriptions/actions/preview-parse`，展示统计（按引擎、按协议）、前 50 个节点，以及跳过清单和原因（原因码显示中文说明）。
- **解析报告**：订阅详情中新增一个标签页，展示 `GET /subscriptions/{id}/parse-report`。
- **OpenVPN 导入**：新增"导入 OpenVPN"按钮。
  - 可以选择一个或多个 `.ovpn` 文件，文件在浏览器本地读取；
  - 可以统一填写用户名和密码，也可以逐个填写；
  - 前端生成 `prism_openvpn_bundle` JSON，作为本地订阅内容提交；
  - 需要凭证但未填写的配置标红，禁止提交。

### 3.5 平台详情（`features/platforms`）

- **"质量准入"表单**，对应 `quality_policy`：
  - 最低纯净度（数字或滑块）；
  - IP 类型多选；
  - 允许的判定多选；
  - 最低置信度；
  - 仅原生 IP；
  - 必需检测（键值对列表）；
  - 评估最长有效期和出口最长有效期；
  - 未知时的处理方式（排除或允许，默认排除）；
  - 排除 Tor 和排除高风险两个开关（默认开）。

  表单为空时，显示"不做质量准入（与 Resin 相同）"。
- **预览**：调用 `preview-filter`（带上 `quality_policy`），展示 `excluded_by` 各原因的计数。
- **"定时轮换"区块**：启用开关、间隔、"避开上一个出口 IP"开关。
- **租约列表**：每一行新增"轮换"按钮，调用 `POST /leases/{account}/actions/rotate`。
- **单节点"为什么被排除"**：在平台视图的节点列表中，点击节点即调用 `explain` 接口并显示结果。

### 3.6 导出与订阅（新建 `features/exports`）

- **导出对话框**（节点页调用）：
  - 格式：sing-box、mihomo、v2rayN、URI、CSV、JSON；
  - 名称模板：带变量说明，并实时预览前 3 个节点的名称；
  - 只导出健康节点（开关）；
  - 下载后显示"导出 N / 跳过 M"，跳过原因可以展开查看。
- **订阅配置列表**：增删改查。
  - 创建或轮换令牌后，只显示一次订阅地址，并提供"复制"按钮，同时提示"关闭后无法再次查看"；
  - 列表显示最近访问时间和访问次数。

### 3.7 系统

- **系统配置页**新增"检测"分组，对应 `intel_enabled`、`intel_node_workers`、`intel_check_concurrency_per_check`、`intel_max_running_jobs`、`intel_auto_checks`、`intel_refresh_schedule`。
- **"关于"区块**：展示 `/system/info` 中的版本和 `build_tags`，以及 `/system/capabilities` 中的协议列表（按引擎分组）。
- **审计日志页**：基于 `GET /api/v1/audit-logs`，使用游标分页（`before_id`）。

## 4. 兼容与清理

- 质量相关的旧组件如果依赖已删除的字段，迁移到新接口；如果不再使用，直接删除。
- `QualityStatus` 等旧类型只保留兼容接口仍在使用的那部分。

## 5. 测试与验收

```sh
npm --prefix internal/api/web run lint
npm --prefix internal/api/web run build
npm --prefix internal/api/web run test:config
make backend && npm --prefix internal/api/web run test:e2e   # 使用真实后端
```

- 扩展 `scripts/check-ui-live.mjs`（Playwright）：
  - 登录；
  - 导入一个本地订阅（使用 WP13 的本地测试节点），检查导入预览；
  - 在节点页多选节点并创建 intel 任务，等待任务完成；
  - 在节点详情中看到 IP 情报；
  - 配置平台质量准入，检查预览计数发生变化；
  - 导出 v2rayN 订阅，并用获得的订阅地址 `GET` 到 200；
  - 在深色主题和 375px 宽度下各截图一次。
- 截图输出到 `internal/api/web/test-results/`，该目录已被 gitignore。
