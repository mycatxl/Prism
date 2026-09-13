// Package config handles environment-based configuration loading and runtime config models.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"prism/internal/platform"
)

// EnvConfig holds all environment-variable-driven settings (not hot-updatable).
type EnvConfig struct {
	// Inspection keys are never serialized as environment diagnostics.
	Quality QualityConfig `json:"-"`
	// Directories
	CacheDir string
	StateDir string
	LogDir   string

	// Network
	ListenAddress string

	// Ports
	ProxyPort       int
	APIMaxBodyBytes int

	// Core
	MaxLatencyTableEntries                          int
	ProbeConcurrency                                int
	GeoIPUpdateSchedule                             string
	DefaultPlatformStickyTTL                        time.Duration
	DefaultPlatformRegexFilters                     []string
	DefaultPlatformRegionFilters                    []string
	DefaultPlatformReverseProxyMissAction           string
	DefaultPlatformReverseProxyEmptyAccountBehavior string
	DefaultPlatformReverseProxyFixedAccountHeader   string
	DefaultPlatformAllocationPolicy                 string
	ProbeTimeout                                    time.Duration
	ResourceFetchTimeout                            time.Duration
	ResourceFetchMaxBytes                           int
	NodeDNSUpstreams                                []string
	ProxyTransportMaxIdleConns                      int
	ProxyTransportMaxIdleConnsPerHost               int
	ProxyTransportIdleConnTimeout                   time.Duration
	ProxyBypassRules                                []string

	// Request log
	RequestLogQueueSize           int
	RequestLogQueueFlushBatchSize int
	RequestLogQueueFlushInterval  time.Duration
	RequestLogDBMaxMB             int
	RequestLogDBRetainCount       int

	// Auth
	AuthVersion AuthVersion
	AdminToken  string
	ProxyToken  string

	// Metrics
	MetricThroughputIntervalSeconds   int
	MetricThroughputRetentionSeconds  int
	MetricBucketSeconds               int
	MetricConnectionsIntervalSeconds  int
	MetricConnectionsRetentionSeconds int
	MetricLeasesIntervalSeconds       int
	MetricLeasesRetentionSeconds      int
	MetricLatencyBinWidthMS           int
	MetricLatencyBinOverflowMS        int
}

// DefaultNodeDNSUpstreams returns the default node DNS upstream URI list.
func DefaultNodeDNSUpstreams() []string {
	return []string{
		"https://doh.pub/dns-query",
		"https://dns.alidns.com/dns-query",
		"tls://223.5.5.5?sni=dns.alidns.com",
		"local",
	}
}

// LoadEnvConfig reads environment variables and returns a validated EnvConfig.
// Returns an error if any required variable is missing or any value is invalid.
func LoadEnvConfig() (*EnvConfig, error) {
	cfg := &EnvConfig{}
	var errs []string
	qualityConfig, qualityErr := LoadQualityConfig()
	if qualityErr != nil {
		errs = append(errs, qualityErr.Error())
	}
	cfg.Quality = qualityConfig

	// --- Directories ---
	cfg.CacheDir = envStr("PRISM_CACHE_DIR", "./.local/cache")
	cfg.StateDir = envStr("PRISM_STATE_DIR", "./.local/state")
	cfg.LogDir = envStr("PRISM_LOG_DIR", "./.local/logs")
	cfg.ListenAddress = strings.TrimSpace(envStr("PRISM_LISTEN_ADDRESS", "127.0.0.1"))

	// --- Ports ---
	cfg.ProxyPort = envInt("PRISM_PORT", 1080, &errs)
	cfg.APIMaxBodyBytes = envInt("PRISM_API_MAX_BODY_BYTES", 1<<20, &errs)

	// --- Core ---
	cfg.MaxLatencyTableEntries = envInt("PRISM_MAX_LATENCY_TABLE_ENTRIES", 12, &errs)
	cfg.ProbeConcurrency = envInt("PRISM_PROBE_CONCURRENCY", 32, &errs)
	cfg.GeoIPUpdateSchedule = envStr("PRISM_GEOIP_UPDATE_SCHEDULE", "0 7 * * *")
	cfg.DefaultPlatformStickyTTL = envDuration("PRISM_DEFAULT_PLATFORM_STICKY_TTL", 7*24*time.Hour, &errs)
	cfg.DefaultPlatformRegexFilters = envStringSlice("PRISM_DEFAULT_PLATFORM_REGEX_FILTERS", []string{}, &errs)
	cfg.DefaultPlatformRegionFilters = envStringSlice("PRISM_DEFAULT_PLATFORM_REGION_FILTERS", []string{}, &errs)
	cfg.DefaultPlatformReverseProxyMissAction = envStr(
		"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION",
		string(platform.ReverseProxyMissActionTreatAsEmpty),
	)
	cfg.DefaultPlatformReverseProxyEmptyAccountBehavior = envStr(
		"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR",
		string(platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule),
	)
	cfg.DefaultPlatformReverseProxyFixedAccountHeader = strings.TrimSpace(envStr(
		"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER",
		"Authorization",
	))
	cfg.DefaultPlatformAllocationPolicy = envStr(
		"PRISM_DEFAULT_PLATFORM_ALLOCATION_POLICY",
		string(platform.AllocationPolicyBalanced),
	)
	cfg.ProbeTimeout = envDuration("PRISM_PROBE_TIMEOUT", 15*time.Second, &errs)
	cfg.ResourceFetchTimeout = envDuration("PRISM_RESOURCE_FETCH_TIMEOUT", 30*time.Second, &errs)
	cfg.ResourceFetchMaxBytes = envInt("PRISM_RESOURCE_FETCH_MAX_BYTES", 32<<20, &errs)
	cfg.NodeDNSUpstreams = envStringSlice("PRISM_NODE_DNS_UPSTREAMS", DefaultNodeDNSUpstreams(), &errs)
	cfg.ProxyTransportMaxIdleConns = envInt("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS", 1024, &errs)
	cfg.ProxyTransportMaxIdleConnsPerHost = envInt("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST", 64, &errs)
	cfg.ProxyTransportIdleConnTimeout = envDuration("PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT", 90*time.Second, &errs)
	cfg.ProxyBypassRules = envDelimitedStringSlice("PRISM_PROXY_BYPASS", []string{})

	// --- Request log ---
	cfg.RequestLogQueueSize = envInt("PRISM_REQUEST_LOG_QUEUE_SIZE", 8192, &errs)
	cfg.RequestLogQueueFlushBatchSize = envInt("PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE", 4096, &errs)
	cfg.RequestLogQueueFlushInterval = envDuration("PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL", 5*time.Minute, &errs)
	cfg.RequestLogDBMaxMB = envInt("PRISM_REQUEST_LOG_DB_MAX_MB", 512, &errs)
	cfg.RequestLogDBRetainCount = envInt("PRISM_REQUEST_LOG_DB_RETAIN_COUNT", 2, &errs)

	// --- Auth (Prism never starts listeners with missing or empty tokens) ---
	authVersionRaw := os.Getenv("PRISM_AUTH_VERSION")
	adminToken, hasAdminToken := os.LookupEnv("PRISM_ADMIN_TOKEN")
	proxyToken, hasProxyToken := os.LookupEnv("PRISM_PROXY_TOKEN")
	cfg.AuthVersion = AuthVersionV1
	if strings.TrimSpace(authVersionRaw) != "" {
		cfg.AuthVersion = NormalizeAuthVersion(authVersionRaw)
	}
	cfg.AdminToken = adminToken
	cfg.ProxyToken = proxyToken

	// --- Metrics ---
	cfg.MetricThroughputIntervalSeconds = envInt("PRISM_METRIC_THROUGHPUT_INTERVAL_SECONDS", 2, &errs)
	cfg.MetricThroughputRetentionSeconds = envInt("PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS", 3600, &errs)
	cfg.MetricBucketSeconds = envInt("PRISM_METRIC_BUCKET_SECONDS", 3600, &errs)
	cfg.MetricConnectionsIntervalSeconds = envInt("PRISM_METRIC_CONNECTIONS_INTERVAL_SECONDS", 15, &errs)
	cfg.MetricConnectionsRetentionSeconds = envInt("PRISM_METRIC_CONNECTIONS_RETENTION_SECONDS", 18000, &errs)
	cfg.MetricLeasesIntervalSeconds = envInt("PRISM_METRIC_LEASES_INTERVAL_SECONDS", 5, &errs)
	cfg.MetricLeasesRetentionSeconds = envInt("PRISM_METRIC_LEASES_RETENTION_SECONDS", 18000, &errs)
	cfg.MetricLatencyBinWidthMS = envInt("PRISM_METRIC_LATENCY_BIN_WIDTH_MS", 100, &errs)
	cfg.MetricLatencyBinOverflowMS = envInt("PRISM_METRIC_LATENCY_BIN_OVERFLOW_MS", 3000, &errs)

	// --- Validation ---
	if cfg.AuthVersion == "" {
		errs = append(
			errs,
			fmt.Sprintf(
				"PRISM_AUTH_VERSION: invalid value %q (allowed: %s)",
				authVersionRaw,
				AuthVersionV1,
			),
		)
	}

	if !hasAdminToken || strings.TrimSpace(cfg.AdminToken) == "" {
		errs = append(errs, "PRISM_ADMIN_TOKEN must be defined and non-empty; run prism init to generate private tokens")
	}
	if !hasProxyToken || strings.TrimSpace(cfg.ProxyToken) == "" {
		errs = append(errs, "PRISM_PROXY_TOKEN must be defined and non-empty; run prism init to generate private tokens")
	} else {
		if cfg.ProxyToken != "" {
			if err := ValidateProxyTokenForV1(cfg.ProxyToken); err != nil {
				errs = append(errs, fmt.Sprintf("PRISM_PROXY_TOKEN: %v", err))
			}
		}
		if cfg.ProxyToken == "api" || cfg.ProxyToken == "healthz" || cfg.ProxyToken == "ui" {
			errs = append(errs, "PRISM_PROXY_TOKEN must not be reserved keyword: api, healthz, ui")
		}
	}
	if cfg.ListenAddress == "" {
		errs = append(errs, "PRISM_LISTEN_ADDRESS must not be empty")
	}

	validatePort("PRISM_PORT", cfg.ProxyPort, &errs)
	validatePositive("PRISM_API_MAX_BODY_BYTES", cfg.APIMaxBodyBytes, &errs)

	validatePositive("PRISM_MAX_LATENCY_TABLE_ENTRIES", cfg.MaxLatencyTableEntries, &errs)
	if cfg.MaxLatencyTableEntries > 32 {
		errs = append(errs, "PRISM_MAX_LATENCY_TABLE_ENTRIES must be <= 32")
	}
	validatePositive("PRISM_PROBE_CONCURRENCY", cfg.ProbeConcurrency, &errs)
	if cfg.ProbeConcurrency > 10000 {
		errs = append(errs, "PRISM_PROBE_CONCURRENCY must be <= 10000")
	}
	if _, err := cron.ParseStandard(cfg.GeoIPUpdateSchedule); err != nil {
		errs = append(errs, fmt.Sprintf("PRISM_GEOIP_UPDATE_SCHEDULE: invalid cron expression %q: %v", cfg.GeoIPUpdateSchedule, err))
	}
	if cfg.DefaultPlatformStickyTTL <= 0 {
		errs = append(errs, "PRISM_DEFAULT_PLATFORM_STICKY_TTL must be positive")
	}
	if _, err := platform.CompileRegexFilters(cfg.DefaultPlatformRegexFilters); err != nil {
		errs = append(errs, fmt.Sprintf("PRISM_DEFAULT_PLATFORM_REGEX_FILTERS: %v", err))
	}
	if err := platform.ValidateRegionFilters(cfg.DefaultPlatformRegionFilters); err != nil {
		errs = append(errs, fmt.Sprintf("PRISM_DEFAULT_PLATFORM_REGION_FILTERS: %v", err))
	}
	normalizedMissAction := platform.NormalizeReverseProxyMissAction(cfg.DefaultPlatformReverseProxyMissAction)
	if normalizedMissAction == "" {
		errs = append(errs, fmt.Sprintf(
			"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION: invalid value %q (allowed: %s, %s)",
			cfg.DefaultPlatformReverseProxyMissAction,
			platform.ReverseProxyMissActionTreatAsEmpty,
			platform.ReverseProxyMissActionReject,
		))
	} else {
		cfg.DefaultPlatformReverseProxyMissAction = string(normalizedMissAction)
	}
	if !platform.ReverseProxyEmptyAccountBehavior(cfg.DefaultPlatformReverseProxyEmptyAccountBehavior).IsValid() {
		errs = append(errs, fmt.Sprintf(
			"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR: invalid value %q (allowed: %s, %s, %s)",
			cfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
			platform.ReverseProxyEmptyAccountBehaviorRandom,
			platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
			platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule,
		))
	}
	normalizedFixedHeaders, fixedHeaders, fixedHeadersErr := platform.NormalizeFixedAccountHeaders(
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
	)
	if fixedHeadersErr != nil {
		errs = append(
			errs,
			fmt.Sprintf(
				"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER: %v",
				fixedHeadersErr,
			),
		)
	} else {
		cfg.DefaultPlatformReverseProxyFixedAccountHeader = normalizedFixedHeaders
	}
	if cfg.DefaultPlatformReverseProxyEmptyAccountBehavior == string(platform.ReverseProxyEmptyAccountBehaviorFixedHeader) &&
		len(fixedHeaders) == 0 {
		errs = append(errs,
			"PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER: required when PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR is FIXED_HEADER",
		)
	}
	if !platform.AllocationPolicy(cfg.DefaultPlatformAllocationPolicy).IsValid() {
		errs = append(errs, fmt.Sprintf(
			"PRISM_DEFAULT_PLATFORM_ALLOCATION_POLICY: invalid value %q (allowed: %s, %s, %s)",
			cfg.DefaultPlatformAllocationPolicy,
			platform.AllocationPolicyBalanced,
			platform.AllocationPolicyPreferLowLatency,
			platform.AllocationPolicyPreferIdleIP,
		))
	}
	if cfg.ProbeTimeout <= 0 {
		errs = append(errs, "PRISM_PROBE_TIMEOUT must be positive")
	}
	if cfg.ResourceFetchTimeout <= 0 {
		errs = append(errs, "PRISM_RESOURCE_FETCH_TIMEOUT must be positive")
	}
	validatePositive("PRISM_RESOURCE_FETCH_MAX_BYTES", cfg.ResourceFetchMaxBytes, &errs)
	if cfg.ResourceFetchMaxBytes > 256<<20 {
		errs = append(errs, "PRISM_RESOURCE_FETCH_MAX_BYTES must not exceed 256 MiB")
	}
	if len(cfg.NodeDNSUpstreams) == 0 {
		errs = append(errs, "PRISM_NODE_DNS_UPSTREAMS must contain at least one DNS upstream when defined")
	}
	for i, upstream := range cfg.NodeDNSUpstreams {
		if strings.TrimSpace(upstream) == "" {
			errs = append(errs, fmt.Sprintf("PRISM_NODE_DNS_UPSTREAMS[%d] must not be empty", i))
		}
	}
	validatePositive("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS", cfg.ProxyTransportMaxIdleConns, &errs)
	validatePositive("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST", cfg.ProxyTransportMaxIdleConnsPerHost, &errs)
	if cfg.ProxyTransportIdleConnTimeout <= 0 {
		errs = append(errs, "PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT must be positive")
	}
	if cfg.ProxyTransportMaxIdleConnsPerHost > cfg.ProxyTransportMaxIdleConns {
		errs = append(
			errs,
			"PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST must be less than or equal to PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS",
		)
	}
	validatePositive("PRISM_REQUEST_LOG_QUEUE_SIZE", cfg.RequestLogQueueSize, &errs)
	validatePositive("PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE", cfg.RequestLogQueueFlushBatchSize, &errs)
	validatePositive("PRISM_REQUEST_LOG_DB_MAX_MB", cfg.RequestLogDBMaxMB, &errs)
	validatePositive("PRISM_REQUEST_LOG_DB_RETAIN_COUNT", cfg.RequestLogDBRetainCount, &errs)
	validatePositive("PRISM_METRIC_THROUGHPUT_INTERVAL_SECONDS", cfg.MetricThroughputIntervalSeconds, &errs)
	validatePositive("PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS", cfg.MetricThroughputRetentionSeconds, &errs)
	validatePositive("PRISM_METRIC_BUCKET_SECONDS", cfg.MetricBucketSeconds, &errs)
	validatePositive("PRISM_METRIC_CONNECTIONS_INTERVAL_SECONDS", cfg.MetricConnectionsIntervalSeconds, &errs)
	validatePositive("PRISM_METRIC_CONNECTIONS_RETENTION_SECONDS", cfg.MetricConnectionsRetentionSeconds, &errs)
	validatePositive("PRISM_METRIC_LEASES_INTERVAL_SECONDS", cfg.MetricLeasesIntervalSeconds, &errs)
	validatePositive("PRISM_METRIC_LEASES_RETENTION_SECONDS", cfg.MetricLeasesRetentionSeconds, &errs)
	validatePositive("PRISM_METRIC_LATENCY_BIN_WIDTH_MS", cfg.MetricLatencyBinWidthMS, &errs)
	validatePositive("PRISM_METRIC_LATENCY_BIN_OVERFLOW_MS", cfg.MetricLatencyBinOverflowMS, &errs)

	if cfg.RequestLogQueueFlushInterval <= 0 {
		errs = append(errs, "PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL must be positive")
	}

	// Queue size must be >= 2x batch size
	if cfg.RequestLogQueueSize < 2*cfg.RequestLogQueueFlushBatchSize {
		errs = append(errs, "PRISM_REQUEST_LOG_QUEUE_SIZE must be at least 2x PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}

	return cfg, nil
}

// --- helpers ---

func envStr(key, defaultVal string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int, errs *[]string) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid integer %q", key, v))
		return defaultVal
	}
	return n
}

func envDuration(key string, defaultVal time.Duration, errs *[]string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid duration %q", key, v))
		return defaultVal
	}
	return d
}

func envStringSlice(key string, defaultVal []string, errs *[]string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid JSON string array %q", key, v))
		return defaultVal
	}
	if out == nil {
		return []string{}
	}
	return out
}

func envDelimitedStringSlice(key string, defaultVal []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	return splitDelimitedStringSlice(v)
}

func splitDelimitedStringSlice(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func validatePort(name string, value int, errs *[]string) {
	if value < 1 || value > 65535 {
		*errs = append(*errs, fmt.Sprintf("%s: port must be 1-65535, got %d", name, value))
	}
}

func validatePositive(name string, value int, errs *[]string) {
	if value <= 0 {
		*errs = append(*errs, fmt.Sprintf("%s: must be positive, got %d", name, value))
	}
}

const (
	v1ProxyTokenForbiddenChars   = ".:|/\\@?#%~"
	v1ProxyTokenForbiddenSpacing = " \t\r\n"
)

// ValidateProxyTokenForV1 validates proxy token constraints used by auth version V1.
func ValidateProxyTokenForV1(token string) error {
	if strings.ContainsAny(token, v1ProxyTokenForbiddenChars) {
		return fmt.Errorf("must not contain any of %q", v1ProxyTokenForbiddenChars)
	}
	if strings.ContainsAny(token, v1ProxyTokenForbiddenSpacing) {
		return fmt.Errorf("must not contain spaces, tabs, newlines, or carriage returns")
	}
	return nil
}
