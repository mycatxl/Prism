// Package model defines domain structs shared across the persistence layer.
package model

import (
	"encoding/json"
	"time"
)

// Platform represents a routing platform.
type Platform struct {
	ID                               string `json:"id"`
	Name                             string `json:"name"`
	StickyTTLNs                      int64  `json:"sticky_ttl_ns"`
	RegexFilters                     []string
	RegionFilters                    []string
	QualityPolicy                    QualityPolicy `json:"quality_policy"`
	ReverseProxyMissAction           string        `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior string        `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   string        `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 string        `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    bool          `json:"passive_circuit_breaker_disabled"`
	ScheduledRotationIntervalNs      int64         `json:"scheduled_rotation_interval_ns"`
	ScheduledRotationEnabled         bool          `json:"scheduled_rotation_enabled"`
	RotationAvoidPreviousIP          bool          `json:"rotation_avoid_previous_ip"`
	UpdatedAtNs                      int64         `json:"updated_at_ns"`
}

// QualityPolicy defines quality-based routing admission rules.
//
// This is the single persisted and runtime representation of a quality policy;
// the platform package operates on this type directly.
//
// The zero value is empty and means "no quality admission", which reproduces
// the upstream Resin behaviour. Use IsEmpty to test it.
type QualityPolicy struct {
	MinPurity *int `json:"min_purity,omitempty"` // 0..100

	// IPTypes restricts accepted network types:
	// residential|mobile|business|wireless|datacenter|non_residential.
	IPTypes []string `json:"ip_types,omitempty"`

	// AllowedVerdicts restricts accepted assessment verdicts:
	// favorable|caution|review|high_risk|conflicting|incomplete.
	AllowedVerdicts []string `json:"allowed_verdicts,omitempty"`

	// MinConfidence requires a minimum assessment confidence:
	// ""|low|medium|high.
	MinConfidence string `json:"min_confidence,omitempty"`

	// RequireNative rejects nodes whose egress is not native to the observed region.
	RequireNative bool `json:"require_native,omitempty"`

	// RequiredChecks maps a check_id to the required outcome,
	// for example {"chatgpt":"available"}.
	RequiredChecks map[string]string `json:"required_checks,omitempty"`

	// MaxAssessmentAge is the maximum age of the newest evidence, as a Go
	// duration string. Empty means unlimited.
	MaxAssessmentAge string `json:"max_assessment_age,omitempty"`

	// MaxEgressAge is the maximum age of the egress IP observation, as a Go
	// duration string. Empty means unlimited.
	MaxEgressAge string `json:"max_egress_age,omitempty"`

	// UnknownAction controls nodes without a usable assessment:
	// "" (equivalent to exclude), allow, or exclude.
	UnknownAction string `json:"unknown_action,omitempty"`

	// ExcludeTor rejects nodes identified as Tor exit/relay. nil means true.
	ExcludeTor *bool `json:"exclude_tor,omitempty"`

	// ExcludeHighRisk rejects nodes whose verdict is high_risk. nil means true.
	ExcludeHighRisk *bool `json:"exclude_high_risk,omitempty"`
}

// qualityPolicyWire is the current on-disk encoding of QualityPolicy.
type qualityPolicyWire struct {
	MinPurity        *int              `json:"min_purity,omitempty"`
	IPTypes          []string          `json:"ip_types,omitempty"`
	AllowedVerdicts  []string          `json:"allowed_verdicts,omitempty"`
	MinConfidence    string            `json:"min_confidence,omitempty"`
	RequireNative    bool              `json:"require_native,omitempty"`
	RequiredChecks   map[string]string `json:"required_checks,omitempty"`
	MaxAssessmentAge string            `json:"max_assessment_age,omitempty"`
	MaxEgressAge     string            `json:"max_egress_age,omitempty"`
	UnknownAction    string            `json:"unknown_action,omitempty"`
	ExcludeTor       *bool             `json:"exclude_tor,omitempty"`
	ExcludeHighRisk  *bool             `json:"exclude_high_risk,omitempty"`
}

// legacyQualityPolicyWire carries keys written by older Prism builds. They are
// accepted on decode and never written back.
type legacyQualityPolicyWire struct {
	MinScore                *int  `json:"min_score,omitempty"`
	MaxAssessmentAgeSeconds int64 `json:"max_assessment_age_seconds,omitempty"`
	MaxEgressAgeSeconds     int64 `json:"max_egress_age_seconds,omitempty"`
}

// IsEmpty reports whether the policy enforces nothing at all.
func (q QualityPolicy) IsEmpty() bool {
	return q.MinPurity == nil &&
		len(q.IPTypes) == 0 &&
		len(q.AllowedVerdicts) == 0 &&
		q.MinConfidence == "" &&
		!q.RequireNative &&
		len(q.RequiredChecks) == 0 &&
		q.MaxAssessmentAge == "" &&
		q.MaxEgressAge == "" &&
		q.UnknownAction == "" &&
		q.ExcludeTor == nil &&
		q.ExcludeHighRisk == nil
}

// MarshalJSON writes only the current keys.
func (q QualityPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(qualityPolicyWire{
		MinPurity:        q.MinPurity,
		IPTypes:          q.IPTypes,
		AllowedVerdicts:  q.AllowedVerdicts,
		MinConfidence:    q.MinConfidence,
		RequireNative:    q.RequireNative,
		RequiredChecks:   q.RequiredChecks,
		MaxAssessmentAge: q.MaxAssessmentAge,
		MaxEgressAge:     q.MaxEgressAge,
		UnknownAction:    q.UnknownAction,
		ExcludeTor:       q.ExcludeTor,
		ExcludeHighRisk:  q.ExcludeHighRisk,
	})
}

// UnmarshalJSON accepts both the current keys and the legacy ones
// (min_score, max_assessment_age_seconds, max_egress_age_seconds).
// The removed keys profile_id, pending_action and conflict_action are ignored.
func (q *QualityPolicy) UnmarshalJSON(data []byte) error {
	var wire qualityPolicyWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var legacy legacyQualityPolicyWire
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	*q = QualityPolicy{
		MinPurity:        wire.MinPurity,
		IPTypes:          wire.IPTypes,
		AllowedVerdicts:  wire.AllowedVerdicts,
		MinConfidence:    wire.MinConfidence,
		RequireNative:    wire.RequireNative,
		RequiredChecks:   wire.RequiredChecks,
		MaxAssessmentAge: wire.MaxAssessmentAge,
		MaxEgressAge:     wire.MaxEgressAge,
		UnknownAction:    wire.UnknownAction,
		ExcludeTor:       wire.ExcludeTor,
		ExcludeHighRisk:  wire.ExcludeHighRisk,
	}

	if q.MinPurity == nil && legacy.MinScore != nil {
		q.MinPurity = legacy.MinScore
	}
	if q.MaxAssessmentAge == "" && legacy.MaxAssessmentAgeSeconds > 0 {
		q.MaxAssessmentAge = (time.Duration(legacy.MaxAssessmentAgeSeconds) * time.Second).String()
	}
	if q.MaxEgressAge == "" && legacy.MaxEgressAgeSeconds > 0 {
		q.MaxEgressAge = (time.Duration(legacy.MaxEgressAgeSeconds) * time.Second).String()
	}
	return nil
}

// Subscription represents a node subscription source.
type Subscription struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	SourceType                string `json:"source_type"`
	URL                       string `json:"url"`
	Content                   string `json:"content"`
	UpdateIntervalNs          int64  `json:"update_interval_ns"`
	Enabled                   bool   `json:"enabled"`
	Ephemeral                 bool   `json:"ephemeral"`
	IncrementalAliveNodes     bool   `json:"incremental_alive_nodes"`
	EphemeralNodeEvictDelayNs int64  `json:"ephemeral_node_evict_delay_ns"`
	AutoIntel                 bool   `json:"auto_intel"`
	UserAgent                 string `json:"user_agent"`
	LastParseReportJSON       string `json:"-"`
	CreatedAtNs               int64  `json:"created_at_ns"`
	UpdatedAtNs               int64  `json:"updated_at_ns"`
}

// Endpoint represents a persisted custom inbound listener. The environment-
// defined default endpoint is synthesized at runtime and is never stored here.
type Endpoint struct {
	ID                   string `json:"id"`
	Port                 int    `json:"port"`
	Enabled              bool   `json:"enabled"`
	AllowManagement      bool   `json:"allow_management"`
	AllowProxy           bool   `json:"allow_proxy"`
	RequireProxyAuthInfo bool   `json:"require_proxy_auth_info"`
	AllowHTTPForward     bool   `json:"allow_http_forward"`
	AllowHTTPReverse     bool   `json:"allow_http_reverse"`
	AllowSOCKS5          bool   `json:"allow_socks5"`
	CreatedAtNs          int64  `json:"created_at_ns"`
	UpdatedAtNs          int64  `json:"updated_at_ns"`
}

// AccountHeaderRule defines header extraction rules for reverse proxy account matching.
type AccountHeaderRule struct {
	URLPrefix   string `json:"url_prefix"`
	Headers     []string
	UpdatedAtNs int64 `json:"updated_at_ns"`
}

// NodeStatic holds the immutable portion of a node's data.
type NodeStatic struct {
	Hash        string          `json:"hash"`
	RawOptions  json.RawMessage `json:"raw_options_json"`
	CreatedAtNs int64           `json:"created_at_ns"`
}

// NodeDynamic holds the mutable runtime state of a node.
type NodeDynamic struct {
	Hash                               string `json:"hash"`
	FailureCount                       int    `json:"failure_count"`
	CircuitOpenSince                   int64  `json:"circuit_open_since"`
	EgressIP                           string `json:"egress_ip"`
	EgressRegion                       string `json:"egress_region"`
	EgressUpdatedAtNs                  int64  `json:"egress_updated_at_ns"`
	LastLatencyProbeAttemptNs          int64  `json:"last_latency_probe_attempt_ns"`
	LastAuthorityLatencyProbeAttemptNs int64  `json:"last_authority_latency_probe_attempt_ns"`
	LastEgressUpdateAttemptNs          int64  `json:"last_egress_update_attempt_ns"`
}

// NodeLatency holds per-domain latency statistics for a node.
type NodeLatency struct {
	NodeHash      string `json:"node_hash"`
	Domain        string `json:"domain"`
	EwmaNs        int64  `json:"ewma_ns"`
	LastUpdatedNs int64  `json:"last_updated_ns"`
}

// NodeLatencyKey is the composite primary key for node_latency.
type NodeLatencyKey struct {
	NodeHash string
	Domain   string
}

// Lease represents a sticky routing lease.
type Lease struct {
	PlatformID     string `json:"platform_id"`
	Account        string `json:"account"`
	NodeHash       string `json:"node_hash"`
	EgressIP       string `json:"egress_ip"`
	CreatedAtNs    int64  `json:"created_at_ns"`
	ExpiryNs       int64  `json:"expiry_ns"`
	LastAccessedNs int64  `json:"last_accessed_ns"`
}

// LeaseKey is the composite primary key for leases.
type LeaseKey struct {
	PlatformID string
	Account    string
}

// SubscriptionNode links a subscription to a node with tags.
type SubscriptionNode struct {
	SubscriptionID string `json:"subscription_id"`
	NodeHash       string `json:"node_hash"`
	Tags           []string
	Evicted        bool `json:"evicted"`
}

// SubscriptionNodeKey is the composite primary key for subscription_nodes.
type SubscriptionNodeKey struct {
	SubscriptionID string
	NodeHash       string
}

// IntelProviderSetting persists per-provider intel configuration. APIKey is
// never serialized; the API exposes only has_key.
type IntelProviderSetting struct {
	ProviderID  string  `json:"provider_id"`
	Enabled     bool    `json:"enabled"`
	APIKey      string  `json:"-"`
	DailyLimit  int     `json:"daily_limit"`
	QPS         float64 `json:"qps"`
	TTLNs       int64   `json:"ttl_ns"`
	ConfigJSON  string  `json:"config_json"`
	UpdatedAtNs int64   `json:"updated_at_ns"`
}

// ExportProfile is a saved export configuration plus its subscription token.
type ExportProfile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Format         string `json:"format"`
	TokenSHA256    string `json:"-"`
	PlatformID     string `json:"platform_id"`
	FilterJSON     string `json:"filter_json"`
	NameTemplate   string `json:"name_template"`
	Enabled        bool   `json:"enabled"`
	LastAccessAtNs int64  `json:"last_access_at_ns"`
	AccessCount    int64  `json:"access_count"`
	CreatedAtNs    int64  `json:"created_at_ns"`
	UpdatedAtNs    int64  `json:"updated_at_ns"`
}

// AuditActorExportPrefix marks the actor of a public subscription access
// (WP11 §4.3.4). Those entries are written for callers that hold nothing but a
// subscription URL, so they are retained in a bucket of their own and can never
// displace a management entry out of the audit trail.
const AuditActorExportPrefix = "export:"

// AuditEntry is one recorded administrative mutation. Detail holds JSON text.
type AuditEntry struct {
	ID         int64  `json:"id"`
	AtNs       int64  `json:"at_ns"`
	Actor      string `json:"actor"`
	RemoteAddr string `json:"remote_addr"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Detail     string `json:"detail"`
}
