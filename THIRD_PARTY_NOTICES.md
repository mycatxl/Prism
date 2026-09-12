# Prism 来源与许可证

Prism 按 GPL-3.0-or-later 发行，完整条款见 `LICENSE`。派生源码保留其原始许可与版权声明。

## Resin 基线

- 上游仓库：Resinat/Resin。
- 固定提交：`9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`，2026-08-01。
- 原始许可：MIT，版权归 Resinat and contributors；全文保存在 `LICENSES/Resin-MIT.txt`。
- 继承范围：代理、调度、节点与订阅、探测、状态、指标、API，以及相关测试和 SQLite 迁移。
- 原参考副本和 Git 历史保留在 `references/Resin/`；Prism 应用源码位于 `cmd/prism/` 和 `internal/`。

## sing-box

- 固定版本：`v1.12.21`；依赖锁定见 `go.mod` / `go.sum`。
- 原许可声明为 GPL 第 3 版或更新版本，保存在 `LICENSES/sing-box-NOTICE.txt`。
- Prism 使用独立名称，不表示与 sing-box 或其作者存在官方关联。

前端依赖由 `web/package-lock.json` 锁定。此文件记录主要继承来源；发布构建须同时提供对应源码、构建方法及实际构建包含的依赖许可材料。

## WireGuard 兼容补丁

`third_party/wireguard-go/` 基于 `v0.0.1-beta.7`，保留原许可，仅回移植 `Device.Close` 的锁顺序修复。较新版本同时改变了 sing-box v1.12.21 使用的传输接口，因此暂以本地模块替换保持兼容。补丁范围和移除条件见该目录的 `PRISM_PATCHES.md`。
