# Prism 来源与许可证

Prism 按 GPL-3.0-or-later 发行，完整条款见 `LICENSE`。派生源码保留其原始许可与版权声明。

## Resin 基线

- 上游仓库：Resinat/Resin。
- 固定提交：`9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`，2026-08-01。
- 原始许可：MIT，版权归 Resinat and contributors；全文保存在 `LICENSES/Resin-MIT.txt`。
- 继承范围：代理、调度、节点与订阅、探测、状态、指标、API，以及相关测试和 SQLite 迁移。
- 原参考副本和 Git 历史保留在 `references/Resin/`；Prism 应用源码位于 `cmd/prism/` 和 `internal/`。

## sing-box

- 固定版本：`v1.14.1`；依赖锁定见 `go.mod` / `go.sum`。
- 原许可声明为 GPL 第 3 版或更新版本，保存在 `LICENSES/sing-box-NOTICE.txt`。
- Prism 使用独立名称，不表示与 sing-box 或其作者存在官方关联。
- WireGuard 以 sing-box 的 `wireguard` endpoint 类型使用，所需 `github.com/sagernet/wireguard-go` 直接取自模块依赖，仓库内不再保留本地替换副本。

## mihomo（已评估，未引入）

- 候选版本：`github.com/metacubex/mihomo@v1.19.31`，GPL-3.0。
- **当前不引入。** 实测代价：单独引入它会额外拉进 51 个 `github.com/metacubex/*` 平行分支模块，其中 `gvisor`、`amneziawg-go`、`utls`、`fswatch` 与 sing-box 使用的 `github.com/sagernet/*` 实现功能重叠，等于在一个进程里装入两套用户态网络栈、两套 WireGuard 和两套 TLS 指纹库；依赖图会从当前 34 个网络栈相关模块膨胀到 179 个。另外方案自身记录了 mihomo 节点只能走系统解析器，无法遵循 `PRISM_NODE_DNS_UPSTREAMS`。
- 覆盖范围窄：它唯一不可替代的收益是 SSR 存量、Snell v1–v3、kcptun 与 VLESS XHTTP 这几类。
- 完整依据见 `docs/ENGINE_DECISIONS.md`。`with_mihomo` 构建标签的接缝保留在代码中，但该标签不属于任何默认构建。

前端依赖由 `internal/api/web/package-lock.json` 锁定。此文件记录主要继承来源；发布构建须同时提供对应源码、构建方法及实际构建包含的依赖许可材料。
