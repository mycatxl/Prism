# WP09 · 数据源、经节点检测与解锁检测

**前置**：WP08。**目标**：

- 建立可插拔的数据源框架，分三类：离线库、服务端在线查询、经节点查询；
- 接入 DNSBL 黑名单；
- 用规则驱动流媒体和 AI 服务的解锁检测；
- 所有结果写入 intel.db（WP08）。

IPPure 改为可批量、可持久化的经节点数据源（按用户要求）。

## 1. 接口（`internal/intel/providers/provider.go`）

```go
type Kind int
const (
    KindOffline  Kind = iota // 本地数据库查询，无网络
    KindOnlineIP             // 服务端按 IP 查询第三方 API（不经节点）
    KindViaNode              // 经被测节点访问"查询我自己"类接口
)

type Spec struct {
    ID, Name, Website, Terms string // Terms：条款与额度提示，显示在设置页
    Kind              Kind
    Profile           string        // 标准化版本，如 "proxycheck-v3-2"
    RequiresKey       bool
    DefaultEnabled    bool          // 无 Key 也能用时才可能为 true
    DefaultDailyLimit int           // 0 = 不限
    DefaultQPS        float64       // 0 = 不限
    BatchSize         int           // online-ip：一次 Lookup 最多几个 IP；1 = 不支持批量
    DefaultTTL        time.Duration
    SupportsIPv6      bool
}

type Result struct {
    Evidence *quality.Evidence  // status=ok 时非空
    Raw      []byte             // ≤ 32 KiB；为空表示不存
    Err      *ProviderError     // Code: PROVIDER_LIMIT|PROVIDER_AUTH|PROVIDER_UNAVAILABLE|PROVIDER_RESPONSE|UNSUPPORTED_IP|EGRESS_MISMATCH
}

type OfflineProvider interface { Spec() Spec; Lookup(ip netip.Addr) Result }
type OnlineProvider  interface { Spec() Spec; Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result }
type ViaNodeProvider interface { Spec() Spec; Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result }
```

- 数据源的生效设置由三部分合并得到：`Spec` 中的默认值、state.db 中的 `intel_provider_settings`（WP02），以及环境变量。修改 Key 后自动重算 `credential_id`，从而解除 paused 状态（WP08）。
- 环境变量只在**首次启动、数据库里还没有对应设置时**写入一次默认值，之后以数据库（设置页）为准。旧版 Prism 的变量按下表迁移：

  | 旧变量 | 新设置 |
  |---|---|
  | `PRISM_QUALITY_API_KEY` | proxycheck 的 Key |
  | `PRISM_QUALITY_DAILY_LIMIT` | proxycheck 的每日额度 |
  | `PRISM_ABUSEIPDB_API_KEY` | abuseipdb 的 Key |
  | `PRISM_QUALITY_ENABLED` | 运行时配置 `intel_enabled` |
  | `PRISM_QUALITY_WORKERS` | 运行时配置 `intel_node_workers` |
  | `PRISM_QUALITY_QUEUE_SIZE` | 忽略，并打印一次弃用告警 |

  新增变量：`PRISM_MAXMIND_ACCOUNT_ID`、`PRISM_MAXMIND_LICENSE_KEY`、`PRISM_IPINFO_TOKEN`、`PRISM_IPQS_API_KEY`、`PRISM_IPAPI_IS_API_KEY`。
- online-ip 类型的数据源，其 HTTP 客户端**不走代理节点**，也**不继承** `HTTP_PROXY` 环境变量。沿用现有 `NewProxyCheckProvider` 中的严格 Transport：禁止重定向、设置超时、限制响应大小，并且错误信息中不能带出包含 Key 的 URL。
- via-node 类型的数据源只通过被测节点的 `adapter.Outbound` 拨号，沿用现有 `fetchIPPure` 的写法：不走环境代理、不走路由、不走 bypass。

## 2. 证据字段扩展（`internal/quality/model.go` 中的 `Evidence`）

新增以下字段，全部为可选并带 `omitempty`：

```go
ASNNumber      int      `json:"asn_number,omitempty"`
City           string   `json:"city,omitempty"`
Region         string   `json:"region,omitempty"`
RegisteredCC   string   `json:"registered_country,omitempty"`
UsageType      string   `json:"usage_type,omitempty"`     // 数据源原始的用途类型
IsMobile       *bool    `json:"is_mobile,omitempty"`
IsResidential  *bool    `json:"is_residential,omitempty"`
FraudScore     *int     `json:"fraud_score,omitempty"`    // 数据源自己的 0–100 风险分（高 = 风险大）
DNSBLListed    []string `json:"dnsbl_listed,omitempty"`   // 命中的 zone
DNSBLChecked   []string `json:"dnsbl_checked,omitempty"`
```

保留原有的 `RiskScore`、`Signals`、`Native`、`TorRoles`、`AbuseConfidence` 等字段，保证旧的解码器测试继续通过。

## 3. 内置数据源

"默认额度"一律按保守值设定，用户可以在设置页调整。凡是涉及第三方的额度与条款，**实现时必须对照官网核实，并写进 `Terms` 字段**。

| id | 类型 | 需要 Key | 默认启用 | 默认日额度 / QPS | TTL | 输出 |
|---|---|---|---|---|---|---|
| `geo_country` | offline | 否 | 是 | 不限 | — | 国家（Resin 原有的 country.mmdb，保持地区过滤兼容） |
| `dbip_lite` | offline | 否 | 是 | 不限 | 30d | ASN、组织、城市、国家（DB-IP Lite mmdb，CC BY 4.0，每月更新；UI 中注明出处） |
| `maxmind_geolite2` | offline | 是（`account_id` 与 `license_key`） | 有 Key 时 | 不限 | 30d | ASN、城市、`registered_country` |
| `ipinfo_lite` | offline | 是（token） | 有 Key 时 | 不限 | 30d | ASN、国家 |
| `torproject` | offline | 否 | 是 | 不限 | 6h | Tor 角色（沿用现有的 Onionoo 名录实现） |
| `proxycheck` | online-ip | 可选 | 是 | 无 Key 时 80/天；有 Key 默认 900/天，**用户可调到套餐上限（最大 1,000,000）**；1 QPS | 24h | 风险分、类型、ASN、组织、代理/VPN/Tor/机房/被攻陷/爬虫/匿名信号、运营商、攻击历史（现有解码器） |
| `abuseipdb` | online-ip | 是 | 有 Key 时 | 900/天，1 QPS | 24h | 30 天举报置信度、举报数、举报人数（现有解码器） |
| `ipqs` | online-ip | 是 | 有 Key 时 | 150/天，1 QPS | 72h | fraud_score、proxy、vpn、tor、recent_abuse、bot_status、connection_type、ISP、ASN |
| `ipapi_is` | online-ip | 可选 | 否 | 500/天，1 QPS | 72h | is_datacenter、is_vpn、is_proxy、is_tor、is_abuser、company.type、asn |
| `dnsbl` | online-ip | 否 | 是 | 不限；每个 zone 5 QPS | 24h | 命中的黑名单 zone |
| `ippure` | via-node | 否 | 是 | 500/天；1 次/60 秒（全局） | 7d | fraudScore（→ `FraudScore`，仅 IPv4）、isResidential、isBroadcast（→ `Native`）、ASN、组织、国家（现有解码器） |
| `ip_api` | via-node | 否 | 是 | 不限；全局 5 QPS | 7d | 国家、城市、ISP、组织、AS、mobile、proxy、hosting |

### 各数据源要点

- **离线库下载**：统一放在 `$PRISM_CACHE_DIR/geo/`。
  - 每周检查一次更新；下载使用现有的 `netutil.RetryDownloader`（直连失败时可以借用节点下载，沿用 Resin GeoIP 的做法）。
  - 下载后校验：能成功打开 mmdb，并且对 `1.1.1.1` 能查到结果；校验通过后原子替换旧文件。
  - 各数据库的下载地址：
    - MaxMind：`https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz` 和 `.../GeoLite2-City/download?suffix=tar.gz`，使用 HTTP Basic 认证（account_id:license_key）；
    - DB-IP：`https://download.db-ip.com/free/dbip-asn-lite-YYYY-MM.mmdb.gz` 和 `dbip-city-lite-YYYY-MM.mmdb.gz`，按当月文件名下载，找不到时回退到上个月；
    - IPinfo Lite：`https://ipinfo.io/data/ipinfo_lite.mmdb?token=...`。
  - **实现时核对以上三个地址的当前格式**。
- **proxycheck**：删除 `PRISM_QUALITY_DAILY_LIMIT` 最大 900 的硬上限。无 Key 时仍然限制在 80 以内，因为匿名额度按源 IP 共享。支持批量查询时把 `BatchSize` 设为 N，这一点需要对照 v3 文档确认；未确认前按 1 处理。
- **dnsbl**：
  - 默认 zones：`zen.spamhaus.org`、`bl.spamcop.net`、`psbl.surriel.com`，可以在 `config_json.zones` 中修改。**不要**加入已经停止服务的 `dnsbl.sorbs.net`。
  - 查询 IPv4 反序的 A 记录；只要返回 `127.0.0.x` 就算命中。
  - 返回 `127.255.255.x` 表示查询被拒绝（例如 Spamhaus 拒绝经公共解析器的查询），此时记为 `error_code=DNSBL_REFUSED`，**不算命中**。
  - 解析器默认使用系统解析器，可以通过 `config_json.resolver`（形如 `udp://host:53`）指定。
  - UI 中提示：Spamhaus 免费使用仅限非商业、低流量场景，而且不能经公共 DNS 查询。
  - 暂不查 IPv6（记为 unsupported）。
- **ip_api**：免费版只能用 HTTP，且仅限非商业用途；每个源 IP 每分钟 45 次。由于经节点访问，占用的是**各节点出口 IP 自己的额度**，天然适合批量检测。
  - 请求地址：`http://ip-api.com/json/?fields=status,message,country,countryCode,regionName,city,isp,org,as,asname,mobile,proxy,hosting,query`
  - 字段映射：`query` 用于核对出口 IP；`proxy` 映射到 `Signals.Proxy`；`hosting` 映射到 `Signals.Hosting`；`mobile` 映射到 `IsMobile`；`as` 形如 `AS13335 Cloudflare, Inc.`，解析出 `ASNNumber`。
- **ippure**：
  - 删除只存内存的 `IPPureChecker` 缓存和 512 条上限，结果写入 intel.db。
  - 全局限速默认 1 次/60 秒，用户可以在设置页调整。
  - 设置页显示提示："IPPure 条款可能限制批量或系统性使用，请自行确认；Prism 默认低频率调用"。
  - 返回的 IP 与本次出口 IP 不一致时，记为 `EGRESS_MISMATCH`，不写入证据。
- **每个数据源都要有夹具测试**：把真实响应脱敏后保存到 `testdata/<id>/*.json`；解码器必须拒绝字段矛盾、越界和 IP 不匹配的响应（沿用现有 proxycheck 解码器的严格风格）。

## 4. 数据源设置 API

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/intel/providers` | 列出每个数据源的 `spec`、生效设置（**不含 Key**，只有 `has_key`）、今日用量、`next_allowed_at`、`blocked_until`、`paused`、`error_code`、`queued`、`running`、`failed` |
| `PATCH /api/v1/intel/providers/{id}` | 请求体：`{"enabled":bool, "api_key":string\|null, "daily_limit":int, "qps":number, "ttl":"24h", "config":{...}}`。`api_key` 为 `""` 表示清除，为 `null` 表示不修改 |
| `POST /api/v1/intel/providers/{id}/actions/test` | 用 `1.1.1.1`（online-ip）或任意一个健康节点（via-node）做一次真实查询，返回标准化结果。**会消耗 1 次额度**，接口文档中要写明 |
| `POST /api/v1/intel/providers/{id}/actions/resume` | 解除 paused 状态 |

## 5. 解锁检测（`internal/intel/checks/`）

### 5.1 规则格式（YAML）

- 内置规则通过 embed 打包在 `checks/builtin/*.yaml` 中。
- 用户可以把自己的规则放在 `$PRISM_STATE_DIR/checks.d/*.yaml`。与内置规则同 id 时，用户规则覆盖内置规则。每 60 秒检查一次文件变化并热加载。

```yaml
id: claude                 # [a-z0-9_]+，唯一
version: 3                 # 规则变更时递增；与已存结果版本不同则视为过期
name: Claude
category: ai               # ai|video|music|social|search|mail|other
enabled: true
ttl: 24h
timeout: 10s
steps:
  - id: home
    request:
      method: GET
      url: https://claude.ai/
      follow_redirects: false
      headers: {Accept-Language: "en-US,en;q=0.9", User-Agent: "<浏览器 UA，统一常量>"}
      max_body_bytes: 262144
  # 也支持 tcp 步骤：- id: smtp; tcp: {host: smtp.gmail.com, port: 25}
outcomes:                  # 按顺序匹配，第一条命中即采用
  - when: {step: home, header_contains: {Location: "app-unavailable-in-region"}}
    outcome: blocked
  - when: {step: home, status_in: [403, 503], body_contains: "challenge-platform"}
    outcome: captcha
  - when: {step: home, status_in: [200, 301, 302, 307]}
    outcome: available
default: unknown
region:                    # 可选：从某一步抓取地区码（两位大写字母）
  step: home
  body_regex: '"countryCode":"([A-Z]{2})"'
```

### 5.2 匹配器

每个 `when` 可以组合多个条件，条件之间为 AND：

- `step`
- `status_in`、`status_not_in`
- `header_contains: {Name: substr}`、`header_regex: {Name: re}`
- `body_contains`、`body_not_contains`、`body_regex`
- `redirect_host`（Location 头中的 host）
- `connected: true|false`（仅用于 tcp 步骤）
- `error: timeout|refused|tls|any`

### 5.3 执行

- 每个检测的每一步都用绑定被测节点的 `http.Client`（写法同 `fetchIPPure`）。
- 不自动跟随重定向（除非规则中写明）。
- 响应体读取不超过 256 KiB；整个检测的超时以规则中的 `timeout` 为准。
- 并发控制：同一节点同一时刻只跑 1 个检测；同一条规则全局并发不超过 `intel_check_concurrency_per_check`（默认 2）。

### 5.4 内置初始规则

下表中的判定信号来自社区常用检测脚本的公开做法。**上线前必须对每条规则抓取真实响应（至少包括可用地区和不可用地区两类），脱敏后作为夹具写成测试，并据此校准匹配器。** 规则文件里要注明校准日期。

| id | 判定信号（需校准） |
|---|---|
| `google_captcha` | `https://www.google.com/search?q=prism&hl=en`：状态码 429，或重定向到 `/sorry/` 时判为 captcha；200 判为 available |
| `youtube_premium` | `https://www.youtube.com/premium`（带 `Accept-Language: en`）：正文含 "Premium is not available in your country" 判为 blocked；地区从 `"countryCode":"XX"` 或 `"GL":"XX"` 中提取 |
| `netflix` | 请求一部 Netflix 自制剧和一部非自制剧的标题页（两个 `/title/<id>` 在校准时选定，写进规则注释）：两者都返回 200 为 available，只有自制剧返回 200 为 region_limited，返回 403 或被拦截为 blocked；地区从重定向路径 `/xx-en/` 中提取 |
| `chatgpt` | 先请求 `https://chatgpt.com/cdn-cgi/trace` 取 `loc`；再请求 `https://ios.chat.openai.com/`，正文含 `unsupported_country` 判为 blocked，含 `VPN` 判为 captcha |
| `claude` | 见 §5.1 的示例 |
| `gemini` | `https://gemini.google.com/`：以不可用提示或跳转作为 blocked 依据，**信号必须在校准时确定** |
| `tiktok` | `https://www.tiktok.com/`：地区从正文 `"region":"XX"` 中提取；被屏蔽页判为 blocked |
| `smtp25` | 用 tcp 步骤连接 `smtp.gmail.com:25`：能连上判为 available（出口允许 25 端口），否则判为 blocked |

- 可以通过 `PATCH /api/v1/intel/checks/{id}` 修改 `enabled`（写入 `intel_provider_settings`，provider_id 为 `check:<id>`）。
- `GET /api/v1/intel/checks` 列出所有规则的元信息、来源（builtin 或 user）和校准日期。

## 6. 测试

1. 每个数据源的解码器都要有夹具测试：成功、字段缺失、越界、IP 不匹配、429、401。
2. DNSBL：用本地 DNS 测试服务器（`miekg/dns` 起在 127.0.0.1）模拟命中、未命中和 `127.255.255.254`。
3. 离线库：用最小 mmdb 夹具（可以用 `maxmind/mmdbwriter` 在测试中生成）。
4. 检测引擎：
   - 用 httptest 服务器，配合 `testutil` 中的"直连 outbound"来模拟节点；
   - 覆盖每一种匹配器、默认结果、区域提取、超时、响应体上限；
   - 用户规则覆盖内置规则、热加载。
5. 内置规则：每条规则至少有 "available" 和 "blocked" 两个夹具，放在 `checks/testdata/<id>/`。

## 7. 验收

```sh
make verify
```

手工验收：在设置页填入 proxycheck 的 Key，对 20 个节点创建 `kind=full` 任务：

- intel.db 中的 `evidence`、`node_checks`、`ip_assessment` 都有数据；
- 重启后 `GET /api/v1/intel/ip/{ip}` 仍能返回完整证据。
