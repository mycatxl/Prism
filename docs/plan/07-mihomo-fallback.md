# WP07 · mihomo 兜底（仅限 sing-box 不支持的 Clash 系协议）

**前置**：WP06。**原则**：能用 sing-box 的节点**一律**用 sing-box。mihomo 只接管下面判定表列出的情况，**没有任何配置项**。进程内调用，不做 sidecar。代码放在构建标签 `with_mihomo` 下（完整版默认开启）。

## 1. 依赖

```sh
go get github.com/metacubex/mihomo@v1.19.31
```

- 已实测可以与 sing-box 1.14 共存（事实 F11）。
- 在 `THIRD_PARTY_NOTICES.md` 中登记：GPL-3.0，与 Prism 的许可证兼容。
- mihomo 的包只能被带 `//go:build with_mihomo` 的文件引用，确保 `make backend-lite` 编译出的二进制不包含 mihomo。

## 2. 使用 mihomo 的判定表（唯一依据）

一个节点当且仅当满足下列**任意一条**时，交给 mihomo：

| 条件 | 说明 |
|---|---|
| Clash `type` ∈ {`ssr`, `mieru`, `masque`, `trusttunnel`, `sudoku`, `shadowquic`, `gost-relay`} | sing-box 1.14 不支持这些类型 |
| Clash `type=snell` 且 `version` ∉ {4} | sing-box 只支持 Snell v4 和 v6（事实 F9）；Clash 一般不写 v6 |
| Clash `type=vless` 且（`network` ∈ {`xhttp`, `splithttp`}，或 `encryption` 不为空且不为 `none`） | XHTTP 传输和 VLESS 加密 |
| Clash `type=ss` 且 `plugin` ∈ {`restls`, `kcptun`, `gost-plugin`} | `shadow-tls` 插件走 sing-box 串接（WP06 §6），**不在此列** |
| Clash `type=wireguard` 且带有 `amnezia-wg-option` | AmneziaWG |
| 分享链接 `ssr://`、`mierus://`；`vless://` 带 `type=xhttp` 或 `splithttp`，或 `encryption` 不为空也不为 `none` | 用 `convert.ConvertsV2Ray([]byte(line))` 转成 map（事实 F13） |

**明确不走 mihomo 的情况**：`openvpn`、`wireguard`（普通）、`tailscale`、`hysteria`、`hysteria2`、`tuic`、`anytls`、`vmess`、`trojan`、`ss`（普通或 shadow-tls）、`ssh`、`socks`、`http`。这些类型如果 sing-box 转换失败，就写入报告（原因 `INVALID`），**不要**退回到 mihomo 重试。

## 3. 解析器接线（`internal/subscription`）

- 在 Clash 转换 `convertClashProxyToNode` 的最前面调用 `needsMihomo(proxy map[string]any) (bool, string)`（实现 §2 的判定表）：
  - 返回 true 且带 `with_mihomo` 时：输出形态 B `{"prism_node":1,"engine":"mihomo","kind":"proxy","name":<name>,"proxy":<原 map，去掉 name>}`；
  - 返回 true 但没有 `with_mihomo` 时：写入报告，原因 `ENGINE_NOT_BUILT:mihomo`，detail 填判定原因。
- Surge 行先转换成 Clash map（现有逻辑），再走同一个判定。
- 分享链接行：`ssr://` 和 `mierus://` 直接走 `ConvertsV2Ray`；`vless://` 先检查 query 中的 `type` 和 `encryption`，命中判定表时才走 `ConvertsV2Ray`，否则走现有的 sing-box 解析。
- 构建标签的拆分：`needsMihomo` 和判定表**不依赖** mihomo 包，所以写在普通文件里。只有 `ConvertsV2Ray` 和 `ParseProxy` 的调用放在带 `with_mihomo` 标签的文件中，另外提供一个 stub 文件（`//go:build !with_mihomo`），其中函数返回 `errMihomoNotBuilt`。

## 4. 运行时：mihomo 节点适配器（`internal/outbound/mihomo_outbound.go`，`//go:build with_mihomo`）

```go
type mihomoOutbound struct {
    tag   string
    typ   string   // 归一化后的协议名，例如 "ssr"
    proxy C.Proxy  // github.com/metacubex/mihomo/constant
    once  sync.Once
}

func buildMihomo(tag string, m map[string]any) (adapter.Outbound, error) {
    p, err := mihomoadapter.ParseProxy(m) // github.com/metacubex/mihomo/adapter
    if err != nil { return nil, fmt.Errorf("mihomo: %w", err) }
    return &mihomoOutbound{tag: tag, typ: normalize(m["type"]), proxy: p}, nil
}

func (o *mihomoOutbound) Type() string           { return o.typ }
func (o *mihomoOutbound) Tag() string            { return o.tag }
func (o *mihomoOutbound) Dependencies() []string { return nil }
func (o *mihomoOutbound) Network() []string {
    if o.proxy.SupportUDP() { return []string{N.NetworkTCP, N.NetworkUDP} }
    return []string{N.NetworkTCP}
}
func (o *mihomoOutbound) DialContext(ctx context.Context, network string, dst M.Socksaddr) (net.Conn, error) {
    if N.NetworkName(network) != N.NetworkTCP { return nil, E.New("mihomo outbound: only tcp is supported") }
    md := &C.Metadata{NetWork: C.TCP, Type: C.INNER, DstPort: dst.Port}
    if dst.IsFqdn() { md.Host = dst.Fqdn } else { md.DstIP = dst.Addr.Unmap() }
    return o.proxy.DialContext(ctx, md) // C.Conn 实现了 net.Conn
}
func (o *mihomoOutbound) ListenPacket(ctx context.Context, dst M.Socksaddr) (net.PacketConn, error) {
    return nil, E.New("mihomo outbound: udp is not supported by prism") // Prism 只转发 TCP
}
func (o *mihomoOutbound) Close() error { var err error; o.once.Do(func() { err = o.proxy.Close() }); return err }
```

- `SingboxRuntime.Build` 遇到 `engine=mihomo` 时，转交给 `buildMihomo`（带标签时），否则返回 `errMihomoNotBuilt`。更简洁的做法是新建一个 `CompositeBuilder`，按 engine 分发给两个实现。
- **DNS**：mihomo 节点中域名形式的服务器地址使用系统解析器，**不经过** `PRISM_NODE_DNS_UPSTREAMS`。这是已知限制，写入 `docs/PROTOCOLS.md`。不要为此初始化 mihomo 的全局 resolver。
- **不初始化** mihomo 的 tunnel、全局配置或统计模块，只使用 `adapter.ParseProxy`。
- mihomo 节点内的 `dialer-proxy` 不支持：在解析阶段写入报告，原因 `UNSUPPORTED_FEATURE:dialer-proxy(mihomo)`。

## 5. 测试（带 `with_mihomo` 标签，不访问网络）

1. `TestProtocolMatrix` 中 mihomo 相关的行（WP06 §11）全部通过：构建成功，`Type()` 返回值正确，`Close()` 幂等。
2. 判定表单元测试：每一行至少一个命中用例和一个"相近但不命中"的用例。例如 `vless` 加 `encryption=none` 应走 sing-box；`ss` 加 `shadow-tls` 应走 sing-box 串接。
3. 拨号测试：用 `net.Listen` 起一个本地监听，它接受连接后立即关闭。mihomo 的 `socks5` 类型本身不在兜底列表中，但可以在**测试中直接调用** `buildMihomo`，用它连接本地的 SOCKS5 测试服务器（testutil），验证适配器的数据通路。
4. 精简版（不带标签）：同样的输入全部进入报告，原因为 `ENGINE_NOT_BUILT:mihomo`，而且精简版二进制中不包含 mihomo 的包：

   ```sh
   go list -deps -tags "$(TAGS_BASE)" ./cmd/prism | grep -c metacubex
   # 结果必须为 0
   ```

## 6. 验收

```sh
make protocol-matrix
make verify
make backend && make backend-lite && ls -la bin/   # 记录两个变体的体积
```
