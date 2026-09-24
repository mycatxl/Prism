# Prism v3 实施方案（交给执行 AI 使用）

本目录是一套**可直接粘贴给其他 AI 执行**的实施方案。方案覆盖以下内容：

- 修复审计中发现的全部问题（见 `docs/FEATURE_AUDIT_2026-09-24.md`）；
- 全量继承 Resin 功能；
- 扩展协议：WireGuard、OpenVPN、OpenConnect、ShadowTLS 串接、Snell，并补全分享链接格式；
- 只有 sing-box 支持不了的 Clash 系协议才交给 mihomo 兜底；
- 导入节点后自动批量采集 IP 信息、批量测纯净度，结果**全部持久化到 SQLite**；
- 导出为 sing-box、mihomo 和 v2rayN 格式。

方案中的关键技术点都已经在 2026-09-24 实测验证（见 `00-overview.md` §3）。执行 AI 不应推翻这些结论，除非给出新的实测证据。

## 使用方式

1. **每次开新会话，都先粘贴 `00-overview.md`**。它是全局约束、已核实事实和设计决策，所有工作包都依赖它。
2. 然后**按顺序**粘贴一个工作包（WP），让 AI 完成并通过该 WP 的验收命令，再进行下一个。
3. 如果对方 AI 的上下文足够大，也可以一次粘贴 `PRISM_PLAN_FULL.md`（由各分册拼接而成，内容相同）。
4. 每个 WP 完成后，要求 AI 输出：改动清单、验收命令的运行结果，以及偏离方案之处和原因。

## 工作包一览

| WP | 文件 | 标题 | 依赖 | 规模 |
|---|---|---|---|---|
| 01 | `01-build-and-repo.md` | 仓库与构建修复 | — | 小 |
| 02 | `02-state-layer.md` | 持久化层重建与迁移 | 01 | 中 |
| 03 | `03-entrypoint-runtime.md` | 程序入口与运行时组装（Resin 全量） | 02 | 中 |
| 04 | `04-regressions-security.md` | 回归修复与安全整改 | 03 | 中 |
| 05 | `05-resin-compat.md` | Resin 兼容层与测试全量移植 | 03 | 中 |
| 06 | `06-singbox-engine-protocols.md` | sing-box 内核改造与协议扩展 | 03 | 大 |
| 07 | `07-mihomo-fallback.md` | mihomo 兜底（仅限 sing-box 不支持的 Clash 系协议） | 06 | 小 |
| 08 | `08-intel-store-jobs.md` | intel.db 持久化与批量任务系统 | 03 | 大 |
| 09 | `09-intel-providers-checks.md` | 数据源、经节点检测与解锁检测 | 08 | 大 |
| 10 | `10-purity-policy.md` | 纯净度评估、平台质量准入与轮换 | 09 | 中 |
| 11 | `11-export-formats.md` | 导出 sing-box / mihomo / v2rayN 与订阅输出 | 06, 07, 10 | 中 |
| 12 | `12-frontend.md` | 前端改造 | 04–11 | 大 |
| 13 | `13-testing-release-docs.md` | 端到端测试、文档与发布 | 全部 | 中 |

推荐顺序：01 → 02 → 03 → 04 → 05 → 06 → 07 → 08 → 09 → 10 → 11 → 12 → 13。其中 05 与 06、08 可以并行，但必须由同一个 AI 会话合并。

## 给执行 AI 的开场提示词（可直接复制）

```text
你是 Prism 项目的实现工程师。Prism 是基于 Resin（github.com/Resinat/Resin，提交 9b8ef8e）二次开发的代理池网关，
Go 模块名为 prism，前端位于 internal/api/web（React 19 + TypeScript + Vite）。
我会先给你《00-overview.md》（全局约束与设计决策），再逐个给你工作包（WP）。

执行规则：
1. 严格按工作包实现，不要扩大范围；方案没写的细节，优先保持 Resin 原行为，其次选最简单的实现，并在总结中说明。
2. 不得删除或跳过任何测试；每个 WP 结束时必须运行该 WP 的「验收」命令，并贴出结果。
3. 单元测试不得访问公网，外部 HTTP 一律使用 httptest 和 testdata 夹具。
4. 代理热路径（internal/proxy、internal/routing）不得访问数据库或外部网络。
5. 不要在日志、API 响应、提交信息中输出任何密钥或节点凭证。
6. 完成后输出：改动文件清单、验收结果、与方案的偏差及原因。
准备好后回复「已读 00-overview」，然后等待第一个工作包。
```
