# WP10 · 纯净度评估、平台质量准入与轮换增强

**前置**：WP09。**目标**：

- 用多来源证据计算可解释的纯净度评估（`prism-purity-v2`）；
- 让平台质量准入真正可用，默认失败即拒绝（fail-closed）；
- 在证据变化或过期时自动重新评估；
- 定时轮换支持"避开上一个出口 IP"。

## 1. 评估算法 `prism-purity-v2`（`internal/intel/assess`，纯函数）

```go
func Assess(ip netip.Addr, ev []quality.Evidence, now time.Time, enabled map[string]bool) Assessment
```

- 只使用**有效**证据：`status=ok` 且 `now < valid_until`。
- `enabled` 表示当前启用的数据源，用于计算覆盖率。
- 评估结果必须是确定的：相同输入得到相同输出，便于测试。

### 1.1 洁净分项（0–100，越高越干净）

| 分项 | 来源与换算 | 权重 |
|---|---|---|
| `proxycheck` | `100 - RiskScore` | 3 |
| `ippure` | `100 - FraudScore`（仅 IPv4） | 3 |
| `ipqs` | `100 - FraudScore` | 3 |
| `abuseipdb` | `100 - AbuseConfidence` | 2 |
| `ipapi_is` | 按标志换算：`is_abuser` 为 40，`is_proxy`、`is_vpn` 或 `is_tor` 为 60，都不是为 100 | 1 |
| `ip_api` | `proxy` 为 60，否则为 100 | 1 |
| `dnsbl` | `100 - 34 × 命中 zone 数`（下限 0）；只统计成功查询的 zone | 1 |

- 分数计算：`base = Σ(w_i × c_i) / Σ w_i`，只对有效分项求和。
- **扣分**：只要有任一有效来源给出某个标志，就在 base 上扣一次，不重复叠加。扣分结果截断到 0–100。

  | 标志 | 扣分 |
  |---|---|
  | compromised | −40 |
  | tor（任一来源，含 torproject 名录中的 exit 角色） | −30 |
  | vpn | −10 |
  | proxy | −10 |
  | scraper | −10 |
  | abuse（AbuseConfidence ≥ 25） | −10 |

- 没有任何有效分项时，`purity_score` 为 NULL，`state` 为 `pending`；如果所有已启用的来源都失败，`state` 为 `unsupported`。

### 1.2 置信度

- 覆盖率：`coverage = Σ 有效分项的权重 / Σ 已启用的计分来源的权重`。
- 一致度：`agreement = 1 - min(1, 分项标准差 / 50)`。
- 置信度按下表判定：

  | 置信度 | 条件 |
  |---|---|
  | `high` | 至少 2 个来源，`coverage ≥ 0.6`，且 `agreement ≥ 0.7` |
  | `medium` | 至少 2 个来源，或 `coverage ≥ 0.4` |
  | `low` | 只有 1 个来源 |
  | `none` | 没有来源 |

### 1.3 纯净度区间（沿用现有区间，前端已有样式）

| 区间 | 分数 |
|---|---|
| excellent | ≥ 95 |
| clean | ≥ 90 |
| fair | ≥ 80 |
| mixed | ≥ 60 |
| poor | < 60 |
| unknown | 分数为 NULL |

### 1.4 IP 类型（与纯净度无关，单独维度）

- 投票来源和映射：
  - proxycheck `network.type`：residential、business、wireless、mobile 原样映射；hosting 映射为 datacenter；
  - ipqs `connection_type`：Residential 映射为 residential；Mobile 映射为 mobile；Corporate 映射为 business；Data Center 映射为 datacenter；Education 映射为 business；
  - ipapi_is：`is_datacenter` 映射为 datacenter；`company.type` 为 isp 时映射为 residential，为 business 时映射为 business，为 hosting 时映射为 datacenter；
  - ip_api：`hosting` 映射为 datacenter；`mobile` 映射为 mobile；
  - IPPure：`isResidential=true` 映射为 residential，`false` 映射为 non_residential。
- 投票规则：
  - 在 {residential, mobile, business, wireless, datacenter} 中取票数最多的类型，平票时取非 datacenter 的一方；
  - non_residential 只在没有其他票时生效；
  - 同时出现 residential 或 mobile 与 datacenter，且双方票数都 ≥ 1、最多票数小于总票数的 2/3 时，判为 `conflicting`。
- 离线兜底：没有任何在线票时，如果 ASN 属于内置的云厂商 ASN 列表（`assess/hosting_asn.go`，至少包含 AWS、GCP、Azure、Oracle、Alibaba、Tencent、Huawei、DigitalOcean、Linode/Akamai、Vultr、Hetzner、OVH、Cloudflare），判为 `datacenter`，并在 reasons 中写入 `ASN_HOSTING_HEURISTIC`。

### 1.5 原生 IP

- 有 IPPure `isBroadcast` 时以它为准，`native = !isBroadcast`。
- 否则，如果离线库同时给出 `country` 和 `registered_country`：两者一致时 `native=true`，不一致时 `native=false`，并在 reasons 中写入 `NATIVE_HEURISTIC`。
- 两者都没有时，`native` 为 NULL。

### 1.6 判定（按顺序，第一条命中即采用）

1. 满足以下任一条件 → `high_risk`：compromised；tor 出口（torproject 名录中的 exit 角色，或信号 tor=true）；AbuseConfidence ≥ 75 且举报数 > 0；`purity_score < 60`。
2. IP 类型为 `conflicting` → `conflicting`。
3. 存在 proxy、vpn、scraper 或匿名信号，或 proxycheck 有攻击历史，或 DNSBL 命中 → `review`。
4. `purity_score` 为 NULL → `pending`。
5. `confidence` 为 `low` 且 `coverage < 0.3` → `incomplete`。
6. `purity_score < 80` → `caution`。
7. 其余 → `favorable`。

### 1.7 输出

- **有效期**：`valid_until` 取所有有效证据 `valid_until` 中的最小值；没有有效证据时取 `now + 1h`。
- **`components_json`**：列出每个分项的 `{source, raw, clean, weight, observed_at}`，以及每一项扣分。
- **`reasons`**：原因码数组，例如 `TOR_EXIT`、`RECENT_ABUSE`、`DNSBL_LISTED:zen.spamhaus.org`、`PROXY_DETECTED`、`NATIVE_HEURISTIC`。
- 与经节点检测无关的字段，都**按 IP 计算**。
- **测试**：表驱动，不少于 25 个用例，覆盖每条判定分支、每个扣分项、置信度边界、平票、冲突、离线兜底、IPv6（IPPure 分不计入）。

### 1.8 触发时机

- 证据写入后（WP08 的第 6 步和数据源队列工作者）。
- **过期巡检**：每 5 分钟扫描 `ip_assessment.valid_until_ns <= now`（只扫上次巡检以后到期的记录），重新计算，并调用 `NotifyEgressIPDirty`。
- **启动时**：如果库中存在 profile 不等于 `prism-purity-v2` 的记录，在后台以限速方式全量重算（每秒 500 个 IP）。

## 2. 平台质量准入（`internal/platform`）

模型见 WP02 §2（`model.QualityPolicy`）。评估函数：

```go
func (p *Platform) qualityAdmit(entry *node.NodeEntry, snap intel.SnapshotReader, now time.Time) (ok bool, reason string)
```

- **策略为空**（`IsEmpty()`）：直接放行，与 Resin 相同。
- **策略非空**时，按以下规则逐条检查，**默认失败即拒绝**：
  1. 取节点的出口 IP：优先 v4；平台 `ip_types` 或 `required_checks` 只关心 v6 的情况暂不支持。
  2. 取该 IP 的评估。没有评估，或 `state` 为 `pending`、`unsupported`、`stale` 时，按 `unknown_action` 处理；`unknown_action` 为空时视为 `exclude`，原因 `QUALITY_UNKNOWN`。
  3. `exclude_tor`（为 nil 时视为 true）且带 tor 标志 → 拒绝，原因 `QUALITY_TOR`。
  4. `exclude_high_risk`（为 nil 时视为 true）且判定为 `high_risk` → 拒绝，原因 `QUALITY_HIGH_RISK`。
  5. `allowed_verdicts` 非空且判定不在列表中 → 拒绝，原因 `QUALITY_VERDICT`。
  6. `min_purity` 已设置，且分数为 NULL 或低于阈值 → 拒绝，原因 `QUALITY_MIN_PURITY`（**分数为 NULL 同样拒绝**）。
  7. `ip_types` 非空且类型不在列表中 → 拒绝，原因 `QUALITY_IP_TYPE`。
  8. `min_confidence` 已设置且置信度低于它 → 拒绝，原因 `QUALITY_CONFIDENCE`。
  9. `require_native` 为 true 且 `native` 不为 1 → 拒绝，原因 `QUALITY_NATIVE`。
  10. `required_checks`：对每一条 `{check_id: outcome}`，节点的检测结果不存在、已过期，或结果与期望不符 → 拒绝，原因 `QUALITY_CHECK:<id>`。
  11. `max_assessment_age` 已设置且 `now - computed_at` 超过它 → 拒绝，原因 `QUALITY_STALE`。
  12. `max_egress_age` 已设置且 `now - LastEgressUpdate` 超过它 → 拒绝，原因 `QUALITY_EGRESS_STALE`。
- **接入方式**：
  - 在 `platform.evaluateNode` 的现有筛选（正则、地区）之后调用；
  - `GlobalNodePool` 的 `QualityLookup` 改为注入 `intel.SnapshotReader`；
  - 删除旧的 `QualityPolicy.Evaluate` 及其占位逻辑（`MinConfidence` 空实现、`actionAllows` 默认放行）。
- **出口 IP 反向索引**：`GlobalNodePool` 新增 `map[netip.Addr]map[node.Hash]struct{}`，在 `UpdateNodeEgressIP` 和节点删除时维护。新增 `NotifyEgressIPDirty(ip)`，对所有相关节点调用现有的 `notifyAllPlatformsDirty(hash)`。
- **时间型条件的巡检**：`max_assessment_age` 和 `max_egress_age` 与时间有关，由上面的 5 分钟巡检一并处理。对于策略中配置了这两项的平台，巡检时对其视图里超龄的节点执行 `NotifyDirty`。

### 2.1 API

- 平台的创建（POST）、修改（PATCH）和查询（GET）都新增 `quality_policy` 字段（对象）。创建和修改时校验：
  - 取值必须在枚举范围内；
  - `min_purity` 在 0–100 之间；
  - duration 字符串可以解析；
  - `required_checks` 中的 id 必须存在于当前规则中。
- `POST /api/v1/platforms/preview-filter`（上游已有接口）增加可选字段 `quality_policy`。返回值增加：
  ```json
  {"excluded_by": {"regex": n, "region": n, "QUALITY_MIN_PURITY": n}}
  ```
- 新增 `GET /api/v1/platforms/{id}/nodes/{hash}/explain`，返回每一条规则的检查结果：
  ```json
  [{"rule":"regex","passed":true}, {"rule":"quality.min_purity","passed":false,"detail":"score=72<80"}]
  ```

## 3. 定时轮换增强（`internal/routing`）

- API 字段已在 WP02、WP03 就位：`scheduled_rotation_enabled`、`scheduled_rotation_interval`，以及新增的 `rotation_avoid_previous_ip`（默认 true）。
- **轮换墓碑**：`Router` 新增一个内存 LRU（上限 100,000 条），键为 `platformID+"\x00"+account`，值为上一个出口 IP 及过期时间。过期时间取 `max(interval, sticky_ttl)`。
  - 轮换器删除租约时，写入一条墓碑；
  - 手动轮换接口也会写入墓碑。
- **选路**：为该账号创建新租约时，如果平台开启了 `rotation_avoid_previous_ip` 且存在墓碑，就在 P2C 的候选集中**排除**出口 IP 等于墓碑 IP 的节点。只有排除后没有候选时，才退回到不排除的结果，并在请求日志中记录 `rotation_fallback_same_ip`。
- **新 API**：`POST /api/v1/platforms/{id}/leases/{account}/actions/rotate`，执行"删除租约 + 写入墓碑"，返回 204。
- 墓碑不持久化（丢失的代价只是可能分到同一个 IP），这一点要写进文档。
- **测试**：
  - 开启后，连续轮换 10 次都不会连续两次拿到同一个出口 IP（候选池有 3 个 IP）；
  - 只有 1 个 IP 时退回到原 IP 并记录 fallback；
  - 墓碑达到上限时淘汰最旧的条目。

## 4. 节点列表中的质量信息

- `GET /api/v1/nodes` 和 `/nodes/{hash}` 的每个节点增加 `intel` 字段：
  ```json
  {
    "egress_ipv4":"", "egress_ipv6":"", "colo":"", "asn":0, "as_org":"", "country":"", "city":"",
    "ip_type":"", "native":null, "purity_score":null, "purity_band":"unknown",
    "confidence":"none", "verdict":"pending", "flags":[],
    "checks":{"chatgpt":"available"}, "assessed_at":""
  }
  ```
  数据全部从投影和 `node_egress` 中读取；列表场景按批次一次性读取，避免 N+1 查询。
- 新增筛选参数：`purity_min`、`purity_max`、`verdict`（可逗号分隔多值）、`confidence_min`、`native`（true/false）、`asn`、`country`、`check=<id>:<outcome>`（可重复）。原有的 `ip_type`、`purity_band` 改为从新评估读取。
- 新增排序字段：`purity_score`、`latency`、`assessed_at`。

## 5. 验收

```sh
make verify
```

新增测试中：assess 的表驱动测试不少于 25 个用例；平台准入的每条规则至少 1 个"通过"用例和 1 个"拒绝"用例；轮换墓碑测试；出口 IP 反向索引在节点删除后清理干净。
