# Prism Control Plane

`internal/api/web` 的持久化设计契约。它描述控制台如何组织真实的运营数据、权限边界和操作结果；API、Go 路由、查询参数和业务语义仍由服务端与 `src/features/*/api.ts` 负责。

## 1. 视觉世界

产品界面是 Prism Control Plane：一套有鲜明品牌个性、适合日常高频使用的现代运营工作台，不是营销页或报表封面。管理员在 `/ops/*` 维护可售代理业务；租户在 `/me/*` 获取接入并管理额度。两者共享 Prism shell、设计 token、主题和交互语法，但拥有不同的信息架构与数据边界。

管理员首页采用均衡工作台：交付状态、客户经营、治理证据并列，通过连续分区、对象列表、主从详情和就地操作组织。租户首页采用额度优先的账户工作面：套餐、周期用量、余额、到期和配额先出现，接入与密钥保持第一操作入口。避免卡片套卡片、KPI 卡墙、传统后台模板、营销 hero 和装饰性数据。

视觉使用清爽中性 daylight 与低眩光 night-shift 两套不透明 surface token。Prism cyan/teal 只表示交互、焦点、选择和结构标记；healthy/live、warn、alert、unknown/offline/reconnecting 使用独立状态色并始终配文字或形状。品牌通过棱镜标记、窄状态轨道、线性分隔、IBM Plex Sans/Mono、对象 ID、时间、地址和可比较读数表达。

页面优先使用连续分区、对象列表、主从详情、关系证据和就地操作。Shell 保持 rail、sticky context bar、work area 三个稳定单元；宽屏详情使用侧栏，窄屏详情使用 Sheet/Drawer，返回恢复筛选、分页和列表上下文。轻量动效只服务导航连续、选择反馈和 mutation 状态，不遮挡内容或改变布局；`prefers-reduced-motion` 下关闭非必要动效。

工作区不使用装饰性渐变、玻璃拟态、发光球、bento 拼贴、无来源 KPI、装饰地图或伪造健康率。未知、空、错误、stale、offline、reconnecting 与 mutation 状态保持独立语义；没有真实数据时不补数字、曲线或告警。

字体使用 IBM Plex Sans；IP、哈希、时间、端口、余额、数量和可比较读数使用 IBM Plex Mono。字距保持为 0，固定格式控件使用稳定 token 尺寸，面板圆角不超过 8px。

## 2. Token 与表面

权威 token 在 `src/styles/design.css`：

- `--p-canvas` 是工作区地面，`--p-rail` 是导航层，`--p-raised` 是面板层，`--p-inset` 是面板内嵌层，`--p-elevated` 只用于覆盖层。
- `--p-ink`、`--p-ink-soft`、`--p-ink-faint` 负责文字层级；`--p-rule`、`--p-row-rule` 负责结构边界。
- `--p-accent` 只表示交互；`--p-signal`、`--p-live`、`--p-warn`、`--p-alert` 表示状态；所有带色背景都使用对应 `--p-on-*` 前景 token。
- 所有颜色原语在 `:root` 和 `[data-theme="dark"]` 中各定义一次，并通过 `@theme inline static` 暴露给组件。
- 控件高度由 `--control-h-sm`、`--control-h`、`--control-h-lg`、`--control-h-xl` 提供；页面不得用偶然相等的 Tailwind 高度替代 token。

页面最多三层表面：canvas -> panel -> inset。面板使用共享 `Panel`，共享组件负责边框、阴影、状态和交互；业务页不自造按钮、输入框、表格、状态胶囊或弹窗。

## 3. Shell

Shell 是三个稳定单元：

1. rail：Prism 标记、当前角色、按运营域分组的导航、连接状态、语言和主题。
2. sticky context bar：当前位置、命令面板入口、账户菜单；不重复 rail 的目的地，也不重复 rail 的连接状态。
3. work area：页面标题、对象操作和业务面板。

管理员使用 `/ops/*`，租户使用 `/me/*` 和 `/account`。管理员路由表通过动态模块加载；租户会话不加载管理路由表，也不发起 system info、environment config、nodes、platforms、request logs 等管理查询。前端隔离只是纵深防御，服务端 `requireAdmin` 仍是权限权威。

桌面端显示完整 rail；低于 1100px 转为抽屉，带 scrim、Escape 关闭和焦点返回。context bar 保持 sticky。390px 视口仍保持完整可操作，表格通过共享表格容器或语义化堆叠布局适配，不产生页面级水平溢出。

Shell 状态只报告浏览器实际知道的事实：管理员实例探针、会话失效、连接中断、旧版令牌配置，或租户的服务连接/离线状态。未知不能显示成健康，离线不能被缓存冒充实时。

## 4. 工作区与对象页

管理员导航按工作组织为：Control、Inventory、Delivery、Customers、Governance。导航保留既有 `/ops/*` 语义和旧地址重定向；导航、路由和页面标题由同一张路径表驱动。

Control 首页组合已有 system、metrics、nodes、platforms、jobs、audit 等查询，只展示能追溯到真实接口的数据。没有统一摘要接口时，不补造容量、成功率、实时流量或健康百分比。

对象详情不是一张放大的表。节点、平台、用户和运行配置都应优先让操作员在几秒内找到：

- 身份：名称、稳定 ID、地址或哈希。
- 状态：文字加形状，必要时附状态时间和新鲜度。
- 核心事实：只显示接口返回或能由明确源数据安全格式化的值。
- 关系：平台、订阅源、租户、租约、请求或审计证据。
- 操作：可逆操作、危险操作、敏感信息操作分开。
- 证据：事件、探测、解析报告、日志或账本，显示时间和失败原因。
- 状态矩阵：loading、empty、success、stale、error、unknown、offline、reconnecting、mutation pending/success/error 均保持语义差异。

节点详情保留 Overview、Intelligence、Leases、Traffic、Events、Attributes、Probe 所需的真实数据面；无数据时显示未评估、未配置或不可用，不画假曲线。平台详情保留 Monitor、Access、Config、Leases，以及重置、重建、释放和删除等危险区。用户详情保留账户、余额、套餐、密钥、用量、账本和运营操作；余额保持微 USDT 整数语义，一次性密钥只在创建响应中展示。

## 5. 状态与操作

`StatusMark` 用于状态列表，`Badge` 只用于短标签。颜色不是唯一语义。QueryState 的 loading、empty、error 要保持目标区域高度；已有数据遇到刷新错误时不能被无意义清空。

每个交互控件都有 default、hover、focus-visible、active、disabled、loading、invalid/error 状态。提交中的 Button 必须设置 disabled 和 `aria-busy`。危险操作必须独立边界、二次确认、进行中状态、成功反馈和错误恢复。敏感信息必须说明生命周期，环境值必须只读。

动效只服务于状态变化和导航反馈，`prefers-reduced-motion` 下关闭非必要动画。键盘焦点、Dialog/Sheet 焦点捕获与恢复、Tabs 语义、表格标题和移动端触控目标由共享组件提供。

## 6. 验收

网页门禁必须保持绿色：

- `check:types`、`lint`、`build`
- `test:config`、`check:kit`、`check:responsive`
- `check:nav`、`check:contrast`、`check:slop`
- `test:e2e` 在真实 Chromium 中验证路由、主题、焦点、渲染错误、租户隔离和旧地址。

任何数字、图表、状态和操作都必须能追溯到真实查询或 mutation。没有数据时不补数字；权限隐藏不能替代后端授权；本契约不允许通过前端改写 Go API、后端路由、查询参数或数据语义来解决展示问题。
