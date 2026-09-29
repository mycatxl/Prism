# Prism 前端设计规范

这份文档是面板视觉与交互的唯一依据。任何页面的取舍以它为准；与它冲突的旧类名一律作废。

规范里的每条"禁止"都对应一个**可机械检查**的破绽，不是审美偏好。写完一页之后按本文末尾的
「验收」跑一遍，不靠眼睛判断。

## 概念

**校准台。** 主体是网络出口——流量从哪个地址出去、那个地址有多干净。所以界面按**测量仪器**做，
不按网站做：明亮的冷调台面、石墨色油墨、承担结构的是发丝细线而不是卡片和阴影、以及**一个饱和色**
只表示"这是活的通路"。

两个刻意的取舍：

- **没有深色模式。** 这些数值是在台面上被读的，旁边是终端和浏览器，白天光线下。第二套主题只是
  一次重映射，不是设计，而且会把每个对比度判断翻一倍，换不来可读性。
- **颜色从不单独传递信息。** 每个状态同时带一个词、一个形状或一个位置——状态列是操作员最先扫的地方。

## 令牌

全部定义在 `src/styles/design.css` 的 `@theme` 里，用 Tailwind 工具类消费。

### 颜色

| 令牌 | 值 | 用途 |
|---|---|---|
| `paper` | `#f2f4f7` | 台面底色。冷调中性，微蓝，**不是暖奶油** |
| `paper-sunk` | `#e6eaf0` | 凹陷区：表头、井、分栏的下沉半边 |
| `paper-raised` | `#ffffff` | 读数所在的纸面 |
| `paper-inset` | `#f8fafc` | 图表与地图的底色 |
| `rail` | `#e7ebf1` | **第二中性层**：导航与工具栏。比读数纸面更冷，框架和内容不会糊成一片 |
| `rule` / `rule-strong` / `rule-faint` | `#d2d8e1` / `#a6afbd` / `#e4e8ee` | 发丝细线；默认边框色取 `rule` |
| `ink` | `#0a0f16` | 正文 |
| `ink-soft` | `#39434f` | 次级文字 |
| `ink-faint` | `#566170` | 三级文字、单位、列头 |
| `signal` / `signal-deep` / `signal-wash` | `#0a6b52` / `#06483a` / `#dcefe8` | 健康、状态良好 |
| `live` / `live-wash` | `#0b5f8a` / `#ddebf5` | 在途、排队、进行中 |
| `warn` / `warn-wash` | `#7d4a00` / `#f7ecd5` | 降级、过期、需要看一眼 |
| `alert` / `alert-wash` | `#a32313` / `#f8e4e0` | 风险、失败、被阻断 |
| `accent` / `accent-deep` / `accent-wash` | `#0b4fa8` / `#073a7d` / `#e2ebf9` | **只有交互**：主操作、当前选中、焦点环 |

`signal` 与 `accent` 必须分开。合成一个色，在密集表格里"健康"和"可点"就长得一样了。

用法：`bg-paper`、`text-ink-soft`、`border-rule`、`bg-accent-wash`、`text-accent-deep`。

### 字体

| 令牌 | 字体 | 用途 |
|---|---|---|
| `font-sans` | **IBM Plex Sans**（400/500/600） | 所有界面文字 |
| `font-mono` | **IBM Plex Mono**（400/500/600） | **只给真正被当作数据读的值**：IP、哈希、延迟、计数、ASN、时间戳 |

**一个家族做界面，一个家族做测量。** 等宽不是"技术感"的戏服：给一段普通说明文字套等宽，是在
用字体代替信息。只加载用到的字重（`src/main.tsx` 里的 `@fontsource` 导入），700 已删除——没有任何
地方用它。

### 字号

`text-2xs` **11** · `text-xs` **12** · `text-sm` **14** · `text-base` **16** · `text-lg` **18** ·
`text-xl` **20** · `text-2xl` **24** · `text-3xl` **30** · `text-4xl` **36**（px）

两端的取舍是有意的：数据端窄（11/12/14），标题端宽（20/24/30/36）。**相邻档位至少差 1.125 倍**——
相邻两步看不出差别、却在干不同的活，就是一页"平"的界面。旧版把 11/12/13 三档用作三种角色，
这就是它读起来廉价的原因。

角色约定：

| 角色 | 字号 + 字重 |
|---|---|
| `.micro` 列头/分区标记 | 11px · 600 · `tracking-micro` · 全大写 |
| `.label` 元信息 | 12px · 500 · `ink-faint` |
| 表格正文、控件 | 14px · 400 |
| 面板标题 | 18px · 600 |
| 页标题 | 24px · 600 |
| 仪器读数 | 20px · 600 · `.readout`（等宽 + 表格数字） |

### 几何

- `rounded-control`（3px）：按钮、输入框、下拉、导航项
- `rounded-panel`（5px）：面板
- **全圆**（`rounded-chip`）：只有状态徽标
- **直角**：表格、细线、结构

### 动效

`--ease-instrument` = `cubic-bezier(0.16, 1, 0.3, 1)`。常规状态切换 110ms，面板/覆盖层 170–240ms。
**没有页面加载编排**：面板是加载进一个任务里，不是让人看它加载。

## 组件库

`src/components/ui/` 是唯一允许的组件来源。禁止自己写新的按钮/徽标/表格样式。

| 组件 | 用途 |
|---|---|
| `Button` | `variant`: primary（accent）/secondary/ghost/danger/quiet · `size`: sm/md/lg/icon · `asChild` |
| `Badge` | 状态。`tone`: neutral/signal/live/warn/alert/outline · `dot`（静态标记） |
| `Panel` / `PanelHeader` / `SectionTitle` | 区域。面板套面板时内层取消外框 |
| `Readout` | 单个测量值 |
| `Table` / `THead` / `TH` / `TBody` / `TR` / `TD` / `TDNum` / `TableWrap` | 数据表。`TH` 用 `.micro`；**只有行线，没有单元格边框** |
| `Input` / `Textarea` / `Select` / `Fieldset` | 表单 |
| `Switch` | 开关（Radix 承载键盘与 ARIA） |
| `Tabs` / `TabsList` / `TabsTrigger` / `TabsContent` | 标签页 |
| `Tooltip` / `TooltipProvider` | 提示 |
| `Sheet` | 右侧详情抽屉（Radix Dialog） |
| `LoadingState` / `ErrorState` / `EmptyState` | 三种状态，全局一致 |
| `Toast` | 操作反馈 |

**Radix 负责行为，本仓库负责外观**——这就是"用可复用的、安全的，不自己造轮子"的落点。

图表：需要坐标轴/图例/交互的用 **ECharts**（`echarts`），配色字面量集中在
`src/features/dashboard/chartPalette.ts`（canvas 读不到 CSS 变量，那份是 `design.css` 的副本，
必须同步改）。

## 布局

```
┌────┬──────────────────────────────────────────────┐
│ 轨 │ 顶栏：位置 · 实例状态 · 语言                 │
│ 道 ├──────────────────────────────────────────────┤
│    │                                              │
│ 图 │  内容区（左对齐，文本块约 65–75ch）           │
│ 标 │                                              │
│ +  │                                              │
│ 标 │                                              │
│ 签 │                                              │
└────┴──────────────────────────────────────────────┘
```

- 左轨道：`bg-rail`（第二中性层），图标 + 短标签，可折叠
- **当前目的地**用填充 + 字重 + 文字色一起标记。**不用彩色边条**——列表行上的彩色边条是最响的
  通用 UI 破绽，而且它说不出填充没说过的话
- 分区之间用细线，不用间距堆叠

## 写作

- **主动语态**。按钮说"保存更改"，就产出"已保存"。
- **按用户理解命名**，不按系统实现命名。
- **空状态是邀请**，不是情绪。失败状态**说明发生了什么、怎么修**，不道歉、不含糊。
- **句首大写**；全大写只留给 `.micro`（列头/分区标记），不做标题上方的装饰性小标签。
- 每个元素只做一件事。

## 禁止

这些是"生成感"的破绽，也是唯一一类能一句话说清、又能机械查出来的问题：

**页面骨架**

- 同尺寸"图标 + 标题 + 正文"卡片网格当页面结构；卡片套卡片
- **英雄数字模板**：大数字、小标签、辅助统计、强调色。四个数字占满一屏，读一次之后就只是被盯着看
- 标题上方的全大写小标签（kicker / eyebrow）
- 装饰性区段编号（01 / 02 / 03），除非序号本身携带读者需要的信息

**表面**

- 渐变文字（强调靠字重或字号）
- 把毛玻璃/模糊当装饰
- 圆角卡片上 >1px 的彩色 `border-left` / `border-right`
- 硬偏移阴影（`box-shadow: 4px 4px 0`）
- 零偏移彩色光晕/外发光
- 行内 sparkline、进度环、柔和阴影圆角矩形**代替内容**（形状读不出数值，却把数值挤掉）
- 用等宽字当"技术感"的戏服
- 用 Unicode 字形或 emoji 当图标系统
- 背景平铺装饰条纹或两轴网格（除非底下的东西本身就是画布、地图、图纸或量具）

**动效**

- 装饰性脉冲状态点、闪烁光标、跑马灯
- 每个区段都套同一个淡入上滑
- 图片在 hover 时缩放或旋转

## 质量底线（是底线，不是特性）

| 底线 | 怎么验 |
|---|---|
| 正文与占位符对比度 ≥ 4.5:1；大字号 ≥ 3:1；控件/图标/焦点环 ≥ 3:1 | `npm run check:contrast`（读 `design.css` 计算，失败即非零退出） |
| 键盘焦点可见 | `:focus-visible` 用 `accent` 描边 2px + 偏移 1px |
| 尊重 `prefers-reduced-motion` | `design.css` 末尾的媒体查询；减的是位移，不是有意义的状态反馈 |
| 浏览器表面也属于设计系统 | 选中色、插入符、滚动条、下划线偏移、`tabular-nums` 都在 `design.css` 的 base 层 |
| 每个交互组件都有 default/hover/focus/active/disabled/loading/error | 组件库里；缺一半不算完成 |
| 响应式到移动端 | 结构变化（导航收成抽屉、表格横向滚动），不是流式字号 |

第三方检查器：<https://github.com/pbakaus/impeccable> 的确定性检测器（61 条反模式 + DOM/几何阈值）
可以对本仓库的页面直接跑：

```bash
# WSL 里，需先取得引擎（launcher 会自动下载并校验 sha256）
IMPECCABLE_SKILL_DIR=$HOME/impeccable/skill \
  sh $HOME/impeccable/skill/scripts/impeccable detect \
  --viewport 1920x1080 http://127.0.0.1:2460/ui/dashboard
```

exit 0 = 无发现，exit 2 = 有发现。**先确认它能报错再相信它报 0**：拿一个故意违规的页面喂它，
它应当报出 `side-tab`、`gradient-text`、`dark-glow`、`pulsing-dot`、`icon-tile-stack`、
`cream-palette` 等条目。引擎在 `crates/` 里带源码，可用 `cargo build --release -p impeccable-cli` 自建。
