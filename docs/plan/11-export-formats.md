# WP11 · 导出（sing-box / mihomo / v2rayN / URI / CSV / JSON）与订阅输出

**前置**：WP06、WP07、WP10。**目标**：

- 把筛选后的节点（例如"纯净度 ≥ 90 的住宅节点"）一键导出为主流客户端能直接使用的格式；
- 可以生成带令牌的订阅链接，供客户端自动更新。

## 1. 包与入口

- 新建包 `internal/export`，统一入口：

  ```go
  func Export(nodes []Item, format string, opt Options) (body []byte, contentType string, report Report, err error)
  ```

- `Item` 包含：`Name`（按模板渲染后的名称）、`Doc`（节点文档，形态 A 或 B）、`Intel`（WP10 的节点 intel 摘要）。
- `Report` 记录导出结果：`{exported:int, skipped:[{name, reason}]}`。无法用目标格式表示的节点**跳过并记入报告**，不报错。

## 2. 各格式规则

### 2.1 `singbox`（JSON）

```json
{
  "outbounds": [
    {"type":"selector","tag":"PROXY","outbounds":["<name1>","<name2>","AUTO"]},
    {"type":"urltest","tag":"AUTO","outbounds":["<name1>","<name2>"],"url":"https://www.gstatic.com/generate_204","interval":"5m"},
    ...节点 outbound...
  ],
  "endpoints": [...WireGuard/OpenVPN/OpenConnect endpoint...]
}
```

- 形态 A 的节点：原样输出，并把 `tag` 设为节点名。
- 形态 B：
  - endpoint 节点放进 `endpoints`，`tag` 设为节点名；
  - chain 节点：deps 的 tag 改为 `<name>#d<i>`，并改写 detour 引用；deps 一并输出，但不加入 selector；
  - mihomo 节点：跳过，原因 `NOT_REPRESENTABLE:singbox`。
- 节点名如有重复，依次追加 ` #2`、` #3`。

### 2.2 `mihomo`（Clash Meta YAML）

```yaml
proxies: [...]
proxy-groups:
  - {name: PROXY, type: select, proxies: [AUTO, ...所有节点名]}
  - {name: AUTO, type: url-test, url: https://www.gstatic.com/generate_204, interval: 300, proxies: [...所有节点名]}
rules:
  - MATCH,PROXY
```

- mihomo 节点：直接输出原 `proxy` map，并把 `name` 设为节点名。
- sing-box 节点：通过**反向映射器** `internal/export/clashmap.go` 转换，至少支持下表中的类型；不支持的类型跳过，原因 `NOT_REPRESENTABLE:mihomo`。

  | sing-box | Clash 字段要点 |
  |---|---|
  | shadowsocks | `type: ss`、`cipher`、`password`、`plugin`/`plugin-opts`（obfs 与 v2ray-plugin 互转）、`udp-over-tcp` |
  | vmess | `uuid`、`alterId`、`cipher`、`tls`、`servername`、`skip-cert-verify`、`network` 与 `ws-opts`/`grpc-opts`/`h2-opts`/`http-upgrade`、`client-fingerprint` |
  | vless | 在 vmess 的基础上增加 `flow`，以及 `reality-opts{public-key,short-id}` |
  | trojan | `password`、`sni`、`skip-cert-verify`、`network` 与各传输选项 |
  | hysteria2 | `password`、`sni`、`obfs`、`obfs-password`、`ports`、`up`、`down` |
  | hysteria | `auth-str`、`up`、`down`、`obfs`、`sni`、`alpn` |
  | tuic | `uuid`、`password`、`congestion-controller`、`udp-relay-mode`、`alpn`、`sni` |
  | anytls | `password`、`sni`、`client-fingerprint` |
  | ssh | `username`、`password` |
  | socks、http | `type: socks5` 或 `type: http`，并带 `username`、`password`、`tls` |
  | wireguard endpoint | `type: wireguard`、`ip`、`ipv6`、`private-key`、`public-key`、`pre-shared-key`、`reserved`、`allowed-ips`、`mtu` |
  | snell v4 | `type: snell`、`version: 4`、`psk`、`obfs-opts` |
  | openvpn-client endpoint | `type: openvpn`，字段按 mihomo `OpenVPNOption` 反向映射：`ca`、`cert`、`key`、`tls-auth`、`key-direction`、`tls-crypt`、`cipher`、`data-ciphers`、`auth`、`username`、`password`、`proto`、`server`、`port` |
  | chain（ss 加 shadowtls） | `type: ss` 加 `plugin: shadow-tls`，`plugin-opts{host, password, version}` |
  | 其余 chain | 跳过，原因 `NOT_REPRESENTABLE:mihomo(chain)` |

### 2.3 `v2rayn`（对 URI 行整体做 base64）与 `uri`（明文 URI 行）

- 支持的类型与写法：

  | 类型 | 写法 |
  |---|---|
  | vmess | v2rayN JSON v2：`{"v":"2","ps","add","port","id","aid","scy","net","type","host","path","tls","sni","alpn","fp"}`，做 base64 |
  | vless、trojan | 标准 query：`type`、`security`、`sni`、`fp`、`pbk`、`sid`、`flow`、`path`、`host`、`serviceName`、`alpn`、`allowInsecure` |
  | ss | SIP002：userinfo 为 `base64url(method:password)`；有插件时写成 `/?plugin=` |
  | hysteria2 | `hysteria2://` |
  | hysteria | `hysteria://` |
  | tuic | `tuic://` |
  | anytls | `anytls://` |
  | wireguard | `wireguard://`（与 WP06 §3 的格式相同） |
  | socks | `socks://` |
  | http | `http://`、`https://` |
  | ssr | mihomo 节点，输出 `ssr://` |

- **与解析器互为逆运算**：对每种类型 T 执行"导出 → 再解析"，得到的节点哈希必须与原节点相同。测试逐一覆盖。
- 无法写成分享链接的类型（openvpn、openconnect、chain、mieru 等）：跳过。

### 2.4 `csv` 与 `json`（用于分析，不是客户端配置）

- 列：name、engine、protocol、protocol_detail、subscription、egress_ipv4、egress_ipv6、colo、country、city、asn、as_org、ip_type、native、purity_score、purity_band、confidence、verdict、flags，以及每个检测的结果、latency_ms、healthy、assessed_at。
- **不包含任何节点凭证**。

## 3. 名称模板

- 默认模板为 `{name}`。可用变量：`{name}`、`{flag}`（国旗 emoji）、`{country}`、`{city}`、`{asn}`、`{org}`、`{ip_type}`、`{purity}`、`{band}`、`{verdict}`、`{engine}`、`{protocol}`、`{latency}`、`{index}`。
- 未知值渲染为空字符串；渲染后压缩多余空格，并截断到 64 个字符。

## 4. API

### 4.1 一次性导出（管理员）

```text
GET /api/v1/nodes/export?format=singbox|mihomo|v2rayn|uri|csv|json&name_template=...&<WP10 中所有节点筛选参数>&healthy_only=true&limit=5000
```

- 使用 `Content-Disposition: attachment` 下载。
- 报告放在响应头中：`X-Prism-Export-Exported: N`、`X-Prism-Export-Skipped: M`。
- json 格式把报告放在响应体的 `report` 字段中。

### 4.2 订阅配置（管理员）

- `GET`、`POST /api/v1/export-profiles`；`GET`、`PATCH`、`DELETE /api/v1/export-profiles/{id}`；`POST /api/v1/export-profiles/{id}/actions/rotate-token`。
- 请求体：`{"name", "format", "platform_id", "filter":{同导出筛选}, "name_template", "enabled"}`。
- 创建或轮换令牌时，在响应中**只返回这一次**订阅地址 `"url": "<scheme>://<host>/sub/<token>"`，服务端只保存令牌的 sha256。

### 4.3 公开订阅（在主监听上注册，不经过管理员鉴权）

```text
GET /sub/{token}
```

1. 计算令牌的 sha256，查找对应配置；找不到或配置已停用时返回 404（不区分两种原因）。
2. 限流：每个令牌每分钟 60 次，每个 IP 每分钟 120 次；超出时返回 429。
3. 如果设置了 `platform_id`，只导出该平台当前可路由视图中的节点，并叠加 `filter`。
4. 更新 `last_access_at` 和 `access_count`，并写审计日志（actor 记为 `export:<id>`）。
5. 响应头：`Cache-Control: no-store`、`Content-Type` 按格式设置。v2rayN 格式额外带 `Profile-Update-Interval: 12`，mihomo 格式额外带 `Content-Disposition: inline; filename=prism.yaml`。

- 接入点（Endpoint）如果关闭了 `allow_management`，同样**不提供** `/sub/*`（与管理面一致）。

## 5. 测试

- 每种格式：
  - 与 golden 文件比对；
  - sing-box 格式输出后，能用 `option.Options` 反序列化并 `box.New` 成功（不启动）；
  - mihomo 格式输出后，每个 proxy 都能被 `adapter.ParseProxy` 解析（`with_mihomo`）；
  - v2rayN 格式与解析器互为逆运算（哈希一致）。
- 跳过报告正确。
- `/sub/{token}`：错误令牌返回 404，停用后返回 404，限流返回 429，平台视图过滤生效，访问计数递增。

## 6. 验收

```sh
make verify
```

手工验收：

- 把导出的 mihomo YAML 导入 mihomo 客户端，把 sing-box JSON 用 `sing-box check` 校验，把 v2rayN 订阅导入 v2rayN，三者都能识别所有已导出的节点；
- 记录验证时使用的客户端版本。
