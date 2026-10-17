// Package config handles environment-based configuration loading and runtime config models.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// ListenAddress is the primary listener host (UI + API + proxies).
	ListenAddress string
	// AdminListen is the optional independent management listener "host:port".
	// An empty value disables it: /ui, /api and /healthz stay on the primary
	// listener only.
	AdminListen string

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
	EgressTraceURL                                  string
	ResourceFetchTimeout                            time.Duration
	ResourceFetchMaxBytes                           int
	// ResourceFetchAllowPrivate lets subscription/resource downloads reach
	// loopback and private addresses (off by default to block SSRF).
	ResourceFetchAllowPrivate         bool
	NodeDNSUpstreams                  []string
	ProxyTransportMaxIdleConns        int
	ProxyTransportMaxIdleConnsPerHost int
	ProxyTransportIdleConnTimeout     time.Duration
	// ProxyTransportResponseHeaderTimeout bounds the wait for upstream response
	// headers after the request was written. Without it an upstream that
	// accepts the connection and never answers pins the request forever (the
	// main listener has no read timeout: the SSE endpoint needs it).
	ProxyTransportResponseHeaderTimeout time.Duration
	// ProxyTransportTLSHandshakeTimeout bounds the TLS handshake of a bypassed
	// HTTPS target.
	ProxyTransportTLSHandshakeTimeout time.Duration
	// ProxyTransportDialTimeout bounds the dial phase of an outbound HTTP
	// request, including a node outbound's own dial, and the local direct
	// dialer used by the bypass branch, CONNECT and SOCKS5.
	ProxyTransportDialTimeout time.Duration
	ProxyBypassRules          []string
	// TrustedProxies lists the CIDRs (or plain IP literals) whose
	// X-Forwarded-For header is trusted when deriving the client IP used for
	// authentication failure limiting. Empty means "trust no proxy".
	//
	// Only list a proxy that appends the peer address it observed to
	// X-Forwarded-For: a proxy that forwards a client-supplied header verbatim
	// would let a client choose its own limit bucket.
	TrustedProxies []string
	// DirectDenyPrivate refuses loopback, private and link-local targets on the
	// local direct reverse-proxy path (PRISM_DIRECT_DENY_PRIVATE).
	DirectDenyPrivate bool
	// BlockedDestinationPorts are the destination ports refused on every local
	// direct dial (PRISM_BLOCKED_DESTINATION_PORTS). The shipped default is the
	// SMTP set 25/465/587, so an open deployment is not an outbound mail relay;
	// an empty list disables the port policy and leaves DirectDenyPrivate in
	// charge of the addresses.
	BlockedDestinationPorts []int
	// DenyPrivateNodes refuses a node whose server names loopback, the LAN, a
	// link-local address or a cloud metadata endpoint, resolving the name when it
	// is not a literal (PRISM_DENY_PRIVATE_NODES).
	//
	// It is separate from DirectDenyPrivate because the two protect different
	// things: DirectDenyPrivate governs the direct (bypass) branch of the local
	// proxies, while this governs the nodes themselves. A deployment that
	// deliberately routes through a node on a private network (a home server, a
	// jump host) wants the second one off and does not care about the first.
	//
	// A name that resolves only into the transparent-proxy fake-IP pool
	// (198.18.0.0/15) is not refused: on a machine running such a proxy that is
	// what every name resolves to, so the answer carries no information about the
	// target.
	DenyPrivateNodes bool
	// ProxyAuthFailLimit enables the same failure limiter on the proxy entry
	// points. 0 disables it, which keeps the upstream Resin behaviour.
	ProxyAuthFailLimit int
	// RequireAdminTOTP makes a second factor mandatory for administrator
	// accounts (PRISM_REQUIRE_ADMIN_TOTP).
	//
	// It is not a switch that can lock an operator out of their own deployment:
	// an administrator without a second factor reaches the console only far
	// enough to enrol one, which is the only way to satisfy the requirement from
	// inside the product.
	RequireAdminTOTP bool
	// The three startup-policy switches of the authentication layer. They are
	// kept on the config even though the loader is their only reader, so
	// `prism check-config` can print the value that is in force: "the
	// deployment looks configured" and "the deployment still refuses weak
	// tokens" are different statements, and the report has to make both.
	//
	// They are not part of the environment snapshot the console reads
	// (/api/v1/system/config/env): that response is built field by field, and
	// these three are deployment policy, not console state.
	EnforceStrongTokens  bool `json:"-"`
	AllowEmptyProxyToken bool `json:"-"`
	AllowInsecureListen  bool `json:"-"`
	// Usage alerts. These three decide when the console reports a single
	// account as having run ahead of its plan, and how long that report is kept
	// (PRISM_ALERT_USAGE_RATIO, PRISM_ALERT_WINDOW_MINUTES,
	// PRISM_ALERT_RETENTION_DAYS). They seed the matching RuntimeConfig fields
	// at startup and stay hot-updatable there afterwards.
	//
	// AlertUsageRatio is the share of a plan's quota the account has to reach
	// inside AlertWindowMinutes before it is reported. Nothing can act on a
	// ratio outside (0, 1], so such a value refuses the configuration outright.
	AlertUsageRatio    float64
	AlertWindowMinutes int
	AlertRetentionDays int

	// Registration says who may create an account for themselves
	// (PRISM_REGISTRATION). Off by default; see RegistrationMode.
	Registration RegistrationMode

	// Billing is where USDT arrives from (PRISM_USDT_*). The receiving address
	// is the operator's own wallet; the server only reads the chain and holds
	// no key. An empty address turns the whole deposit feature off, which is
	// why it is not an error.
	USDTAddress      string `json:"-"`
	USDTContract     string
	TronGridAPIKey   string `json:"-"`
	TronGridEndpoint string

	// Request log
	RequestLogQueueSize           int
	RequestLogQueueFlushBatchSize int
	RequestLogQueueFlushInterval  time.Duration
	RequestLogDBMaxMB             int
	RequestLogDBRetainCount       int

	// Auth
	AuthVersion AuthVersion
	ProxyToken  string `json:"-"`

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
	// The origin record (origins.go) is what `prism check-config` prints next to
	// each value. It is cleared here, before the first read, so a second load in
	// the same process reports this environment and not the earlier one.
	resetEnvOrigins()
	qualityConfig, qualityErr := LoadQualityConfig()
	if qualityErr != nil {
		errs = append(errs, qualityErr.Error())
	}
	cfg.Quality = qualityConfig

	// --- Directories ---
	cfg.CacheDir = cleanDirPath("PRISM_CACHE_DIR", envStr("PRISM_CACHE_DIR", "./.local/cache"), &errs)
	cfg.StateDir = cleanDirPath("PRISM_STATE_DIR", envStr("PRISM_STATE_DIR", "./.local/state"), &errs)
	cfg.LogDir = cleanDirPath("PRISM_LOG_DIR", envStr("PRISM_LOG_DIR", "./.local/logs"), &errs)
	cfg.ListenAddress = strings.TrimSpace(envStr("PRISM_LISTEN_ADDRESS", "127.0.0.1"))
	// Optional independent management listener (empty disables it).
	cfg.AdminListen = strings.TrimSpace(envStr("PRISM_ADMIN_LISTEN", ""))

	// --- Ports ---
	cfg.ProxyPort = envInt("PRISM_PORT", 2260, &errs)
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
	// PRISM_EGRESS_TRACE_URL points the node egress probe at a reachable
	// endpoint (a local one in a fully offline deployment). Empty means "not
	// set": the persisted runtime config or the shipped default is used
	// instead, so an unrelated value is never clobbered.
	cfg.EgressTraceURL = strings.TrimSpace(envStr("PRISM_EGRESS_TRACE_URL", ""))
	cfg.ResourceFetchTimeout = envDuration("PRISM_RESOURCE_FETCH_TIMEOUT", 30*time.Second, &errs)
	cfg.ResourceFetchAllowPrivate = envBool("PRISM_RESOURCE_FETCH_ALLOW_PRIVATE", false, &errs)
	cfg.ResourceFetchMaxBytes = envInt("PRISM_RESOURCE_FETCH_MAX_BYTES", 32<<20, &errs)
	cfg.NodeDNSUpstreams = envStringSlice("PRISM_NODE_DNS_UPSTREAMS", DefaultNodeDNSUpstreams(), &errs)
	cfg.ProxyTransportMaxIdleConns = envInt("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS", 1024, &errs)
	cfg.ProxyTransportMaxIdleConnsPerHost = envInt("PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST", 64, &errs)
	cfg.ProxyTransportIdleConnTimeout = envDuration("PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT", 90*time.Second, &errs)
	cfg.ProxyTransportResponseHeaderTimeout = envDuration(
		"PRISM_PROXY_TRANSPORT_RESPONSE_HEADER_TIMEOUT",
		60*time.Second,
		&errs,
	)
	cfg.ProxyTransportTLSHandshakeTimeout = envDuration(
		"PRISM_PROXY_TRANSPORT_TLS_HANDSHAKE_TIMEOUT",
		10*time.Second,
		&errs,
	)
	cfg.ProxyTransportDialTimeout = envDuration("PRISM_PROXY_TRANSPORT_DIAL_TIMEOUT", 30*time.Second, &errs)
	cfg.ProxyBypassRules = envDelimitedStringSlice("PRISM_PROXY_BYPASS", []string{})
	cfg.TrustedProxies = envDelimitedStringSlice("PRISM_TRUSTED_PROXIES", []string{})
	cfg.DirectDenyPrivate = envBool("PRISM_DIRECT_DENY_PRIVATE", true, &errs)
	// PRISM_BLOCKED_DESTINATION_PORTS is read through the raw environment on
	// purpose: it is a Prism-only setting, so there is no upstream RESIN_
	// spelling to fall back to, and "set but empty" must stay distinguishable
	// from "not set" (unset gets the shipped SMTP denylist, empty means
	// "block no port").
	rawBlockedPorts, blockedPortsSet := os.LookupEnv(blockedDestinationPortsEnv)
	if blockedPortsSet {
		recordEnvOrigin(blockedDestinationPortsEnv, envSourceForValue(rawBlockedPorts, false))
	} else {
		recordEnvOrigin(blockedDestinationPortsEnv, EnvSourceDefault)
		rawBlockedPorts = defaultBlockedDestinationPortsRaw
	}
	blockedPorts, blockedPortsErr := parseBlockedDestinationPorts(rawBlockedPorts)
	if blockedPortsErr != nil {
		errs = append(errs, blockedPortsErr.Error())
	}
	cfg.BlockedDestinationPorts = blockedPorts
	cfg.DenyPrivateNodes = envBool("PRISM_DENY_PRIVATE_NODES", true, &errs)
	cfg.ProxyAuthFailLimit = envInt("PRISM_PROXY_AUTH_FAIL_LIMIT", 30, &errs)
	cfg.RequireAdminTOTP = envBool("PRISM_REQUIRE_ADMIN_TOTP", false, &errs)
	// --- Registration ---
	// PRISM_REGISTRATION decides whether the console offers a sign-up form and
	// what it accepts. Anything unrecognised is refused rather than treated as
	// a mode the operator did not ask for.
	registrationRaw := strings.TrimSpace(envStr("PRISM_REGISTRATION", string(RegistrationOff)))
	if registrationRaw == "" {
		// Set but empty means "not configured", the same way an empty
		// PRISM_EGRESS_TRACE_URL means "keep the shipped target". Treating it
		// as an unknown mode would refuse to start over a blank line in a
		// .env file.
		registrationRaw = string(RegistrationOff)
	}
	cfg.Registration = RegistrationMode(registrationRaw)
	if !cfg.Registration.Valid() {
		errs = append(errs, fmt.Sprintf(
			"PRISM_REGISTRATION: invalid value %q (allowed: %s, %s, %s)",
			registrationRaw, RegistrationOff, RegistrationInvite, RegistrationOpen,
		))
		cfg.Registration = RegistrationOff
	}
	// --- USDT deposits ---
	// The address is the operator's own wallet and the server never signs
	// anything, so an empty value is a supported state: deposits stay off and
	// everything else keeps working.
	cfg.USDTAddress = strings.TrimSpace(envStr("PRISM_USDT_ADDRESS", ""))
	cfg.USDTContract = strings.TrimSpace(envStr("PRISM_USDT_CONTRACT", DefaultUSDTContract))
	if cfg.USDTContract == "" {
		cfg.USDTContract = DefaultUSDTContract
	}
	cfg.TronGridAPIKey = strings.TrimSpace(envStr("PRISM_TRONGRID_API_KEY", ""))
	cfg.TronGridEndpoint = strings.TrimSpace(envStr("PRISM_TRONGRID_ENDPOINT", DefaultTronGridEndpoint))
	if cfg.TronGridEndpoint == "" {
		cfg.TronGridEndpoint = DefaultTronGridEndpoint
	}
	// --- Usage alerts ---
	// The three PRISM_ALERT_* variables seed the alert settings at startup; the
	// same three fields stay hot-updatable through the settings page, which is
	// why they also exist on RuntimeConfig.
	cfg.AlertUsageRatio = envFloat("PRISM_ALERT_USAGE_RATIO", DefaultAlertUsageRatio, &errs)
	cfg.AlertWindowMinutes = envInt("PRISM_ALERT_WINDOW_MINUTES", DefaultAlertWindowMinutes, &errs)
	cfg.AlertRetentionDays = envInt("PRISM_ALERT_RETENTION_DAYS", DefaultAlertRetentionDays, &errs)

	// --- Request log ---
	cfg.RequestLogQueueSize = envInt("PRISM_REQUEST_LOG_QUEUE_SIZE", 8192, &errs)
	cfg.RequestLogQueueFlushBatchSize = envInt("PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE", 4096, &errs)
	cfg.RequestLogQueueFlushInterval = envDuration("PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL", 5*time.Minute, &errs)
	cfg.RequestLogDBMaxMB = envInt("PRISM_REQUEST_LOG_DB_MAX_MB", 512, &errs)
	cfg.RequestLogDBRetainCount = envInt("PRISM_REQUEST_LOG_DB_RETAIN_COUNT", 2, &errs)

	// --- Auth ---
	authVersionRaw, _ := lookupEnv("PRISM_AUTH_VERSION")
	proxyToken, hasProxyToken := lookupEnv("PRISM_PROXY_TOKEN")
	cfg.AuthVersion = AuthVersionV1
	if strings.TrimSpace(authVersionRaw) != "" {
		cfg.AuthVersion = NormalizeAuthVersion(authVersionRaw)
	}
	cfg.ProxyToken = proxyToken

	// PRISM_ENFORCE_STRONG_TOKENS=false restores the upstream Resin behaviour,
	// where weak proxy tokens are only reported through /api/v1/system/config/env.
	enforceStrongTokens := envBool("PRISM_ENFORCE_STRONG_TOKENS", true, &errs)
	allowEmptyProxyToken := envBool("PRISM_ALLOW_EMPTY_PROXY_TOKEN", false, &errs)
	allowInsecureListen := envBool("PRISM_ALLOW_INSECURE_LISTEN", false, &errs)
	// Kept on the config so `prism check-config` reports the policy the process
	// is actually running with; nothing else reads them back.
	cfg.EnforceStrongTokens = enforceStrongTokens
	cfg.AllowEmptyProxyToken = allowEmptyProxyToken
	cfg.AllowInsecureListen = allowInsecureListen
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

	proxyTokenEmpty := !hasProxyToken || strings.TrimSpace(cfg.ProxyToken) == ""

	// PRISM-DEVIATION: X1 — weak proxy tokens are rejected by default; setting
	// PRISM_ENFORCE_STRONG_TOKENS=false restores the upstream Resin behaviour
	// where they are only flagged in /api/v1/system/config/env.
	if proxyTokenEmpty {
		if !allowEmptyProxyToken {
			errs = append(errs, "PRISM_PROXY_TOKEN must be defined and non-empty; run prism init to generate a private proxy token, or set PRISM_ALLOW_EMPTY_PROXY_TOKEN=true to disable proxy authentication")
		}
	} else {
		if enforceStrongTokens && len(cfg.ProxyToken) < 16 {
			errs = append(errs, "PRISM_PROXY_TOKEN must be at least 16 characters")
		}
		if err := ValidateProxyTokenForV1(cfg.ProxyToken); err != nil {
			errs = append(errs, fmt.Sprintf("PRISM_PROXY_TOKEN: %v", err))
		}
		if cfg.ProxyToken == "api" || cfg.ProxyToken == "healthz" || cfg.ProxyToken == "ui" {
			errs = append(errs, "PRISM_PROXY_TOKEN must not be reserved keyword: api, healthz, ui")
		}
	}

	if proxyTokenEmpty && !allowInsecureListen && !isLoopbackListenAddress(cfg.ListenAddress) {
		errs = append(errs, "PRISM_LISTEN_ADDRESS must be a loopback address when the proxy token is empty; set PRISM_ALLOW_INSECURE_LISTEN=true to override")
	}
	if proxyTokenEmpty && !allowInsecureListen &&
		cfg.AdminListen != "" && !isLoopbackListenAddress(cfg.AdminListen) {
		errs = append(errs, fmt.Sprintf(
			"PRISM_ADMIN_LISTEN (%s) must be a loopback address when the proxy token is empty; set PRISM_ALLOW_INSECURE_LISTEN=true to override",
			cfg.AdminListen,
		))
	}
	if cfg.ListenAddress == "" {
		errs = append(errs, "PRISM_LISTEN_ADDRESS must not be empty")
	}
	validateOptionalListenAddress("PRISM_ADMIN_LISTEN", cfg.AdminListen, &errs)

	validatePort("PRISM_PORT", cfg.ProxyPort, &errs)
	validatePositive("PRISM_API_MAX_BODY_BYTES", cfg.APIMaxBodyBytes, &errs)
	for i, proxy := range cfg.TrustedProxies {
		if err := validateTrustedProxy(proxy); err != nil {
			errs = append(errs, fmt.Sprintf("PRISM_TRUSTED_PROXIES[%d]: %v", i, err))
		}
	}
	if cfg.ProxyAuthFailLimit < 0 {
		errs = append(errs, "PRISM_PROXY_AUTH_FAIL_LIMIT must not be negative")
	}

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
	if cfg.EgressTraceURL != "" {
		u, err := url.ParseRequestURI(cfg.EgressTraceURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, "PRISM_EGRESS_TRACE_URL: must be an http/https absolute URL")
		}
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
	// A zero or negative deadline here would silently restore the "no timeout"
	// behaviour these settings exist to remove.
	if cfg.ProxyTransportResponseHeaderTimeout <= 0 {
		errs = append(errs, "PRISM_PROXY_TRANSPORT_RESPONSE_HEADER_TIMEOUT must be positive")
	}
	if cfg.ProxyTransportTLSHandshakeTimeout <= 0 {
		errs = append(errs, "PRISM_PROXY_TRANSPORT_TLS_HANDSHAKE_TIMEOUT must be positive")
	}
	if cfg.ProxyTransportDialTimeout <= 0 {
		errs = append(errs, "PRISM_PROXY_TRANSPORT_DIAL_TIMEOUT must be positive")
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

	// Queue size must be at least 2x the batch size.
	//
	// Written as a division rather than `size < 2*batch`: the multiplication
	// overflows to a negative number for a batch size at or above 2^62, which
	// made the comparison true for every size and let the invalid pair through.
	// Both values are validated positive above, so `batch > size/2` is exactly
	// `2*batch > size` with integer truncation, and cannot overflow.
	if cfg.RequestLogQueueFlushBatchSize > cfg.RequestLogQueueSize/2 {
		errs = append(errs, "PRISM_REQUEST_LOG_QUEUE_SIZE must be at least 2x PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE")
	}
	// The usage-alert settings are read by the alert scanner, which cannot
	// answer for a ratio that reports nothing or a window that measures
	// nothing: an out-of-range value refuses the whole configuration and names
	// the variable it came from.
	if cfg.AlertUsageRatio <= 0 || cfg.AlertUsageRatio > 1 {
		errs = append(errs, fmt.Sprintf(
			"PRISM_ALERT_USAGE_RATIO: must be greater than 0 and at most 1, got %g",
			cfg.AlertUsageRatio,
		))
	}
	validatePositive("PRISM_ALERT_WINDOW_MINUTES", cfg.AlertWindowMinutes, &errs)
	validatePositive("PRISM_ALERT_RETENTION_DAYS", cfg.AlertRetentionDays, &errs)

	if len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}

	return cfg, nil
}

// --- helpers ---

// --- environment compatibility (deviation X3) ---

const (
	prismEnvPrefix = "PRISM_"
	resinEnvPrefix = "RESIN_"
)

// USDT on TRON. The contract is fixed because Tether has deployed one TRC20
// USDT contract and everything here is denominated in it; the endpoint is
// TronGrid, which returns already-decoded Transfer events, so no log parsing is
// involved.
const (
	DefaultUSDTContract     = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	DefaultTronGridEndpoint = "https://api.trongrid.io"
)

// legacyEnvWarned records the RESIN_<X> variables that already produced a
// deprecation warning, so each of them warns exactly once per process.
var (
	legacyEnvWarnMu     sync.Mutex
	legacyEnvWarned     = map[string]bool{}
	legacyEnvWarnLogger = log.Printf
)

// lookupEnv resolves name by checking PRISM_<X> first and falling back to the
// upstream Resin spelling RESIN_<X>. A RESIN_ hit is used as-is and logged once
// per variable, because operators still have to rename it:
//
//	config: RESIN_PORT is deprecated, use PRISM_PORT
//
// PRISM_QUALITY_* deliberately has no fallback: those variables only exist in
// Prism.
//
// Every read is recorded (see origins.go), which is what `prism check-config`
// prints next to the effective value: the report is built from the same
// resolution the value came from, not from a second reading of the
// environment.
func lookupEnv(name string) (string, bool) {
	if value, ok := os.LookupEnv(name); ok {
		recordEnvOrigin(name, envSourceForValue(value, false))
		return value, true
	}
	legacy := legacyEnvName(name)
	if legacy == "" {
		recordEnvOrigin(name, EnvSourceDefault)
		return "", false
	}
	value, ok := os.LookupEnv(legacy)
	if !ok {
		recordEnvOrigin(name, EnvSourceDefault)
		return "", false
	}
	warnLegacyEnv(legacy, name)
	recordEnvOrigin(name, envSourceForValue(value, true))
	return value, true
}

// legacyEnvName maps a PRISM_<X> variable onto its RESIN_<X> predecessor.
func legacyEnvName(name string) string {
	if !strings.HasPrefix(name, prismEnvPrefix) {
		return ""
	}
	return resinEnvPrefix + strings.TrimPrefix(name, prismEnvPrefix)
}

// warnLegacyEnv emits the deprecation warning for a RESIN_ variable at most
// once.
func warnLegacyEnv(legacy, name string) {
	legacyEnvWarnMu.Lock()
	alreadyWarned := legacyEnvWarned[legacy]
	if !alreadyWarned {
		legacyEnvWarned[legacy] = true
	}
	logf := legacyEnvWarnLogger
	legacyEnvWarnMu.Unlock()
	if alreadyWarned {
		return
	}
	if logf == nil {
		logf = log.Printf
	}
	logf("config: %s is deprecated, use %s", legacy, name)
}
func envBool(key string, defaultVal bool, errs *[]string) bool {
	raw, set := os.LookupEnv(key)
	v := strings.TrimSpace(raw)
	// The switch is Prism-only, so it is read straight from the environment;
	// the origin is still recorded so check-config can report it like any
	// other setting. Unset is the shipped default and "set but empty" is a
	// line that carries nothing - two different answers to "why does this
	// deployment behave this way" - so the record distinguishes them the way
	// lookupEnv does.
	if set {
		recordEnvOrigin(key, envSourceForValue(v, false))
	} else {
		recordEnvOrigin(key, EnvSourceDefault)
	}
	if v == "" {
		return defaultVal
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		*errs = append(*errs, fmt.Sprintf("%s: invalid boolean %q", key, v))
		return defaultVal
	}
}

// envStr reads a string setting. envBool stays out of the compatibility layer
// on purpose: every PRISM_* boolean switch is Prism-only.
func envStr(key, defaultVal string) string {
	if v, ok := lookupEnv(key); ok {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int, errs *[]string) int {
	v, ok := lookupEnv(key)
	if !ok || v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid integer %q", key, v))
		return defaultVal
	}
	return n
}

// envFloat reads a floating point setting. Like the integer and duration
// helpers it answers the default for an unset or empty variable, and a
// malformed value is reported by the name of the variable.
func envFloat(key string, defaultVal float64, errs *[]string) float64 {
	v, ok := lookupEnv(key)
	if !ok || v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid number %q", key, v))
		return defaultVal
	}
	return f
}

func envDuration(key string, defaultVal time.Duration, errs *[]string) time.Duration {
	v, ok := lookupEnv(key)
	if !ok || v == "" {
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
	v, ok := lookupEnv(key)
	if !ok || v == "" {
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
	v, ok := lookupEnv(key)
	if !ok || v == "" {
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

// blockedDestinationPortsEnv is the environment variable that carries the
// destination port denylist of every local direct dial.
const blockedDestinationPortsEnv = "PRISM_BLOCKED_DESTINATION_PORTS"

// defaultBlockedDestinationPortsRaw is the shipped value of
// PRISM_BLOCKED_DESTINATION_PORTS: the SMTP ports an open deployment must not
// relay through. It is kept as the raw spelling so "unset" and "set to empty"
// stay distinguishable.
const defaultBlockedDestinationPortsRaw = "25,465,587"

// parseBlockedDestinationPorts parses the comma-separated port list of
// PRISM_BLOCKED_DESTINATION_PORTS. An empty value blocks no port; every other
// token must parse as an integer inside 1..65535, and a bad token refuses the
// whole setting with the token itself named. The result is deduplicated and
// sorted, so the effective configuration does not depend on the order the
// operator wrote it in.
func parseBlockedDestinationPorts(raw string) ([]int, error) {
	ports := make([]int, 0, 3)
	seen := make(map[int]bool)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		port, err := strconv.Atoi(token)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf(
				"%s: invalid port %q (must be an integer between 1 and 65535)",
				blockedDestinationPortsEnv, token,
			)
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

// cleanDirPath normalises a directory path. Absolute paths are allowed (Docker
// deployments use paths such as /var/lib/prism); only a path segment that is
// exactly ".." is rejected.
func cleanDirPath(name, path string, errs *[]string) string {
	if path == "" {
		return path
	}
	if containsDotDotSegment(path) {
		*errs = append(*errs, fmt.Sprintf("%s: must not contain '..' segments", name))
		return path
	}
	return filepath.Clean(path)
}

// containsDotDotSegment reports whether any path segment is exactly "..".
func containsDotDotSegment(path string) bool {
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// validateTrustedProxy accepts an IP literal or CIDR list entry.
//
// A zero-length prefix is refused: 0.0.0.0/0 and ::/0 name every source address
// there is, so listing one turns a deployment that looks configured into one that
// trusts anything a client writes in X-Forwarded-For or X-Forwarded-Proto - the
// failure limiter would key on a header the client chooses and be bypassed by
// rotating it.
func validateTrustedProxy(value string) error {
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return fmt.Errorf("must be an IP address or CIDR, got %q", value)
		}
		if prefix.Bits() == 0 {
			return fmt.Errorf("must not trust every source address: %q covers all of them", value)
		}
		return nil
	}
	if _, err := netip.ParseAddr(strings.Trim(value, "[]")); err != nil {
		return fmt.Errorf("must be an IP address or CIDR, got %q", value)
	}
	return nil
}

func validatePort(name string, value int, errs *[]string) {
	if value < 1 || value > 65535 {
		*errs = append(*errs, fmt.Sprintf("%s: port must be 1-65535, got %d", name, value))
	}
}

// validateOptionalListenAddress validates an optional "host:port" listener
// address. An empty value is valid and means the listener is disabled.
func validateOptionalListenAddress(name, value string, errs *[]string) {
	if value == "" {
		return
	}
	host, portRaw, err := net.SplitHostPort(value)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: must be host:port, got %q", name, value))
		return
	}
	if strings.TrimSpace(host) == "" {
		*errs = append(*errs, fmt.Sprintf("%s: host must not be empty, got %q", name, value))
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: port must be numeric, got %q", name, portRaw))
		return
	}
	validatePort(name, port, errs)
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
	// PRISM-DEVIATION: X1 — the upstream V1 validator only rejects forbidden
	// characters. The 16-character minimum is a separate Prism policy enforced
	// while PRISM_ENFORCE_STRONG_TOKENS is true.
	if strings.ContainsAny(token, v1ProxyTokenForbiddenChars) {
		return fmt.Errorf("must not contain any of %q", v1ProxyTokenForbiddenChars)
	}
	if strings.ContainsAny(token, v1ProxyTokenForbiddenSpacing) {
		return fmt.Errorf("must not contain spaces, tabs, newlines, or carriage returns")
	}
	return nil
}

// isLoopbackListenAddress reports whether addr binds to the loopback interface.
func isLoopbackListenAddress(addr string) bool {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return false
	}
	host := trimmed
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// RegistrationMode says who may create an account for themselves.
//
// The zero value is "off" rather than "open": an installation that has not
// thought about self-service sign-up is not open to it, and a config row read
// back from the environment is not allowed to change that by omission.
type RegistrationMode string

const (
	// RegistrationOff refuses every self-service sign-up. Accounts are created
	// by an administrator.
	RegistrationOff RegistrationMode = "off"
	// RegistrationInvite accepts a sign-up that presents a valid invite code,
	// and opens the subscription the code carries.
	RegistrationInvite RegistrationMode = "invite"
	// RegistrationOpen accepts any sign-up. No plan is attached, so the account
	// reaches nothing until an administrator grants one.
	RegistrationOpen RegistrationMode = "open"
)

// Valid reports whether the mode is one this build understands.
func (m RegistrationMode) Valid() bool {
	switch m {
	case RegistrationOff, RegistrationInvite, RegistrationOpen:
		return true
	default:
		return false
	}
}

// AllowsRegistration reports whether the mode admits a sign-up at all.
func (m RegistrationMode) AllowsRegistration() bool {
	return m == RegistrationInvite || m == RegistrationOpen
}
