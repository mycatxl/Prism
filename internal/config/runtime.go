package config

import "time"

// RuntimeConfig holds all hot-updatable global settings.
// These are persisted in the database and served via GET /system/config.
type RuntimeConfig struct {
	// Request log
	RequestLogEnabled                  bool `json:"request_log_enabled"`
	ReverseProxyLogDetailEnabled       bool `json:"reverse_proxy_log_detail_enabled"`
	ReverseProxyLogReqHeadersMaxBytes  int  `json:"reverse_proxy_log_req_headers_max_bytes"`
	ReverseProxyLogReqBodyMaxBytes     int  `json:"reverse_proxy_log_req_body_max_bytes"`
	ReverseProxyLogRespHeadersMaxBytes int  `json:"reverse_proxy_log_resp_headers_max_bytes"`
	ReverseProxyLogRespBodyMaxBytes    int  `json:"reverse_proxy_log_resp_body_max_bytes"`

	// Health check
	MaxConsecutiveFailures          int      `json:"max_consecutive_failures"`
	MaxLatencyTestInterval          Duration `json:"max_latency_test_interval"`
	MaxAuthorityLatencyTestInterval Duration `json:"max_authority_latency_test_interval"`
	MaxEgressTestInterval           Duration `json:"max_egress_test_interval"`

	// Probe
	LatencyTestURL     string   `json:"latency_test_url"`
	EgressTraceURL     string   `json:"egress_trace_url"`
	LatencyAuthorities []string `json:"latency_authorities"`

	// P2C
	P2CLatencyWindow   Duration `json:"p2c_latency_window"`
	LatencyDecayWindow Duration `json:"latency_decay_window"`

	// Persistence
	CacheFlushInterval       Duration `json:"cache_flush_interval"`
	CacheFlushDirtyThreshold int      `json:"cache_flush_dirty_threshold"`

	// Intel batch detection
	IntelEnabled                  bool   `json:"intel_enabled"`
	IntelNodeWorkers              int    `json:"intel_node_workers"`
	IntelCheckConcurrencyPerCheck int    `json:"intel_check_concurrency_per_check"`
	IntelMaxRunningJobs           int    `json:"intel_max_running_jobs"`
	IntelAutoChecks               bool   `json:"intel_auto_checks"`
	IntelRefreshSchedule          string `json:"intel_refresh_schedule"`

	// Retention and banding. These are the settings the console's own settings
	// page owns; they live here rather than in the environment because
	// changing how much history to keep is an operational decision, not a
	// deployment one.
	//
	// AuditRetentionDays and UsageRetentionDays of 0 mean "use the shipped
	// window" (90 and 400 days), which is what a config persisted before they
	// existed decodes to. They are not a switch for "keep everything": a
	// negative or zero value is answered with the shipped window by
	// RetentionDays and by the audit pruner, and a deployment that wants a
	// longer window writes a larger number (the audit table keeps at most
	// 100000 rows regardless).
	AuditRetentionDays int         `json:"audit_retention_days"`
	UsageRetentionDays int         `json:"usage_retention_days"`
	PurityBands        PurityBands `json:"purity_bands"`

	// Usage alerts. These are the settings the alert scanner reads: how much of
	// a plan's quota one account may spend inside a short window before the
	// console reports it, over how many minutes that window is measured, and how
	// long such a report is kept.
	//
	// A zero in any of them means "use the shipped setting", which is what a
	// config persisted before they existed decodes to. Unlike the two windows
	// above, a patch is refused outright for a value that cannot be acted on -
	// ratio outside (0, 1], window or retention below 1 day/minute - so a
	// persisted zero can only come from an older state.db and is filled in at
	// startup (cmd/prism, loadRuntimeConfig).
	AlertUsageRatio    float64 `json:"alert_usage_ratio"`
	AlertWindowMinutes int     `json:"alert_window_minutes"`
	AlertRetentionDays int     `json:"alert_retention_days"`
}

// DefaultEgressTraceURL is the shipped node egress probe target. It is also the
// fallback for a runtime config persisted before egress_trace_url existed: such
// a row decodes to an empty value and must keep probing the default URL.
const DefaultEgressTraceURL = "https://cloudflare.com/cdn-cgi/trace"

// Shipped retention windows for the two append-only logs the console prunes.
const (
	// DefaultAuditRetentionDays is how long a management write stays readable
	// in the audit log.
	DefaultAuditRetentionDays = 90
	// DefaultUsageRetentionDays is how long a summarized usage day is kept.
	DefaultUsageRetentionDays = 400
)

// Shipped usage-alert settings. They live here rather than in the environment
// for the same reason the retention windows do: how eagerly the console reports
// a single account is an operational decision, not a deployment one - the same
// three fields are hot-updatable through the settings page.
const (
	// DefaultAlertUsageRatio is the share of a plan's quota an account has to
	// reach inside one window to be reported. A third of the quota inside an
	// hour is a pace ordinary use does not reach, and it still leaves time to
	// react before the quota itself is gone.
	DefaultAlertUsageRatio = 0.3
	// DefaultAlertWindowMinutes is the window the ratio is measured over.
	DefaultAlertWindowMinutes = 60
	// DefaultAlertRetentionDays is how long an alert stays readable.
	DefaultAlertRetentionDays = 90
)

// PurityBands are the score thresholds behind the purity band names.
//
// The type is stated here rather than imported from the quality package: config
// is the lowest layer of the program and every package may read it, so it must
// not depend on the domain packages that read it. The conversion is one-way and
// lives at the wiring point (cmd/prism), which is where both sides are known.
//
// A field left unset means the shipped threshold, so a config row persisted
// before the setting existed keeps behaving exactly as it did.
type PurityBands struct {
	Excellent int `json:"excellent"`
	Clean     int `json:"clean"`
	Fair      int `json:"fair"`
	Mixed     int `json:"mixed"`
}

// DefaultPurityBands returns the shipped thresholds.
func DefaultPurityBands() PurityBands {
	return PurityBands{Excellent: 95, Clean: 90, Fair: 80, Mixed: 60}
}

// Normalized returns the bands with any unusable value replaced by the shipped
// one, and refuses a set that does not descend.
//
// Thresholds that are not strictly descending would name a higher score worse
// than a lower one, which silently changes what every platform filter and plan
// IP tier means: such a set is answered with the shipped bands instead.
func (b PurityBands) Normalized() PurityBands {
	if b.Mixed <= 0 || b.Mixed >= b.Fair || b.Fair >= b.Clean || b.Clean >= b.Excellent || b.Excellent > 100 {
		return DefaultPurityBands()
	}
	return b
}

// RetentionDays returns a retention window, falling back to the shipped one.
func RetentionDays(configured, shipped int) int {
	if configured <= 0 {
		return shipped
	}
	return configured
}

// AlertRatio returns the share of a plan quota that has to be reached for an
// account to be reported, falling back to the shipped ratio for a value nothing
// can act on: a ratio outside (0, 1] would either report every account from its
// first byte or name a level that cannot be reached before the quota is already
// spent. A config persisted before the setting existed decodes to 0 and lands
// here.
func AlertRatio(configured float64) float64 {
	if configured <= 0 || configured > 1 {
		return DefaultAlertUsageRatio
	}
	return configured
}

// AlertWindow returns the number of minutes a usage alert is measured over,
// falling back to the shipped window for a value that measures nothing.
func AlertWindow(configured int) int {
	if configured <= 0 {
		return DefaultAlertWindowMinutes
	}
	return configured
}

// NewDefaultRuntimeConfig returns a RuntimeConfig populated with the default
// values specified in the design system
func NewDefaultRuntimeConfig() *RuntimeConfig {
	return &RuntimeConfig{
		RequestLogEnabled:                  true,
		ReverseProxyLogDetailEnabled:       false,
		ReverseProxyLogReqHeadersMaxBytes:  4096,
		ReverseProxyLogReqBodyMaxBytes:     1024,
		ReverseProxyLogRespHeadersMaxBytes: 1024,
		ReverseProxyLogRespBodyMaxBytes:    1024,

		MaxConsecutiveFailures:          3,
		MaxLatencyTestInterval:          Duration(1 * time.Hour),
		MaxAuthorityLatencyTestInterval: Duration(3 * time.Hour),
		MaxEgressTestInterval:           Duration(24 * time.Hour),

		LatencyTestURL:     "https://www.gstatic.com/generate_204",
		EgressTraceURL:     DefaultEgressTraceURL,
		LatencyAuthorities: []string{"gstatic.com", "google.com", "cloudflare.com", "github.com"},

		P2CLatencyWindow:   Duration(10 * time.Minute),
		LatencyDecayWindow: Duration(10 * time.Minute),

		CacheFlushInterval:       Duration(5 * time.Minute),
		CacheFlushDirtyThreshold: 1000,

		IntelEnabled: true,
		// With the via-node interval per node, the worker pool is what decides
		// throughput: every worker can be talking to a different node at the same
		// time, which is the point of routing the lookups through the nodes.
		IntelNodeWorkers: 100,
		// The per-rule ceiling is not a vendor rate: every rule runs through each
		// node's own client, so the node worker pool is what bounds the fan-out.
		// A low ceiling only made items queue up inside the engine while their
		// step timeout was already running.
		IntelCheckConcurrencyPerCheck: 512,
		IntelMaxRunningJobs:           64,
		IntelAutoChecks:               false,
		IntelRefreshSchedule:          "0 4 * * *",
		AuditRetentionDays:            DefaultAuditRetentionDays,
		UsageRetentionDays:            DefaultUsageRetentionDays,
		PurityBands:                   DefaultPurityBands(),
		AlertUsageRatio:               DefaultAlertUsageRatio,
		AlertWindowMinutes:            DefaultAlertWindowMinutes,
		AlertRetentionDays:            DefaultAlertRetentionDays,
	}
}
