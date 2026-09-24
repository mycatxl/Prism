package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"prism/internal/model"
)

// Setting is the effective configuration of one data source: the spec defaults
// merged with the persisted intel_provider_settings row and, only on the first
// start, with the legacy environment variables (WP09 §1).
type Setting struct {
	Spec       Spec
	Enabled    bool
	APIKey     string
	DailyLimit int
	QPS        float64
	TTL        time.Duration
	Config     map[string]any
	// Source records where the values came from: "default", "state" or "env".
	Source      string
	UpdatedAtNs int64
}

// HasKey reports whether every credential of the provider is configured. The
// key value itself is never exposed to the API (R6).
func (s Setting) HasKey() bool {
	if s.Spec.RequiresKey {
		for _, field := range s.Spec.CredentialFields {
			if s.ConfigString(field) == "" {
				return false
			}
		}
		if len(s.Spec.CredentialFields) > 0 {
			return true
		}
		return s.APIKey != ""
	}
	if s.APIKey != "" {
		return true
	}
	for _, field := range s.Spec.CredentialFields {
		if s.ConfigString(field) != "" {
			return true
		}
	}
	return false
}

// Runnable reports whether the provider may be executed with these settings.
func (s Setting) Runnable() bool {
	if !s.Enabled {
		return false
	}
	if s.Spec.RequiresKey && !s.HasKey() {
		return false
	}
	return true
}

// ConfigString reads one config_json value as a trimmed string.
func (s Setting) ConfigString(key string) string {
	if s.Config == nil {
		return ""
	}
	value, ok := s.Config[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// ConfigStrings reads a config_json value as a string list.
func (s Setting) ConfigStrings(key string) []string {
	if s.Config == nil {
		return nil
	}
	raw, ok := s.Config[key]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

// CredentialID is sha256(provider + credential). It lets a key rotation lift a
// pause automatically without ever storing the key (WP08 §2, WP09 §1).
func (s Setting) CredentialID() string {
	secret := s.APIKey
	for _, field := range s.Spec.CredentialFields {
		secret += "\x00" + s.ConfigString(field)
	}
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s.Spec.ID + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// EffectiveTTL returns the configured TTL or the spec default.
func (s Setting) EffectiveTTL() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return s.Spec.DefaultTTL
}

// QueueSpec is the online-ip queue-worker configuration projected from a
// Setting (see internal/intel/jobs.ProviderQueueSpec).
type QueueSpec struct {
	ProviderID   string
	Enabled      bool
	DailyLimit   int
	QPS          float64
	BatchSize    int
	CredentialID string
}

// SettingsStore is the subset of the state engine the provider settings need.
type SettingsStore interface {
	ListIntelProviderSettings() ([]model.IntelProviderSetting, error)
	UpsertIntelProviderSetting(s model.IntelProviderSetting) error
}

// EnvSource reads one environment variable (nil disables environment support).
type EnvSource func(string) string

// envBinding maps the environment variables of one provider onto its setting.
type envBinding struct {
	apiKey     string
	dailyLimit string
	config     map[string]string
}

// envBindings is the migration table of WP09 §1. PRISM_QUALITY_ENABLED and
// PRISM_QUALITY_WORKERS are handled by the runtime configuration
// (intel_enabled / intel_node_workers), and PRISM_QUALITY_QUEUE_SIZE is
// ignored with a deprecation warning.
var envBindings = map[string]envBinding{
	"proxycheck":       {apiKey: "PRISM_QUALITY_API_KEY", dailyLimit: "PRISM_QUALITY_DAILY_LIMIT"},
	"abuseipdb":        {apiKey: "PRISM_ABUSEIPDB_API_KEY"},
	"ipqs":             {apiKey: "PRISM_IPQS_API_KEY"},
	"ipapi_is":         {apiKey: "PRISM_IPAPI_IS_API_KEY"},
	"maxmind_geolite2": {config: map[string]string{"account_id": "PRISM_MAXMIND_ACCOUNT_ID", "license_key": "PRISM_MAXMIND_LICENSE_KEY"}},
	"ipinfo_lite":      {config: map[string]string{"token": "PRISM_IPINFO_TOKEN"}},
}

// DeprecatedQualityQueueSizeEnv is ignored; the queue size follows
// intel_node_workers.
const DeprecatedQualityQueueSizeEnv = "PRISM_QUALITY_QUEUE_SIZE"

// DefaultSetting builds the spec-default setting of one provider, optionally
// seeded from the environment.
func DefaultSetting(spec Spec, env EnvSource) Setting {
	setting := Setting{Spec: spec, Config: map[string]any{}, Source: "default"}
	setting.QPS = spec.DefaultQPS
	setting.TTL = spec.DefaultTTL
	appliedEnv := false
	if env != nil {
		if binding, ok := envBindings[spec.ID]; ok {
			appliedEnv = applyEnvBinding(&setting, binding, env)
		}
	}
	if len(setting.Config) == 0 {
		setting.Config = map[string]any{}
	}
	setting.DailyLimit = resolveLimit(spec, setting.DailyLimit, setting.HasKey())
	setting.Enabled = spec.DefaultEnabled
	if spec.RequiresKey {
		setting.Enabled = setting.HasKey()
	}
	// Source reports "env" only when a legacy variable really supplied a value:
	// a spec default is not an environment value.
	if appliedEnv {
		setting.Source = "env"
	}
	return setting
}

// applyEnvBinding fills one setting from the legacy environment variables and
// reports whether any of them was set.
func applyEnvBinding(setting *Setting, binding envBinding, env EnvSource) bool {
	applied := false
	if binding.apiKey != "" {
		if value := strings.TrimSpace(env(binding.apiKey)); value != "" {
			setting.APIKey = value
			applied = true
		}
	}
	if binding.dailyLimit != "" {
		if limit, ok := parseEnvInt(env(binding.dailyLimit)); ok {
			setting.DailyLimit = clampLimit(setting.Spec, limit)
			applied = true
		}
	}
	for key, name := range binding.config {
		if value := strings.TrimSpace(env(name)); value != "" {
			setting.Config[key] = value
			applied = true
		}
	}
	return applied
}

// MergeSetting merges the persisted row of one provider (nil when absent) with
// the spec defaults and the environment.
func MergeSetting(spec Spec, row *model.IntelProviderSetting, env EnvSource) Setting {
	setting := DefaultSetting(spec, env)
	if row == nil {
		return setting
	}
	setting.Source = "state"
	setting.Enabled = row.Enabled
	setting.APIKey = strings.TrimSpace(row.APIKey)
	setting.UpdatedAtNs = row.UpdatedAtNs
	if strings.TrimSpace(row.ConfigJSON) != "" {
		config := map[string]any{}
		if err := json.Unmarshal([]byte(row.ConfigJSON), &config); err == nil {
			setting.Config = config
		}
	}
	if setting.Config == nil {
		setting.Config = map[string]any{}
	}
	switch {
	case row.QPS > 0:
		setting.QPS = row.QPS
	case row.QPS < 0:
		// A negative value explicitly means "unlimited"; it is the only way to
		// lift a finite default from the settings page.
		setting.QPS = 0
	}
	if row.TTLNs > 0 {
		setting.TTL = time.Duration(row.TTLNs)
	}
	switch {
	case row.DailyLimit > 0:
		setting.DailyLimit = clampLimit(spec, row.DailyLimit)
	case row.DailyLimit < 0:
		// A negative value explicitly means "unlimited"; it is the only way to
		// lift a finite default from the settings page.
		setting.DailyLimit = 0
	default:
		setting.DailyLimit = resolveLimit(spec, 0, setting.HasKey())
	}
	return setting
}

// ResolveSettings merges every spec with its persisted row.
func ResolveSettings(specs []Spec, rows []model.IntelProviderSetting, env EnvSource) []Setting {
	byID := make(map[string]model.IntelProviderSetting, len(rows))
	for _, row := range rows {
		byID[row.ProviderID] = row
	}
	out := make([]Setting, 0, len(specs))
	for _, spec := range specs {
		if row, ok := byID[spec.ID]; ok {
			out = append(out, MergeSetting(spec, &row, env))
			continue
		}
		out = append(out, MergeSetting(spec, nil, env))
	}
	return out
}

// SeedSettings writes the environment defaults once, for the providers that do
// not have a row yet (WP09 §1). It returns the number of rows written and the
// deprecation warnings to log.
func SeedSettings(store SettingsStore, specs []Spec, env EnvSource, nowNs int64) (int, []string, error) {
	if store == nil || env == nil {
		return 0, nil, nil
	}
	rows, err := store.ListIntelProviderSettings()
	if err != nil {
		return 0, nil, err
	}
	existing := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		existing[row.ProviderID] = struct{}{}
	}
	var warnings []string
	if strings.TrimSpace(env(DeprecatedQualityQueueSizeEnv)) != "" {
		warnings = append(warnings, DeprecatedQualityQueueSizeEnv+
			" is deprecated and ignored; the provider queue is bounded by intel_node_workers")
	}
	written := 0
	for _, spec := range specs {
		if _, ok := existing[spec.ID]; ok {
			continue
		}
		if _, ok := envBindings[spec.ID]; !ok {
			continue
		}
		seed := DefaultSetting(spec, env)
		if seed.APIKey == "" && len(seed.Config) == 0 {
			continue
		}
		configJSON := "{}"
		if len(seed.Config) > 0 {
			encoded, err := json.Marshal(seed.Config)
			if err != nil {
				continue
			}
			configJSON = string(encoded)
		}
		row := model.IntelProviderSetting{
			ProviderID:  spec.ID,
			Enabled:     seed.Enabled,
			APIKey:      seed.APIKey,
			DailyLimit:  seed.DailyLimit,
			QPS:         seed.QPS,
			TTLNs:       int64(seed.EffectiveTTL()),
			ConfigJSON:  configJSON,
			UpdatedAtNs: nowNs,
		}
		if err := store.UpsertIntelProviderSetting(row); err != nil {
			return written, warnings, err
		}
		written++
	}
	return written, warnings, nil
}

// resolveLimit returns the effective daily limit: the configured value when it
// is non-zero, otherwise the spec default for the current credential.
func resolveLimit(spec Spec, configured int, hasKey bool) int {
	if configured != 0 {
		return clampLimit(spec, configured)
	}
	if hasKey && spec.DefaultDailyLimitWithKey > 0 {
		return spec.DefaultDailyLimitWithKey
	}
	return clampLimit(spec, spec.DefaultDailyLimit)
}

// clampLimit bounds a daily limit by MaxDailyLimit (0 = unbounded).
func clampLimit(spec Spec, limit int) int {
	if limit < 0 {
		return 0
	}
	if spec.MaxDailyLimit > 0 && limit > spec.MaxDailyLimit {
		return spec.MaxDailyLimit
	}
	return limit
}

func parseEnvInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value := 0
	negative := false
	for i, r := range raw {
		if i == 0 && r == '-' {
			negative = true
			continue
		}
		if r < '0' || r > '9' {
			return 0, false
		}
		value = value*10 + int(r-'0')
		if value > 1_000_000_000 {
			return 0, false
		}
	}
	if negative {
		value = -value
	}
	return value, true
}

// SecondsPerRequest returns the minimum interval between two requests of one
// provider (0 = unlimited).
func (s Setting) SecondsPerRequest() time.Duration {
	if s.QPS <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / s.QPS)
}
