# Prism 应用基线

日期：2026-09-24。

- Resin 提交：`9b8ef8e5cf83071fbac4de29bd7187268b9cff7b`。
- 原 Resin 模块声明：`go 1.25.5`；Prism 当前声明：`go 1.26.0`，由安全依赖的最低要求决定。
- sing-box：`v1.14.1`（1.14 系列最新稳定补丁版；1.15.0 仍在 alpha，不采用）。
- mihomo：`v1.19.31`（模块 `github.com/metacubex/mihomo`，GPL-3.0）。**不引入**：见
  `docs/ENGINE_DECISIONS.md` 的 D-1，`go.mod` 不含该模块，只有 `internal/node/mihomo_built.go` /
  `mihomo_notbuilt.go` 保留构建标签接缝。
- 构建标签：
  - 基础集 `TAGS_BASE`：`with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy`。
  - 完整集 `TAGS_FULL`：当前与 `TAGS_BASE` 相同（`Makefile`、`.github/workflows/release.yml`、
    `Dockerfile` 三处一致），也是默认构建标签；`with_mihomo` 不在其中。
  - 默认不启用 `with_embedded_tor`、`with_naive_outbound`、`with_tailscale`。
- 应用模块：`prism`，入口 `cmd/prism`。
- 前端：`internal/api/web/`，由 `internal/api` 通过 `//go:embed all:web/dist` 嵌入生产构建；开发时仍可使用独立 Vite 服务。

迁出时复制了完整内部模块、主程序、既有测试及 SQL 迁移。357 处本地导入更新到新模块。原参考副本保留，应用后续改动在根目录的 `cmd/` 与 `internal/` 进行。

## 依赖版本说明

- `github.com/sagernet/gvisor` 固定为 `v0.0.0-20260727.0-sing-box-mod.1`。该伪版本的数字预发布标识在 semver 规则下排序低于更早的 hash 型伪版本，MVS 不会自动选中，因此必须显式 require；`go.mod` 中有对应注释。
- `github.com/sagernet/wireguard-go` 直接使用模块依赖版本，不再使用 `third_party/` 本地替换。sing-box 1.14 的 WireGuard endpoint 需要新接口，旧的 beta.7 补丁副本已删除，相关失效说明见下。
- 升级任一内核都必须通过 `make protocol-matrix`。

## 安全依赖与兼容性

后续安全扫描要求升级 x/net、x/crypto、x/text、gRPC、chi、compress 和 x/mod。完整锁定版本以根目录 `go.mod` 为准。

Go 1.27 下新版 x/net 默认使用新的 HTTP/2 包装实现，sing-box 的关闭逻辑依赖的私有连接池符号不再存在。构建采用 x/net 提供的 `http2legacy` 标签，选择同一已修复版本中的经典实现；未退回存在漏洞的依赖版本。该兼容标签也用于测试与漏洞扫描。
