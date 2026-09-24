# WP02 · 持久化层 `internal/state` 重建与迁移

**前置**：WP01。**目标**：恢复与上游 Resin 一致的持久化层（state.db 和 cache.db、脏集合回写、启动一致性修复、迁移），并加上 Prism 新增的表和列。完成后，所有依赖 `prism/internal/state` 的包都能编译。

## 1. 来源

- **情况 A**：WP01 §1 找回了 Prism 本地的 `internal/state`（带有 `repo_quality.go` 和 state migration 10）。以它为基础，把其中质量相关的表**删除**（新方案的检测数据全部放在 intel.db，见 WP08），再按本文第 3 节重新编号新增的迁移。编号必须连续，已经发布过的迁移不允许修改。
- **情况 B**（默认）：从上游 `github.com/Resinat/Resin@9b8ef8e` 复制以下内容：
  - `internal/state/*.go`，共 10 个非测试文件：consistency、dirtyset、engine、errors、flush、migrate、persistence_bootstrap、repo_cache、repo_state、schema；
  - `internal/state/migrations/**`，共 21 个 SQL 文件；
  - `internal/state/*_test.go`，共 10 个测试文件。

  复制后，把导入路径 `github.com/Resinat/Resin` 替换为 `prism`。注释和错误文案中的 Resin 可以改为 Prism，但**不要改任何 SQL 表名或列名**。

## 2. 模型改动（`internal/model/models.go`）

1. 删除 `platform.QualityPolicy` 这个重复类型。全项目只保留 `model.QualityPolicy`，由 `platform` 包引用。它的语义在 WP10 实现，本 WP 只负责持久化。

   ```go
   // QualityPolicy: empty value means "no quality admission" (Resin behaviour).
   type QualityPolicy struct {
       MinPurity        *int              `json:"min_purity,omitempty"`         // 0..100
       IPTypes          []string          `json:"ip_types,omitempty"`           // residential|mobile|business|wireless|datacenter|non_residential
       AllowedVerdicts  []string          `json:"allowed_verdicts,omitempty"`   // favorable|caution|review|high_risk|conflicting|incomplete
       MinConfidence    string            `json:"min_confidence,omitempty"`     // ""|low|medium|high
       RequireNative    bool              `json:"require_native,omitempty"`
       RequiredChecks   map[string]string `json:"required_checks,omitempty"`    // check_id -> outcome, e.g. {"chatgpt":"available"}
       MaxAssessmentAge string            `json:"max_assessment_age,omitempty"` // Go duration, "" = unlimited
       MaxEgressAge     string            `json:"max_egress_age,omitempty"`
       UnknownAction    string            `json:"unknown_action,omitempty"`     // ""(=exclude)|allow|exclude
       ExcludeTor       *bool             `json:"exclude_tor,omitempty"`        // nil = true
       ExcludeHighRisk  *bool             `json:"exclude_high_risk,omitempty"`  // nil = true
   }
   func (q QualityPolicy) IsEmpty() bool // 所有字段为零值时返回 true
   ```

   **旧键兼容**：解码时把 `min_score` 当作 `min_purity`，把 `max_assessment_age_seconds` 和 `max_egress_age_seconds` 转为 duration 字符串；`profile_id`、`pending_action`、`conflict_action` 直接忽略。编码时只输出新键。

2. `model.Platform` 保留已有的 `ScheduledRotationEnabled` 和 `ScheduledRotationIntervalNs`，另外新增：

   ```go
   QualityPolicy           QualityPolicy `json:"quality_policy"`
   RotationAvoidPreviousIP bool          `json:"rotation_avoid_previous_ip"`
   ```

3. `model.Subscription` 新增：

   ```go
   AutoIntel           bool   `json:"auto_intel"`             // 默认 true
   UserAgent           string `json:"user_agent"`             // 空 = 使用全局 UA
   LastParseReportJSON string `json:"-"`                      // 由 WP06 写入
   ```

4. 新增模型：

   ```go
   type IntelProviderSetting struct {
       ProviderID  string  `json:"provider_id"`
       Enabled     bool    `json:"enabled"`
       APIKey      string  `json:"-"`            // 绝不序列化
       DailyLimit  int     `json:"daily_limit"`  // 0 = 使用数据源默认值
       QPS         float64 `json:"qps"`          // 0 = 使用数据源默认值
       TTLNs       int64   `json:"ttl_ns"`       // 0 = 使用数据源默认值
       ConfigJSON  string  `json:"config_json"`  // 数据源私有配置（例如 DNSBL zones）
       UpdatedAtNs int64   `json:"updated_at_ns"`
   }
   type ExportProfile struct {
       ID, Name, Format, TokenSHA256, PlatformID, FilterJSON, NameTemplate string
       Enabled                                                          bool
       LastAccessAtNs, AccessCount, CreatedAtNs, UpdatedAtNs            int64
   }
   type AuditEntry struct {
       ID                                        int64
       AtNs                                      int64
       Actor, RemoteAddr, Action, Target, Detail string // Detail 为 JSON 文本
   }
   ```

## 3. 新增 state.db 迁移

编号从上游的 000009 往后接；情况 A 下按实际情况顺延。每个迁移都要提供 `.up.sql` 和 `.down.sql`。modernc sqlite 支持 `DROP COLUMN`，down 迁移直接删列即可。

`000010_platforms_quality_and_rotation.up.sql`

```sql
ALTER TABLE platforms ADD COLUMN quality_policy_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE platforms ADD COLUMN scheduled_rotation_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN scheduled_rotation_interval_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN rotation_avoid_previous_ip INTEGER NOT NULL DEFAULT 1;
```

`000011_subscriptions_import_options.up.sql`

```sql
ALTER TABLE subscriptions ADD COLUMN auto_intel INTEGER NOT NULL DEFAULT 1;
ALTER TABLE subscriptions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN last_parse_report_json TEXT NOT NULL DEFAULT '{}';
```

`000012_intel_provider_settings.up.sql`

```sql
CREATE TABLE IF NOT EXISTS intel_provider_settings (
    provider_id   TEXT PRIMARY KEY,
    enabled       INTEGER NOT NULL,
    api_key       TEXT NOT NULL DEFAULT '',
    daily_limit   INTEGER NOT NULL DEFAULT 0,
    qps           REAL NOT NULL DEFAULT 0,
    ttl_ns        INTEGER NOT NULL DEFAULT 0,
    config_json   TEXT NOT NULL DEFAULT '{}',
    updated_at_ns INTEGER NOT NULL
);
```

`000013_export_profiles.up.sql`

```sql
CREATE TABLE IF NOT EXISTS export_profiles (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    format            TEXT NOT NULL,
    token_sha256      TEXT NOT NULL UNIQUE,
    platform_id       TEXT NOT NULL DEFAULT '',
    filter_json       TEXT NOT NULL DEFAULT '{}',
    name_template     TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    last_access_at_ns INTEGER NOT NULL DEFAULT 0,
    access_count      INTEGER NOT NULL DEFAULT 0,
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);
```

`000014_audit_log.up.sql`

```sql
CREATE TABLE IF NOT EXISTS audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at_ns       INTEGER NOT NULL,
    actor       TEXT NOT NULL,
    remote_addr TEXT NOT NULL,
    action      TEXT NOT NULL,
    target      TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_audit_log_at ON audit_log(at_ns);
```

cache.db **不做任何修改**，保持与 Resin 完全一致。

## 4. 仓储（StateRepo / StateEngine）方法

- 平台和订阅的 CRUD 读写新列。`quality_policy_json` 用 `json.Marshal(model.QualityPolicy)` 序列化，读取时按 §2 兼容旧键。
- 新增以下方法。StateEngine 通过嵌入 StateRepo 暴露这些方法，写操作都在事务中执行：

  ```go
  SetSubscriptionParseReport(id string, reportJSON string) error          // 超过 64 KiB 截断（保留 JSON 合法）
  ListIntelProviderSettings() ([]model.IntelProviderSetting, error)
  UpsertIntelProviderSetting(s model.IntelProviderSetting) error
  ListExportProfiles() ([]model.ExportProfile, error)
  GetExportProfile(id string) (*model.ExportProfile, error)
  GetExportProfileByTokenSHA256(hash string) (*model.ExportProfile, error)
  UpsertExportProfile(p model.ExportProfile) error                        // name 冲突时返回 ErrConflict
  DeleteExportProfile(id string) error
  TouchExportProfileAccess(id string, atNs int64) error                   // access_count+1
  AppendAudit(e model.AuditEntry) error
  ListAudit(beforeID int64, limit int) ([]model.AuditEntry, error)        // 按 id 倒序
  PruneAudit(olderThanNs int64, keepMax int) (int64, error)               // 默认保留 90 天且最多 100000 条
  ```

- `PersistenceBootstrap(stateDir, cacheDir)` 保持上游签名不变。intel.db 由 WP08 的独立包负责打开和迁移，**不放在这里**。
- 文件权限：数据库文件 0600，目录 0700。state.db 设置 `synchronous=FULL`（与 Prism 此前的安全修复一致）。

## 5. 使用方的编译修复

- `internal/service`、`internal/metrics`、`internal/requestlog` 只引用了 `state.InitDB`、`OpenDB`、`ErrNotFound`、`ErrConflict` 和 `StateEngine`，恢复后即可编译。
- 执行 `go mod tidy`（此时依赖已完整）。

## 6. 测试

1. 上游 10 个 state 测试全部通过。
2. 新增 `migrate_prism_test.go`，验证三件事：
   - 先只执行上游 1–9 号迁移，并插入平台、订阅和接入点数据；
   - 再执行到最新版本；
   - 断言旧数据完好、新列取默认值（`auto_intel=1`、`rotation_avoid_previous_ip=1`、`quality_policy_json='{}'`）。
3. 为每个新表写 CRUD 测试。`IntelProviderSetting.APIKey` 必须能存取，并且 `json.Marshal` 的结果中不出现 Key。
4. `QualityPolicy` 编解码测试：覆盖旧键兼容、空策略 `IsEmpty()` 返回 true、往返（round-trip）一致。

## 7. 验收

```sh
go build -tags "with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy" ./internal/...
go test ./internal/state/... ./internal/model/...
```

`./internal/...` 必须全部能编译；`./cmd/...` 由 WP03 负责。

## 8. 不要做

- 不要修改上游 1–9 号迁移的内容或编号。
- 不要把检测数据放进 state.db 或 cache.db。
