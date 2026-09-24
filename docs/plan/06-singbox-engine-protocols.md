# WP06 · sing-box 内核改造与协议扩展

**前置**：WP03。**目标**：

- 把 sing-box 运行时改为内嵌 `box.Box`（事实 F5、F6）；
- 支持 endpoint（WireGuard、OpenVPN、OpenConnect）和 detour 串接（ShadowTLS、`dialer-proxy`）；
- 接入 Snell v4；
- 补齐 tuic、hysteria、anytls、wireguard、ssh 分享链接，修复 SIP002 解析；
- 提供解析报告和能力接口；
- 建立协议矩阵测试。

mihomo 兜底在 WP07 实现。本 WP 只负责把"sing-box 无法表示"的节点标记出来，交给 WP07 或写入解析报告。

## 1. 节点文档格式（`RawOptions`）

### 1.1 两种形态

**形态 A：普通 sing-box outbound。** 保持原 JSON 不变，与 Resin 完全一致，哈希不变。

```json
{"type":"vless","tag":"jp-1","server":"1.2.3.4","server_port":443,"uuid":"...","tls":{...}}
```

**形态 B：信封格式。** 顶层含有 `"prism_node": 1` 的对象：

```json
{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"wg-jp","main":{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"...","peers":[{"address":"1.2.3.4","port":51820,"public_key":"...","allowed_ips":["0.0.0.0/0","::/0"]}]}}
{"prism_node":1,"engine":"singbox","kind":"chain","name":"ss-stls","main":{"type":"shadowsocks","method":"2022-blake3-aes-128-gcm","password":"...","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"1.2.3.4","server_port":443,"version":3,"password":"...","tls":{"enabled":true,"server_name":"cloud.tencent.com"}}]}
{"prism_node":1,"engine":"mihomo","kind":"proxy","name":"ssr-hk","proxy":{"type":"ssr","server":"...","port":443,"cipher":"aes-256-cfb","password":"...","obfs":"plain","protocol":"origin"}}
```

规则：

- `kind` 的取值：`endpoint`、`chain`（engine 为 singbox）；`proxy`（engine 为 mihomo）。单个 outbound **一律使用形态 A**，不允许用信封包装。
- `deps` 中每一项的 tag 必须依次为 `d0`、`d1`……`deps` 里的项可以是 outbound，也可以是 endpoint，按 §1.3 判定。`main` 和 `deps` 中的 `detour` 只能引用 `d*`。
- `name` 是显示名，**不参与哈希**。`main.tag` 也不参与哈希。

### 1.2 哈希规则（修改 `internal/node/hash.go`）

- 形态 A：保持现有逻辑（删除 `tag` 后对 JSON 做规范化）。
- 形态 B：删除顶层的 `name`、`main.tag` 和 `proxy.name` 后再规范化。`deps[i].tag` 固定为 `d<i>`，保留即可。
- 测试：
  - 形态 A 的哈希与上游实现逐字节一致（用上游的 hash 测试用例）；
  - 形态 B 中，仅改变 name 或 tag 时哈希不变，改变任意其他字段时哈希改变。

### 1.3 类型判定

- endpoint 类型集合：`wireguard`（仅在形态 B 中）、`openvpn-client`、`openconnect`、`tailscale`。
- 形态 A 中出现 `type=wireguard` 时，视为**旧版 WG outbound**，按 §3.1 转换为 endpoint 后再构建（这样旧数据的哈希不变，也能继续使用）。

### 1.4 `NodeEntry` 新增字段（`internal/node/entry.go`）

- `Engine string`：取值 `singbox` 或 `mihomo`。
- `Protocol`：取主类型，并统一为小写。对 mihomo 类型做归一化映射：`ss` → `shadowsocks`，`socks5` → `socks`，其余保持原样（例如 `ssr`、`mieru`、`vless`）。
- `Chain bool`。
- `ProtocolDetail string`：用于展示，例如 `shadowsocks+shadowtls`、`vless+xhttp`、`vless+reality`。

以上字段都在 `NewNodeEntry` 中通过解析 RawOptions 得到，无需持久化。

## 2. SingboxRuntime（替换 `internal/outbound/builder.go` 中手工拼装的服务图）

新建文件 `internal/outbound/singbox_runtime.go`，保留 `OutboundBuilder` 接口，也保留 `secure_dns.go`。

```go
type SingboxRuntime struct {
    ctx          context.Context
    inst         *box.Box
    logger       log.ContextLogger
    seq          atomic.Uint64
    endpoints    atomic.Int64
    maxEndpoints int64 // PRISM_MAX_ENDPOINTS，默认 256
}

func NewSingboxRuntime(cfg SingboxBuilderConfig) (*SingboxRuntime, error) {
    ctx := include.Context(context.Background())
    reg := service.FromContext[adapter.DNSTransportRegistry](ctx).(*dns.TransportRegistry)
    registerSecureDNSTransport(reg)
    specs, err := secureDNSTransportSpecsForUpstreams(cfg.DNSUpstreams)
    if err != nil { return nil, err }
    servers := make([]option.DNSServerOptions, 0, len(specs))
    for _, s := range specs {
        servers = append(servers, option.DNSServerOptions{Type: s.transportType, Tag: s.tag, Options: s.options})
    }
    inst, err := box.New(box.Options{Context: ctx, Options: option.Options{
        Log:   &option.LogOptions{Disabled: true},
        DNS:   &option.DNSOptions{RawDNSOptions: option.RawDNSOptions{Servers: servers, Final: secureDNSFailoverTransportTag}},
        Route: &option.RouteOptions{DefaultDomainResolver: &option.DomainResolveOptions{Server: secureDNSFailoverTransportTag}},
    }})
    if err != nil { return nil, err }
    if err := inst.Start(); err != nil { _ = inst.Close(); return nil, err }
    return &SingboxRuntime{ctx: ctx, inst: inst /* ... */}, nil
}
```

上面这段 DNS 配置已经实测通过（事实 F6）。

### `Build(raw json.RawMessage) (adapter.Outbound, error)` 的步骤

1. 解析形态 A 或 B。
2. 生成唯一 tag：`base := "n/" + hashHex[:16] + "/" + strconv.FormatUint(r.seq.Add(1), 10)`。
   - 必须带序号：`EnsureNodeOutbound` 可能并发构建同一个节点，而同名 tag 的 `Create` 会替换掉已有实例，导致落败方在 `Close` 时误删胜者。
3. 按类型分别处理：
   - **outbound**：`sJson.UnmarshalContext(r.ctx, raw, &opt)`（`opt` 类型为 `option.Outbound`），然后 `r.inst.Outbound().Create(r.ctx, r.inst.Router(), r.logger, base, opt.Type, opt.Options)`，最后用 `r.inst.Outbound().Outbound(base)` 取回实例。
   - **endpoint**：先检查 `endpoints < maxEndpoints`，超出时返回错误 `ENDPOINT_LIMIT: too many active endpoint nodes`。然后用 `option.Endpoint` 和 `r.inst.Endpoint().Create(...)` 创建，用 `r.inst.Outbound().Outbound(base)` 取回（该方法也会查 endpoint，事实 F7）。
   - **chain**：
     - 深拷贝 JSON，把所有值为 `d<k>` 的 `detour` 字段改写为 `base + "/d<k>"`；
     - 按顺序创建 `deps`，tag 为 `base+"/d<i>"`；
     - 最后创建 `main`，tag 为 `base`。
4. 任何一步失败，都要按逆序 `Remove` 已经创建的实例，再返回错误。
5. 返回句柄：

   ```go
   type singboxHandle struct {
       adapter.Outbound
       rt   *SingboxRuntime
       tags []string // 创建顺序
       isEP []bool
       once sync.Once
   }
   func (h *singboxHandle) Close() error // 按逆序调用 Outbound().Remove / Endpoint().Remove；endpoint 计数减一；幂等
   ```

   `OutboundManager.closeOutbound` 会通过 `io.Closer` 调用它。

### 其他要求

- `SingboxRuntime.Close()` 调用 `inst.Close()`。
- 删除 `builder.go` 中手工创建 endpoint、inbound、outbound manager 和 DNS 的代码。`NewSingboxBuilderWithConfig` 保留为调用 `NewSingboxRuntime` 的包装，以兼容现有调用方和测试。
- 保留原 builder 测试中的 DNS 行为断言，并迁移到新的运行时下验证。例如：域名形式的节点服务器地址经 DoH 解析；本地 bootstrap 生效。

## 3. WireGuard

### 3.1 统一转换为 endpoint

所有来源都输出形态 B，`kind` 为 `endpoint`。

| 来源字段（旧 sing-box outbound / Clash / Surge / URI） | endpoint 字段 |
|---|---|
| `local_address`，或 Clash 的 `ip`/`ipv6`，或 URI 的 `address=`（逗号分隔） | `address`：列表，自动补 `/32` 或 `/128` |
| `private_key`、`private-key`，或 URI 中 userinfo（需 URL 解码） | `private_key` |
| `server` + `server_port`，或 `server` + `port` | `peers[0].address` 和 `peers[0].port` |
| `peer_public_key`、`public-key`、`publickey=` | `peers[0].public_key` |
| `pre_shared_key`、`pre-shared-key`、`presharedkey=` | `peers[0].pre_shared_key` |
| `reserved`（`[1,2,3]`、`"1,2,3"` 或 base64 三字节） | `peers[0].reserved`（3 个 uint8） |
| `allowed-ips`，缺省时为 `0.0.0.0/0` 和 `::/0` | `peers[0].allowed_ips` |
| `persistent-keepalive` | `peers[0].persistent_keepalive_interval` |
| `mtu` | `mtu` |
| 旧格式中的 `peers[]` 或 Clash 的 `peers:` 列表 | 逐项映射到 `peers[]` |
| `system_interface`、`interface_name`、`gso` | 忽略（固定 `system=false`） |

- Clash 节点带有 `amnezia-wg-option` 时，sing-box 无法支持，交给 WP07（mihomo）处理。
- 分享链接：`wireguard://` 与 `wg://` 格式相同，都是 v2rayN 格式：
  ```text
  wireguard://<urlencoded private_key>@host:port?publickey=..&presharedkey=..&reserved=1,2,3&address=10.0.0.2/32,fd00::2/128&mtu=1280#name
  ```
- 测试：五种来源都能得到相同的 endpoint JSON；`Build` 成功；向不可达的对端拨号时返回超时错误，而不是 panic。

## 4. OpenVPN（`.ovpn` 转换为 `openvpn-client` endpoint，需要 `with_openvpn`）

### 4.1 输入识别

满足以下条件时，按 OpenVPN 配置文件解析：

- 内容不是 JSON，也不是 YAML；
- 存在形如 `^\s*remote\s+\S+` 的行；
- 同时存在 `<ca>` 内联块、`secret` 指令或 `<secret>` 块之一；
- 或者满足以下条件之一：存在 `^\s*client\s*$` 行、以 `dev tun` 开头的行、`<ca>` 块。

**批量导入**使用 Prism 捆绑格式（JSON）：

```json
{"prism_openvpn_bundle":1,"profiles":[{"name":"jp-1","ovpn":"<整份 .ovpn 文本>","username":"u","password":"p"}]}
```

### 4.2 指令映射

解析方式：逐行读取；`#` 和 `;` 开头为注释；`<tag>…</tag>` 为内联块；参数支持引号。

| .ovpn 指令 | openvpn-client 字段 |
|---|---|
| `remote host [port] [proto]`，可出现多次，也可以写在 `<connection>` 块中 | `servers[]`：`{server, server_port, network}`，默认端口 1194 |
| `proto udp/udp4/udp6/tcp/tcp-client/tcp4/tcp6` | `network`：`tcp-client` 映射为 `tcp` |
| `port N` | 作为未写端口的 remote 的默认端口 |
| `remote-random` | `remote_random: true` |
| `dev tun` / `dev tunN` | 允许（必须有）；**`dev tap*` 拒绝** |
| `<ca>` | `tls.certificate`（PEM 全文作为单个元素） |
| `<cert>`、`<key>` | `tls.client_certificate`、`tls.client_key` |
| `<tls-auth>` 加 `key-direction 0/1` | `tls.control_wrap = {type:"tls_auth", key:[PEM], direction: 0→"server", 1→"client"}` |
| `<tls-crypt>` / `<tls-crypt-v2>` | `tls.control_wrap.type = "tls_crypt"` / `"tls_crypt_v2"` |
| `<secret>` 或 `secret`（内联）加 `key-direction` | `mode:"static_key"`、`static_key`、`key_direction`；此模式下要求 `ifconfig a b`，映射为 `address:["a/32"]`、`peer_address:"b"` |
| `cipher X` / `data-ciphers A:B` / `data-ciphers-fallback X` / `auth X` | `cipher` / `data_ciphers:[A,B]` / `data_ciphers_fallback` / `auth` |
| `verify-x509-name NAME [name/name-prefix/subject]` | `tls.server_name` 和 `tls.server_name_type`（缺省为 `subject`） |
| `remote-cert-tls server` | `tls.remote_certificate_tls:"server"` |
| `tls-version-min V` / `tls-version-max V` / `tls-cipher X` | `tls.version_min` / `tls.version_max` / `tls.cipher` |
| `comp-lzo [x]` / `compress [alg]` / `allow-compression x` | `compression_lzo` / `compression` / `allow_compression` |
| `tun-mtu N` / `mssfix N` / `fragment N` | `mtu` / `mss_fix` / `fragment` |
| `keepalive a b`，或 `ping a` 加 `ping-restart b` | `ping_interval:"<a>s"`、`ping_restart:"<b>s"` |
| `reneg-sec N` | `renegotiate_interval:"<N>s"`（为 0 时设 `renegotiate_disabled:true`） |
| `pull-filter accept/ignore/reject "text"` | `pull_filters[]` |
| `route-nopull` | `route_no_pull:true` |
| `auth-user-pass` 加内联块 `<auth-user-pass>user\npass</auth-user-pass>`，或捆绑格式中的 username/password | `username` 和 `password` |
| `redirect-gateway`、`route`、`dhcp-option`、`block-outside-dns`、`nobind`、`persist-*`、`resolv-retry`、`verb`、`mute`、`script-security`、`up`、`down` | 忽略（写入解析报告的 `detail` 作为提示） |
| `pkcs12`、`http-proxy`、`socks-proxy`、`plugin`，或 `ca/cert/key/tls-auth/auth-user-pass` 后面跟**文件路径** | **拒绝**，原因写为 `UNSUPPORTED_FEATURE:<指令>`（文件路径类提示"请内联"） |
| 有 `auth-user-pass` 但没有提供凭证 | 拒绝，原因 `INVALID:credentials required` |

- 名称优先取 `profiles[].name`，其次取订阅名加第一个 remote 的主机名。
- 测试夹具放在 `internal/subscription/testdata/openvpn/*.ovpn`，至少覆盖：tls-crypt、tls-auth 加 key-direction 1、多个 remote 加 remote-random、tcp-client、static key、verify-x509-name、compress lz4-v2，以及三种拒绝场景（dev tap、外部文件、缺少凭证）。每个成功用例都要求 `Build` 成功（sing-box 会校验 CA，事实 F8）。

## 5. OpenConnect 与 sing-box `endpoints` 数组

- 解析 sing-box JSON 时，除 `outbounds` 外，还要读取顶层的 `endpoints` 数组。
- 其中类型为 `wireguard`、`openvpn-client`、`openconnect` 的项转换为形态 B 的 endpoint 节点。
- `tailscale`：当前构建不含 `with_tailscale`，写入解析报告，原因为 `ENGINE_NOT_BUILT:tailscale`。

## 6. detour 串接

1. **sing-box JSON**：某个 outbound 或 endpoint 的 `detour` 引用了同一文档中的另一个 tag 时，生成 chain 节点：
   - 递归收集依赖，最大深度 3，出现环时拒绝，原因为 `INVALID:detour cycle`；
   - 依赖按拓扑顺序重命名为 `d0…`，并改写所有引用；
   - 被引用的项如果本身也是可用的代理（即不是 `shadowtls`、`selector`、`urltest`、`direct`、`block`、`dns`），也会作为独立节点导入。
2. **Clash `ss` 加 `plugin: shadow-tls`**（plugin-opts 含 `host`、`password`、`version`）：
   ```text
   d0   = {"type":"shadowtls","tag":"d0","server":S,"server_port":P,"version":V,"password":PW,
           "tls":{"enabled":true,"server_name":host,"utls":{"enabled":true,"fingerprint":<client-fingerprint 或 "chrome">}}}
   main = {"type":"shadowsocks","method":M,"password":SSPW,"detour":"d0","server":S,"server_port":P}
   ```
   version 为 1 时不写 `password`。
3. **Surge `ss` 行中带 `shadow-tls-password`、`shadow-tls-sni`、`shadow-tls-version`**：按第 2 条处理。
4. **Clash `dialer-proxy: X`**，且 X 位于同一文件中：生成 chain，`d0` 为 X 转换后的 sing-box 对象。如果 X 无法用 sing-box 表示，写入报告，原因为 `UNSUPPORTED_FEATURE:dialer-proxy`。
5. 测试：chain 节点 `Build` 成功；向不可达地址拨号时，错误中**不能**包含 `outbound detour not found`。真实的端到端串接验证在 WP13。

## 7. Snell

- 在 `supportedOutboundTypes` 中加入 `snell`。
- Clash 和 Surge 的 snell 节点：`version` 为 4 时转换为：
  ```json
  {"type":"snell","version":4,"psk":..,"obfs_mode":<obfs-opts.mode 或 obfs>,"obfs_host":<obfs-opts.host 或 obfs-host>}
  ```
- version 为 1、2、3 或缺省时，sing-box 不支持，交给 WP07。
- sing-box JSON 中的 snell 按原样导入（仅 v4 和 v6）。
- 删除 Surge 解析器中对 `snell` 的主动拒绝（`parser.go:362`）。`ssr` 和 `naive` 仍然转给 WP07 或报告处理。

## 8. 分享链接补全（sing-box 路径）

| scheme | 映射 |
|---|---|
| `tuic://uuid:password@host:port?congestion_control=&udp_relay_mode=&alpn=h3,..&sni=&allow_insecure=1&disable_sni=1#name` | `{"type":"tuic","uuid","password","congestion_control","udp_relay_mode","tls":{"enabled":true,"server_name":sni,"alpn":[..],"insecure":bool,"disable_sni":bool}}` |
| `hysteria://host:port?protocol=udp&auth=&peer=&insecure=1&upmbps=&downmbps=&alpn=&obfs=xplus&obfsParam=#name` | `{"type":"hysteria","up_mbps","down_mbps","auth_str":auth,"obfs":obfsParam,"tls":{"enabled":true,"server_name":peer,"insecure","alpn":[..]}}`；`protocol` 不是 udp 时写入报告 `UNSUPPORTED_FEATURE:hysteria protocol=<x>` |
| `anytls://password@host:port?sni=&insecure=1&fp=#name` | `{"type":"anytls","password","tls":{"enabled":true,"server_name","insecure","utls":{"enabled":fp!="","fingerprint":fp}}}` |
| `wireguard://`、`wg://` | 见 §3 |
| `ssh://user:pass@host:port#name` | `{"type":"ssh","user","password"}`（URI 中的私钥不支持） |
| `ss://`（修复） | ① SIP002 中 `@host:port/?plugin=` 格式：解析前去掉 `?` 前面的 `/`；② 非 base64 的 userinfo 要做 URL 解码（修复 ss2022 密钥中 `%3D` 的问题）；③ `plugin` 参数值先 URL 解码，再按 `;` 拆分 |
| `ssr://`、`mierus://`，以及 `vless://` 中带 `type=xhttp`/`splithttp`，或 `encryption` 不为空也不为 `none` | 交给 WP07 |

每个 scheme 至少 3 个测试用例，其中包括一个非法用例（应写入报告，而不是 panic）。

## 9. 解析报告

```go
type ParseResult struct {
    Nodes   []ParsedNode  `json:"-"`
    Skipped []SkippedNode `json:"skipped"` // 最多 500 条，超出部分只计数
    Stats   ParseStats    `json:"stats"`
}
type ParseStats struct {
    Total      int            `json:"total"`
    Imported   int            `json:"imported"`
    Skipped    int            `json:"skipped"`
    ByEngine   map[string]int `json:"by_engine"`
    ByProtocol map[string]int `json:"by_protocol"`
}
type SkippedNode struct {
    Name   string `json:"name"`
    Type   string `json:"type"`
    Source string `json:"source"` // singbox|clash|surge|uri|ovpn|plain
    Reason string `json:"reason"` // UNSUPPORTED_PROTOCOL|UNSUPPORTED_FEATURE|ENGINE_NOT_BUILT|INVALID
    Detail string `json:"detail"`
}
```

- 新增 `subscription.ParseWithReport(data []byte) (ParseResult, error)`。原来的 `ParseGeneralSubscription` 保留，内部调用新函数，只返回 `Nodes`。
- 解析器的每个转换函数签名改为 `(ParsedNode, *SkippedNode, bool)` 或等价形式。**不允许再有静默丢弃**：凡是能识别出 type 却没有导入的节点，都必须出现在 `Skipped` 中。
- 订阅每次刷新后调用 `engine.SetSubscriptionParseReport(id, json)`（WP02）。
- 新增 API：
  - `GET /api/v1/subscriptions/{id}/parse-report`
  - `POST /api/v1/subscriptions/actions/preview-parse`：请求体 `{"content":"..."}` 或 `{"url":"..."}`；URL 使用现有下载器（沿用大小上限和超时）；返回 `ParseResult` 以及前 50 个节点的 `{name, engine, protocol, protocol_detail}`；**不落库**。

## 10. 能力接口

`GET /api/v1/system/capabilities` 返回：

```json
{"engines":[
  {"name":"singbox","version":"1.14.0","outbound_types":["anytls","http","hysteria","hysteria2","shadowsocks","shadowtls","snell","socks","ssh","trojan","tuic","vless","vmess"],"endpoint_types":["openconnect","openvpn-client","wireguard"]},
  {"name":"mihomo","built":true,"version":"1.19.31","fallback_types":["ssr","mieru","snell(v1-3)","vless(xhttp/encryption)","masque","trusttunnel","sudoku","shadowquic","gost-relay","wireguard(amnezia)","ss(restls/kcptun/gost-plugin)"]}
],"build_tags":["..."],"share_link_schemes":["vmess","vmess1","vless","trojan","ss","ssd","ssr","hysteria","hysteria2","hy2","tuic","anytls","wireguard","wg","ssh","socks","socks5","socks5h","http","https","tg","netch","mierus"],"file_formats":["singbox-json","clash-yaml","clash-json","surge","uri-lines","base64","plain-proxy-lines","ovpn","prism-openvpn-bundle"]}
```

- 列表写成代码中的常量。
- 加一个测试：遍历 `outbound_types` 和 `endpoint_types`，用 sing-box 注册表的 `CreateOptions(type)` 确认每个类型都存在。

## 11. 协议矩阵测试（`internal/outbound/protocol_matrix_test.go`，名称为 `TestProtocolMatrix`）

采用表驱动，每个用例包含：输入片段、期望 engine、期望 kind、期望 protocol，以及 `Build` 的期望结果（成功或错误码）。**不访问网络。** 至少覆盖以下用例：

| 输入 | 期望 |
|---|---|
| ss（AEAD、2022 原样、2022 百分号编码、SIP002 obfs 插件、v2ray-plugin） | singbox / outbound / 成功 |
| vmess（ws+tls、grpc）、vless（reality+vision、ws、grpc、httpupgrade）、trojan（ws）、hysteria2（salamander、端口跳跃） | singbox / 成功 |
| tuic、hysteria、anytls、ssh 的 URI 以及对应的 Clash 写法 | singbox / 成功 |
| socks5、http、https、`ip:port:user:pass` | singbox / 成功 |
| WireGuard 的五种来源（旧 outbound JSON、endpoint JSON、Clash、Surge、URI） | singbox / endpoint / 成功 |
| `.ovpn` 夹具（全部成功用例） | singbox / endpoint / 成功 |
| sing-box JSON 中 ss 加 detour 到 shadowtls；Clash ss 加 shadow-tls 插件；Clash `dialer-proxy` | singbox / chain / 成功，且拨号错误不含 "detour not found" |
| snell v4（Clash、Surge） | singbox / 成功 |
| ssr、mieru、vless-xhttp、vless-encryption、snell v3、wg amnezia、ss restls | 带 `with_mihomo`：mihomo / proxy / 成功（WP07）；不带：报告 `ENGINE_NOT_BUILT` |
| tailscale endpoint、naive | 报告 `ENGINE_NOT_BUILT:<x>` |
| 非法 URI | 报告 `INVALID`，不 panic |

## 12. 验收

```sh
make protocol-matrix
make verify
```

另外手工核对：导入一个包含上述各类节点的本地订阅，`GET /api/v1/subscriptions/{id}/parse-report` 的统计结果与预期一致。
