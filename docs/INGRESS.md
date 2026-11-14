# 管理面、数据面与 Tunnel/L4 边界

Prism 可在同一端口识别多个协议，也可在接入点页面为不同端口选择协议组合。对公网部署，应分别安排管理 HTTP 与代理 TCP 的入口。

```text
浏览器 / 管理 API / 支付平台
        │ HTTPS
        ▼
HTTP 反代或 Cloudflare Tunnel
        │ HTTP，可信代理补充 X-Forwarded-For / Proto
        ▼
127.0.0.1:2261  PRISM_ADMIN_LISTEN
        │ WebUI / API / healthz / 公开支付回调
        │
        ├────────── 同一个 Prism 进程 ──────────┐
        │                                     │
代理客户 ── 公网 TCP 或保留源地址的 L4 ──► :2262 接入点
                                              │ HTTP CONNECT / SOCKS5
                                              ▼
                                     鉴权 / 限额 / 租约 / 上游
```

## 端口与防火墙示例

以下只是配置示例，实际配置由运营者在自己的部署中应用：

```text
PRISM_LISTEN_ADDRESS=0.0.0.0
PRISM_PORT=2260
PRISM_ADMIN_LISTEN=127.0.0.1:2261
PRISM_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

主监听地址也用于后台创建的额外接入点。上面允许额外接入点绑定公网，同时**主端口 2260 也会绑定公网地址**。默认接入点是只读、不可删除的内置接入点，继续提供管理和代理路由；增加 `PRISM_ADMIN_LISTEN` 不会把管理路由从主端口移走。

| 端口 | 暴露策略 | 接入点策略 |
| --- | --- | --- |
| 2260，内置主端口 | 防火墙拒绝外部访问，保留本机诊断 | 内置协议组合与管理路由仍存在 |
| 2261，独立管理监听 | 只监听回环；Tunnel/HTTP 反代指向这里 | `/ui/`、`/api/`、`/healthz` |
| 2262，后台创建的数据接入点 | 仅向预期客户网络开放 TCP | `enabled=true`、`allow_management=false`、`allow_proxy=true`、`allow_http_forward=true`、`allow_socks5=true` |
| 可选 2263 | 需要单独区分客户或协议时才开放 | 单独选协议；同样关闭 `allow_management` |

数据接入点可按需求设置 `require_proxy_auth_info`；完整 proxy 范围 AccessKey 鉴权仍是数据面要求。通常关闭数据接入点的 `allow_http_reverse`，需要销售反向转发时再单独规划 TLS 与访问日志。操作系统防火墙、云安全组和 IPv6 的暴露策略必须一致。

SOCKS5 和普通 HTTP 代理入站没有内置 TLS。公网使用需安排受控网络或合适的加密封装，并验证来源地址和凭据传输边界。客户端到 Prism 的传输与 Prism 到上游节点的加密是两个独立边界。

## Tunnel 承载管理 HTTP

普通 Cloudflare Tunnel 的公开 HTTP/HTTPS 主机名适合 WebUI、API 和公开支付回调。管理员浏览器使用 HTTPS；反代/Tunnel 再连回环管理端口。支付通知路径 `/api/v1/public/ldc/notify` 是公开的 GET/POST 验签回调，不能被交互式登录或 WAF 验证挡住。

普通 HTTP 主机名 Tunnel 不能当作透明 SOCKS5 或 HTTP CONNECT 数据入口。额外建立 Prism 接入点也不会改变前置 Tunnel 的协议能力。客户代理流量必须能够直接到达 TCP 接入点，或经已验证的 L4/网络层方案到达。带专用客户端的隧道产品及其他 L4 产品需要单独验证客户端要求、协议能力和服务条款。

Prism 自身的端口协议识别可以混合 HTTP 与 SOCKS5。外部只按 TLS SNI 路由的设备无法从 SOCKS5 握手或裸 CONNECT 的首包取得 SNI；需要按端口分流或使用明确支持该协议识别的入站设备。

## 可信反代与真实客户端 IP

HTTP 请求先读取 TCP peer。只有 peer 在 `PRISM_TRUSTED_PROXIES` 内才采信 XFF，并从右向左选择第一个不受信的地址；跳过可信中间代理。全部地址受信、空头或只有无效地址时回退到 peer。重复 XFF 头合并处理；`CF-Connecting-IP` 不是单独的信任来源。

反代应覆盖或正确追加客户地址，不能把客户原样提供的 XFF 当作已核实来源。仅配置实际代理的精确地址/CIDR，避免将客户可直接进入的大网段设为可信。Cloudflared 从回环连入时，可使用上例的精确回环前缀。HTTPS 终止代理也应可靠设置 `X-Forwarded-Proto`，否则远程浏览器会话不能正确识别 HTTPS。

回归证据：`internal/api/client_ip_tunnel_test.go` 用实际 TCP peer 与两层 HTTP 反代验证公开 Host 保留、真实来源及伪造 XFF 丢弃；`internal/api/client_ip_parity_test.go` 验证管理失败限流器与代理解析一致；`internal/proxy/client_ip_test.go` 覆盖 IPv6、重复头和无效链。原始 SOCKS5 连接始终读取 TCP peer，证据见 `cmd/prism/client_ip_raw_test.go`。

这些是离线链路回归。真实公开域名上的 Tunnel、代理 ACL 和 HTTPS 仍需在获授权的部署中验证；本轮本地测试没有访问现有部署。

## L4 与 PROXY protocol

当前 Prism **不解析 HAProxy PROXY protocol v1/v2**。不要向监听器发送 `PROXY` 文本或 v2 二进制前缀；它们不是受支持的协议入口。

普通 nginx stream、HAProxy、Caddy L4 或云负载均衡若重新建立后端 TCP 连接，Prism 看到的是前置设备地址。SOCKS5 没有 XFF，HTTP CONNECT 的透明 TCP 转发也不会核实客户提供的 XFF。将这种设备列入 `PRISM_TRUSTED_PROXIES` 不能恢复来源，反而可能使 HTTP 客户提交的伪造头被信任。

来源丢失会影响 `request_logs.client_ip`、认证失败限流桶和来源 IP 白名单。当前应使用直接公网 TCP、保留原始源地址并正确安排回程的 DNAT/透明 L4，或明确接受来源丢失的限制。任何终止 TLS 的普通 L4 方案也要核实这一点。未来若实现 PROXY protocol，需要独立的可信 L4 peer 白名单、严格前缀解析及伪造前缀测试；这是后续代码能力，不是现有配置开关。
