# Prism 应用基线

日期：2026-09-07。

- Resin 提交：`9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`。
- 原 Resin 模块声明：`go 1.25.5`；Prism 当前声明：`go 1.26.0`，由安全依赖的最低要求决定；本机验证工具链：Go 1.27.0。
- sing-box：`v1.12.21`。
- 标准构建标签：`with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy`。WireGuard 的用户态网络栈需要 `with_gvisor`，该要求由实例创建回归测试确认。
- 应用模块：`prism`，入口 `cmd/prism`。
- 前端：`web/`，通过 `prism/web` 嵌入生产构建；开发时仍可使用独立 Vite 服务。

迁出时复制了完整内部模块、主程序、既有测试及 SQL 迁移。357 处本地导入更新到新模块，依赖版本与 `go.sum` 未改变。原参考副本保留，应用后续改动在根目录的 `cmd/` 与 `internal/` 进行。

迁移前 `references/Resin/.env.example` 已有本地修改，未将其归因于本轮后端实现。完整项目与旧骨架备份见 `SESSION_RECOVERY.md`。

## 安全依赖与兼容性

后续安全扫描要求升级 x/net、x/crypto、x/text、gRPC、chi、compress 和 x/mod。当前包括 x/net v0.58.0、x/crypto v0.56.0、x/text v0.41.0、gRPC v1.82.1、chi v5.3.0、compress v1.18.7 和 x/mod v0.40.0；完整锁定版本以根目录 `go.mod` 为准。sing-box 本身保持 v1.12.21。审计范围与扫描结果见 [安全审计](AUDIT_2026-09-07.md)。

Go 1.27 下新版 x/net 默认使用新的 HTTP/2 包装实现，sing-box 1.12 的关闭逻辑依赖的私有连接池符号不再存在。构建采用 x/net 提供的 `http2legacy` 标签，选择同一已修复版本中的经典实现；未退回存在漏洞的依赖版本。该兼容标签也用于测试与漏洞扫描。

WireGuard 的新版本同时改变了 Bind 接口。为兼容现有 sing-box，根目录使用 `third_party/wireguard-go` 的本地替换，仅回移植关闭锁顺序修复；细节见其中的 `PRISM_PATCHES.md`。升级代理引擎时需要重新评估并移除这两项兼容措施。
