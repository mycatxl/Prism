package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"prism/internal/model"
)

// Provider settings API (WP09 §4). This file is the write path behind
// GET/PATCH /api/v1/intel/providers: it validates a patch, persists it in
// intel_provider_settings and applies the new effective settings to the live
// registry, so the running scheduler picks the change up without a restart.
//
// The stored credential never leaves this package: the API view reports
// has_key only (R6).

// Bounds of the settings surface. Every input is validated before it is
// persisted and every value has a documented over-limit behaviour (R4/R7).
const (
	// MaxQPS bounds the configurable request rate of one provider.
	MaxQPS = 100
	// MaxTTL bounds the configurable evidence TTL (365 days).
	MaxTTL = 365 * 24 * time.Hour
	// MaxAPIKeyBytes bounds one stored credential.
	MaxAPIKeyBytes = 512
	// MaxConfigBytes bounds the stored config_json payload.
	MaxConfigBytes = 8 * 1024
	// MaxConfigKeys bounds the number of provider-private config keys.
	MaxConfigKeys = 32
	// MaxConfigListEntries bounds one config string list (DNSBL zones).
	MaxConfigListEntries = 64
	// MaxConfigStringBytes bounds one config string value.
	MaxConfigStringBytes = 1024
)

// Settings errors of the §4 write path. The API layer maps them onto
// 404 NOT_FOUND and 400 INVALID_ARGUMENT.
var (
	// ErrUnknownProvider reports an id that is not registered.
	ErrUnknownProvider = errors.New("unknown provider")
	// ErrInvalidSetting reports a patched value that failed validation. The
	// message never repeats a secret: no config value and no API key is echoed.
	ErrInvalidSetting = errors.New("invalid provider setting")
	// ErrSettingsUnavailable reports a settings surface without a store.
	ErrSettingsUnavailable = errors.New("intel provider settings are unavailable")
)

// ProviderPatch is the PATCH /api/v1/intel/providers/{id} body (WP09 §4). Every
// field is optional: an absent field keeps the persisted value.
//
//   - api_key is write-only. An explicit empty string clears the stored
//     credential, an explicit null keeps it.
//   - daily_limit and qps accept 0 for "unlimited".
//   - ttl is a Go duration string ("24h") and must be positive.
//   - config is merged into the persisted value; a key with a null value is
//     removed.
type ProviderPatch struct {
	Enabled    *bool          `json:"enabled,omitempty"`
	APIKey     *string        `json:"api_key,omitempty"`
	DailyLimit *int           `json:"daily_limit,omitempty"`
	QPS        *float64       `json:"qps,omitempty"`
	TTL        *string        `json:"ttl,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
}

// ProviderUsage is today's budget and queue state of one provider (WP08 §2
// provider_state/provider_queue). Remaining is -1 when the provider is
// unlimited, so a client can tell "no quota" from "quota exhausted".
type ProviderUsage struct {
	Day             string `json:"day"`
	Used            int    `json:"used"`
	Remaining       int    `json:"remaining"`
	Exhausted       bool   `json:"exhausted"`
	NextRequestAtNs int64  `json:"next_request_at_ns"`
	// NextAllowedAtNs is the first instant the provider may be called again:
	// the later of the QPS gate and the 429 cooldown.
	NextAllowedAtNs int64  `json:"next_allowed_at_ns"`
	BlockedUntilNs  int64  `json:"blocked_until_ns"`
	Paused          bool   `json:"paused"`
	ErrorCode       string `json:"error_code"`
	Queued          int    `json:"queued"`
	Running         int    `json:"running"`
	Done            int    `json:"done"`
	Failed          int    `json:"failed"`
}

// DatabaseFileStatus is one file of a downloaded offline database (WP09 §3).
type DatabaseFileStatus struct {
	// Name is the file name inside $PRISM_CACHE_DIR/geo.
	Name string `json:"name"`
	// Installed reports the file on disk, with its size and age.
	Installed   bool   `json:"installed"`
	SizeBytes   int64  `json:"size_bytes"`
	UpdatedAtNs int64  `json:"updated_at_ns,omitempty"`
	Age         string `json:"age,omitempty"`
	// Downloading reports an in-flight download of this file.
	Downloading bool `json:"downloading,omitempty"`
	// LastOutcome is refreshed | up-to-date | skipped | failed.
	LastOutcome string `json:"last_outcome,omitempty"`
	// ErrorCode is the stable identifier of a skip or a failure, for example
	// GEO_NOT_READY or GEO_HTTP_STATUS.
	ErrorCode string `json:"error_code,omitempty"`
	// Reason explains the outcome without ever repeating a credential.
	Reason          string `json:"reason,omitempty"`
	LastAttemptAtNs int64  `json:"last_attempt_at_ns,omitempty"`
	LastSuccessAtNs int64  `json:"last_success_at_ns,omitempty"`
	NextAttemptAtNs int64  `json:"next_attempt_at_ns,omitempty"`
}

// DatabaseStatus is the download state of the offline databases of one
// provider: whether they are installed, how old they are, when the last
// download was attempted and how it ended (WP09 §3).
type DatabaseStatus struct {
	// Installed is true when every file of the provider exists.
	Installed bool `json:"installed"`
	// Ready reports whether the databases may be downloaded with the current
	// settings; a not-ready provider is skipped with Reason.
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
	// RefreshInterval is the file age at which a database is downloaded again.
	RefreshInterval string `json:"refresh_interval"`
	// Downloading, ErrorCode and LastSuccessAtNs aggregate the files.
	Downloading     bool                 `json:"downloading,omitempty"`
	ErrorCode       string               `json:"error_code,omitempty"`
	LastSuccessAtNs int64                `json:"last_success_at_ns,omitempty"`
	Files           []DatabaseFileStatus `json:"files"`
}

// ProviderStatus is one entry of GET /api/v1/intel/providers: the spec, the
// effective non-secret settings and the live budget/queue state.
type ProviderStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Website string `json:"website,omitempty"`
	// Terms is the vendor quota/terms hint shown on the settings page (§3).
	Terms string `json:"terms,omitempty"`
	// Category is the kind in its documented wire form:
	// offline | online-ip | via-node.
	Category string `json:"category"`
	// ViaNode is true for a source queried through the tested node; Direct is
	// true for a query from the Prism host. An offline source is neither.
	ViaNode   bool   `json:"via_node"`
	Direct    bool   `json:"direct"`
	Profile   string `json:"profile"`
	BatchSize int    `json:"batch_size"`
	// RequiresKey marks a source that cannot run without a credential.
	RequiresKey  bool `json:"requires_key"`
	SupportsIPv6 bool `json:"supports_ipv6"`

	DefaultEnabled           bool    `json:"default_enabled"`
	DefaultDailyLimit        int     `json:"default_daily_limit"`
	DefaultDailyLimitWithKey int     `json:"default_daily_limit_with_key,omitempty"`
	DefaultQPS               float64 `json:"default_qps"`
	DefaultTTL               string  `json:"default_ttl"`
	MaxDailyLimit            int     `json:"max_daily_limit,omitempty"`

	// CredentialFields names the config keys that hold a secret. The values of
	// these keys are never returned, only has_key (R6).
	CredentialFields []string `json:"credential_fields,omitempty"`

	Enabled    bool    `json:"enabled"`
	Runnable   bool    `json:"runnable"`
	HasKey     bool    `json:"has_key"`
	DailyLimit int     `json:"daily_limit"`
	QPS        float64 `json:"qps"`
	TTL        string  `json:"ttl"`
	// Config holds the provider-private settings (DNSBL zones, a resolver, a
	// mirror URL) without the secret-looking keys.
	Config      map[string]any `json:"config"`
	Source      string         `json:"source"`
	UpdatedAtNs int64          `json:"updated_at_ns"`

	Usage ProviderUsage `json:"usage"`

	// Database reports the state of the downloaded offline database of this
	// provider: installed, size and age, last attempt and its outcome. It is
	// nil for a provider without one and never carries a credential (WP09 §3).
	Database *DatabaseStatus `json:"database,omitempty"`
}

// UsageFunc reports the live budget/queue state of one provider. It may be nil:
// the offline sources have no budget.
type UsageFunc func(providerID string) ProviderUsage

// SettingsService owns the live registry and the persisted provider settings.
// It is safe for concurrent use; the read-modify-write of one update is
// serialised so two PATCHes cannot interleave.
type SettingsService struct {
	registry *Registry
	store    SettingsStore
	env      EnvSource
	now      func() time.Time
	mu       sync.Mutex
	// databases is the offline database refresher of WP09 §3, attached once
	// during assembly (SetDatabases).
	databases *GeoManager
}

// NewSettingsService builds the §4 settings surface. A nil store keeps the
// surface read-only: every read falls back to the environment/defaults and
// Update fails with ErrSettingsUnavailable.
func NewSettingsService(registry *Registry, store SettingsStore, env EnvSource, now func() time.Time) *SettingsService {
	if now == nil {
		now = time.Now
	}
	return &SettingsService{registry: registry, store: store, env: env, now: now}
}

// Registry returns the live registry the settings are applied to.
func (s *SettingsService) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

// databaseState renders the offline database state of one provider. A provider
// without a downloaded database reports nil, so the field stays absent.
func (s *SettingsService) databaseState(providerID string) *DatabaseStatus {
	if s == nil || s.databases == nil {
		return nil
	}
	return s.databases.DatabaseStatus(providerID)
}

// SetDatabases attaches the offline database refresher (WP09 §3) so the
// settings surface can report its state and trigger a refresh. It must be
// called while the application is assembled, before the surface is served.
func (s *SettingsService) SetDatabases(databases *GeoManager) {
	if s == nil {
		return
	}
	s.databases = databases
}

// RefreshDatabases downloads the offline databases of one provider now. The
// result reports a bounded outcome per file; only an unknown provider or a
// disabled refresher is an error.
func (s *SettingsService) RefreshDatabases(ctx context.Context, id string) (GeoRefreshResult, error) {
	if s == nil || s.registry == nil {
		return GeoRefreshResult{}, ErrSettingsUnavailable
	}
	id = strings.TrimSpace(id)
	if _, ok := s.registry.Spec(id); !ok {
		return GeoRefreshResult{}, fmt.Errorf("%w: %s", ErrUnknownProvider, id)
	}
	if s.databases == nil {
		return GeoRefreshResult{}, fmt.Errorf("%w: %s", ErrGeoUnavailable, id)
	}
	return s.databases.RequestRefresh(ctx, id)
}

// Statuses returns every registered provider in registry order (sorted by id).
func (s *SettingsService) Statuses(usage UsageFunc) ([]ProviderStatus, error) {
	if s == nil || s.registry == nil {
		return nil, ErrSettingsUnavailable
	}
	rows, err := s.rows()
	if err != nil {
		return nil, err
	}
	specs := s.registry.Specs()
	out := make([]ProviderStatus, 0, len(specs))
	for _, spec := range specs {
		out = append(out, s.status(spec, s.effective(spec, rows), usage))
	}
	return out, nil
}

// Status returns one provider.
func (s *SettingsService) Status(id string, usage UsageFunc) (ProviderStatus, error) {
	if s == nil || s.registry == nil {
		return ProviderStatus{}, ErrSettingsUnavailable
	}
	spec, ok := s.registry.Spec(strings.TrimSpace(id))
	if !ok {
		return ProviderStatus{}, fmt.Errorf("%w: %s", ErrUnknownProvider, strings.TrimSpace(id))
	}
	rows, err := s.rows()
	if err != nil {
		return ProviderStatus{}, err
	}
	return s.status(spec, s.effective(spec, rows), usage), nil
}

// Update validates and persists one patch, then applies the resolved settings to
// the live registry (WP09 §4). A changed key also produces a new credential
// fingerprint, which lifts a pause on the provider's next request (§3.3).
func (s *SettingsService) Update(id string, patch ProviderPatch, usage UsageFunc) (ProviderStatus, error) {
	if s == nil || s.registry == nil {
		return ProviderStatus{}, ErrSettingsUnavailable
	}
	id = strings.TrimSpace(id)
	spec, ok := s.registry.Spec(id)
	if !ok {
		return ProviderStatus{}, fmt.Errorf("%w: %s", ErrUnknownProvider, id)
	}
	if s.store == nil {
		return ProviderStatus{}, ErrSettingsUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	row, err := s.rowOf(id)
	if err != nil {
		return ProviderStatus{}, err
	}
	next, err := s.mergePatch(spec, MergeSetting(spec, row, s.env), patch)
	if err != nil {
		return ProviderStatus{}, err
	}
	updated, err := s.rowFromSetting(next)
	if err != nil {
		return ProviderStatus{}, err
	}
	if err := s.store.UpsertIntelProviderSetting(updated); err != nil {
		return ProviderStatus{}, fmt.Errorf("persist provider setting: %w", err)
	}
	if err := s.Reload(); err != nil {
		return ProviderStatus{}, err
	}
	setting, ok := s.registry.Setting(id)
	if !ok {
		setting = next
	}
	return s.status(spec, setting, usage), nil
}

// Reload re-reads the persisted rows and applies them to the running registry.
// The queue workers and the pipeline steps resolve the registry on every use,
// so the change is effective without a restart (WP09 §1/§4).
func (s *SettingsService) Reload() error {
	if s == nil || s.registry == nil {
		return ErrSettingsUnavailable
	}
	rows, err := s.rows()
	if err != nil {
		return err
	}
	s.registry.Apply(ResolveSettings(s.registry.Specs(), rows, s.env))
	return nil
}

// rows reads the persisted settings; a nil store means "nothing persisted yet".
func (s *SettingsService) rows() ([]model.IntelProviderSetting, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	rows, err := s.store.ListIntelProviderSettings()
	if err != nil {
		return nil, fmt.Errorf("list provider settings: %w", err)
	}
	return rows, nil
}

// rowOf returns the persisted row of one provider, or nil when it never was
// configured through the API.
func (s *SettingsService) rowOf(id string) (*model.IntelProviderSetting, error) {
	rows, err := s.rows()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ProviderID == id {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// effective resolves the setting of one provider: the registry holds the live
// value, the store is the fallback for a registry that was never applied.
func (s *SettingsService) effective(spec Spec, rows []model.IntelProviderSetting) Setting {
	if setting, ok := s.registry.Setting(spec.ID); ok {
		return setting
	}
	for i := range rows {
		if rows[i].ProviderID == spec.ID {
			return MergeSetting(spec, &rows[i], s.env)
		}
	}
	return MergeSetting(spec, nil, s.env)
}

// rowFromSetting renders the row that persists one effective setting. "0 =
// unlimited" is stored as the negative marker MergeSetting understands
// (-1 for daily_limit/qps), because 0 keeps meaning "spec default" in the
// persisted shape.
func (s *SettingsService) rowFromSetting(setting Setting) (model.IntelProviderSetting, error) {
	// The persisted config keeps every value, including the credential fields of
	// providers such as maxmind_geolite2: only the API view redacts them (R6).
	config := map[string]any{}
	for key, value := range setting.Config {
		if value == nil {
			continue
		}
		config[key] = value
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return model.IntelProviderSetting{}, fmt.Errorf("%w: config could not be encoded", ErrInvalidSetting)
	}
	row := model.IntelProviderSetting{
		ProviderID:  setting.Spec.ID,
		Enabled:     setting.Enabled,
		APIKey:      strings.TrimSpace(setting.APIKey),
		DailyLimit:  storeLimit(setting.DailyLimit),
		QPS:         storeQPS(setting.QPS),
		TTLNs:       int64(setting.EffectiveTTL()),
		ConfigJSON:  string(encoded),
		UpdatedAtNs: s.now().UTC().UnixNano(),
	}
	return row, nil
}

// storeLimit maps a daily limit onto the persisted marker: the API's 0
// (unlimited) becomes -1, because 0 keeps meaning "spec default" in the
// persisted shape.
func storeLimit(limit int) int {
	if limit <= 0 {
		return -1
	}
	return limit
}

// storeQPS maps a request rate onto the persisted marker, see storeLimit.
func storeQPS(qps float64) float64 {
	if qps <= 0 {
		return -1
	}
	return qps
}

// mergePatch validates one patch and applies it to the current effective
// setting.
func (s *SettingsService) mergePatch(spec Spec, current Setting, patch ProviderPatch) (Setting, error) {
	next := current
	next.Config = cloneConfig(current.Config)

	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	if patch.APIKey != nil {
		key, err := sanitizeSecret(*patch.APIKey)
		if err != nil {
			return Setting{}, err
		}
		next.APIKey = key
	}
	if patch.DailyLimit != nil {
		limit := *patch.DailyLimit
		if limit < 0 {
			return Setting{}, invalidSetting("daily_limit: must be >= 0 (0 = unlimited)")
		}
		if spec.MaxDailyLimit > 0 && limit > spec.MaxDailyLimit {
			return Setting{}, invalidSetting(fmt.Sprintf("daily_limit: must be <= %d", spec.MaxDailyLimit))
		}
		next.DailyLimit = limit
	}
	if patch.QPS != nil {
		qps := *patch.QPS
		if math.IsNaN(qps) || math.IsInf(qps, 0) || qps < 0 {
			return Setting{}, invalidSetting("qps: must be >= 0 (0 = unlimited)")
		}
		if qps > MaxQPS {
			return Setting{}, invalidSetting(fmt.Sprintf("qps: must be <= %d", MaxQPS))
		}
		next.QPS = qps
	}
	if patch.TTL != nil {
		ttl, err := time.ParseDuration(strings.TrimSpace(*patch.TTL))
		if err != nil || ttl <= 0 {
			return Setting{}, invalidSetting("ttl: must be a positive Go duration such as \"24h\"")
		}
		if ttl > MaxTTL {
			return Setting{}, invalidSetting(fmt.Sprintf("ttl: must be <= %s", DurationString(MaxTTL)))
		}
		next.TTL = ttl
	}
	if patch.Config != nil {
		merged, err := mergeConfig(next.Config, patch.Config)
		if err != nil {
			return Setting{}, err
		}
		next.Config = merged
	}
	if err := validateProviderConfig(spec, next.Config); err != nil {
		return Setting{}, err
	}
	return next, nil
}

// cloneConfig copies a config map so a rejected patch never mutates the applied
// setting.
func cloneConfig(config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = value
	}
	return out
}

// configKeyPattern is the accepted shape of a provider-private config key.
var configKeyPattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// mergeConfig merges the patch into the persisted config. A null value removes
// the key; everything else is normalised and bounded.
func mergeConfig(base, patch map[string]any) (map[string]any, error) {
	out := cloneConfig(base)
	for key, value := range patch {
		if !configKeyPattern.MatchString(key) {
			return nil, invalidSetting("config: key names must match " + configKeyPattern.String())
		}
		if value == nil {
			delete(out, key)
			continue
		}
		normalized, err := normalizeConfigValue(key, value)
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	if len(out) > MaxConfigKeys {
		return nil, invalidSetting(fmt.Sprintf("config: at most %d keys are accepted", MaxConfigKeys))
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, invalidSetting("config: could not be encoded")
	}
	if len(encoded) > MaxConfigBytes {
		return nil, invalidSetting(fmt.Sprintf("config: at most %d bytes are accepted", MaxConfigBytes))
	}
	return out, nil
}

// normalizeConfigValue bounds and normalises one config value. Values are never
// echoed back in an error message.
func normalizeConfigValue(key string, value any) (any, error) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if len(trimmed) > MaxConfigStringBytes {
			return nil, invalidSetting(fmt.Sprintf("config.%s: at most %d bytes are accepted", key, MaxConfigStringBytes))
		}
		if strings.ContainsFunc(trimmed, isControlRune) {
			return nil, invalidSetting("config." + key + ": control characters are not accepted")
		}
		return trimmed, nil
	case bool:
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, invalidSetting("config." + key + ": must be a finite number")
		}
		if typed == math.Trunc(typed) && math.Abs(typed) <= 1e15 {
			return int64(typed), nil
		}
		return typed, nil
	case []any:
		if len(typed) > MaxConfigListEntries {
			return nil, invalidSetting(fmt.Sprintf("config.%s: at most %d entries are accepted", key, MaxConfigListEntries))
		}
		out := make([]string, 0, len(typed))
		for index, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, invalidSetting(fmt.Sprintf("config.%s[%d]: must be a string", key, index))
			}
			trimmed := strings.TrimSpace(text)
			if len(trimmed) > MaxConfigStringBytes {
				return nil, invalidSetting(fmt.Sprintf("config.%s[%d]: at most %d bytes are accepted", key, index, MaxConfigStringBytes))
			}
			if trimmed == "" {
				return nil, invalidSetting(fmt.Sprintf("config.%s[%d]: must not be empty", key, index))
			}
			out = append(out, trimmed)
		}
		return out, nil
	default:
		return nil, invalidSetting("config." + key + ": must be a string, number, boolean or a list of strings")
	}
}

// ConfigStrings reads a config value as a string list.
func configStrings(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, strings.TrimSpace(text))
		}
		return out, true
	default:
		return nil, false
	}
}

// validateProviderConfig enforces the provider-private config rules of §3: the
// DNSBL zones and resolver, and the mirror URL overrides of the HTTP sources.
func validateProviderConfig(spec Spec, config map[string]any) error {
	if raw, ok := config["url"]; ok {
		text, ok := raw.(string)
		if !ok {
			return invalidSetting("config.url: must be a string")
		}
		parsed, err := url.Parse(text)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return invalidSetting("config.url: must be an absolute http(s) URL")
		}
	}
	switch spec.ID {
	case "dnsbl":
		if raw, ok := config["zones"]; ok {
			zones, ok := configStrings(raw)
			if !ok || len(zones) == 0 {
				return invalidSetting("config.zones: must be a list of 1..64 domain names")
			}
			if len(zones) > MaxConfigListEntries {
				return invalidSetting(fmt.Sprintf("config.zones: at most %d entries are accepted", MaxConfigListEntries))
			}
			for index, zone := range zones {
				if !validHostname(zone) {
					return invalidSetting(fmt.Sprintf("config.zones[%d]: must be a domain name", index))
				}
			}
		}
		if raw, ok := config["resolver"]; ok {
			text, ok := raw.(string)
			if !ok || !validResolverURL(text) {
				return invalidSetting("config.resolver: must look like udp://host:53 or tcp://host:53")
			}
		}
	}
	return nil
}

// validResolverURL accepts the resolver shape ResolverFromURL understands.
func validResolverURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.Scheme != "udp" && parsed.Scheme != "tcp" {
		return false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || strings.TrimSpace(host) == "" {
		return false
	}
	value, err := net.LookupPort("tcp", port)
	return err == nil && value > 0 && value <= 65535
}

// validHostname accepts a DNS name with at least one dot, such as
// zen.spamhaus.org.
func validHostname(raw string) bool {
	name := strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if len(name) < 4 || len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

// sanitizeSecret bounds and cleans a stored credential. The value never appears
// in the returned error.
func sanitizeSecret(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > MaxAPIKeyBytes {
		return "", invalidSetting(fmt.Sprintf("api_key: at most %d bytes are accepted", MaxAPIKeyBytes))
	}
	if strings.ContainsFunc(trimmed, isControlRune) {
		return "", invalidSetting("api_key: control characters are not accepted")
	}
	return trimmed, nil
}

func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// secretConfigKey reports whether a config key may hold a credential. Its value
// is never returned by the API (R6).
func secretConfigKey(spec Spec, key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return true
	}
	for _, field := range spec.CredentialFields {
		if lower == strings.ToLower(strings.TrimSpace(field)) {
			return true
		}
	}
	for _, marker := range []string{"key", "token", "secret", "password", "passwd", "credential", "license"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// publicConfig drops the secret-looking keys so a settings response can never
// carry a credential (R6).
func publicConfig(spec Spec, config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for key, value := range config {
		if secretConfigKey(spec, key) || value == nil {
			continue
		}
		out[key] = value
	}
	return out
}

// status renders the API view of one provider.
func (s *SettingsService) status(spec Spec, setting Setting, usage UsageFunc) ProviderStatus {
	view := ProviderStatus{
		ID:                       spec.ID,
		Name:                     spec.Name,
		Website:                  spec.Website,
		Terms:                    spec.Terms,
		Category:                 spec.Kind.String(),
		ViaNode:                  spec.Kind == KindViaNode,
		Direct:                   spec.Kind == KindOnlineIP,
		Profile:                  spec.Profile,
		BatchSize:                spec.BatchSize,
		RequiresKey:              spec.RequiresKey,
		SupportsIPv6:             spec.SupportsIPv6,
		DefaultEnabled:           spec.DefaultEnabled,
		DefaultDailyLimit:        spec.DefaultDailyLimit,
		DefaultDailyLimitWithKey: spec.DefaultDailyLimitWithKey,
		DefaultQPS:               spec.DefaultQPS,
		DefaultTTL:               spec.TTLString(),
		MaxDailyLimit:            spec.MaxDailyLimit,
		CredentialFields:         append([]string(nil), spec.CredentialFields...),
		Enabled:                  setting.Enabled,
		Runnable:                 setting.Runnable(),
		HasKey:                   setting.HasKey(),
		DailyLimit:               setting.DailyLimit,
		QPS:                      setting.QPS,
		TTL:                      DurationString(setting.EffectiveTTL()),
		Config:                   publicConfig(spec, setting.Config),
		Source:                   setting.Source,
		UpdatedAtNs:              setting.UpdatedAtNs,
		Usage:                    ProviderUsage{Remaining: -1},
		Database:                 s.databaseState(spec.ID),
	}
	if usage == nil {
		return view
	}
	state := usage(spec.ID)
	state.Remaining = -1
	if setting.DailyLimit > 0 {
		state.Remaining = setting.DailyLimit - state.Used
		if state.Remaining < 0 {
			state.Remaining = 0
		}
		state.Exhausted = state.Used >= setting.DailyLimit
	}
	state.NextAllowedAtNs = state.NextRequestAtNs
	if state.BlockedUntilNs > state.NextAllowedAtNs {
		state.NextAllowedAtNs = state.BlockedUntilNs
	}
	view.Usage = state
	return view
}

// invalidSetting builds a validation error.
func invalidSetting(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSetting, message)
}
