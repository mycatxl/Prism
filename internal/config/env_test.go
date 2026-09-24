package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// setEnvs sets multiple env vars and returns a cleanup function.
func setEnvs(t *testing.T, envs map[string]string) {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
}

// requiredEnvs returns the minimum env vars needed for LoadEnvConfig to succeed.
//
// PRISM-DEVIATION: X1 Prism requires tokens of at least 16 characters by
// default, so the fixture uses long tokens; upstream's short literals
// ("upstream-admin-token"/"upstream-proxy-token") are rejected before any other validation.
func requiredEnvs() map[string]string {
	return map[string]string{
		"PRISM_AUTH_VERSION": "V1",
		"PRISM_ADMIN_TOKEN":  "upstream-admin-token",
		"PRISM_PROXY_TOKEN":  "upstream-proxy-token",
	}
}

func TestLoadEnvConfig_Defaults(t *testing.T) {
	setEnvs(t, requiredEnvs())

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Directories. The configured defaults are "./.local/<x>"; LoadEnvConfig
	// normalises them with filepath.Clean, so the leading "./" is dropped.
	assertEqual(t, "CacheDir", cfg.CacheDir, ".local/cache")
	assertEqual(t, "StateDir", cfg.StateDir, ".local/state")
	assertEqual(t, "LogDir", cfg.LogDir, ".local/logs")
	assertEqual(t, "ListenAddress", cfg.ListenAddress, "127.0.0.1")

	// Ports
	assertEqual(t, "ProxyPort", cfg.ProxyPort, 2260)
	assertEqual(t, "APIMaxBodyBytes", cfg.APIMaxBodyBytes, 1<<20)

	// Core
	assertEqual(t, "MaxLatencyTableEntries", cfg.MaxLatencyTableEntries, 12)
	assertEqual(t, "ProbeConcurrency", cfg.ProbeConcurrency, 32)
	assertEqual(t, "GeoIPUpdateSchedule", cfg.GeoIPUpdateSchedule, "0 7 * * *")
	assertEqual(t, "DefaultPlatformStickyTTL", cfg.DefaultPlatformStickyTTL, 7*24*time.Hour)
	assertEqual(t, "DefaultPlatformRegexFiltersLength", len(cfg.DefaultPlatformRegexFilters), 0)
	assertEqual(t, "DefaultPlatformRegionFiltersLength", len(cfg.DefaultPlatformRegionFilters), 0)
	assertEqual(t, "DefaultPlatformReverseProxyMissAction", cfg.DefaultPlatformReverseProxyMissAction, "TREAT_AS_EMPTY")
	assertEqual(
		t,
		"DefaultPlatformReverseProxyEmptyAccountBehavior",
		cfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		"ACCOUNT_HEADER_RULE",
	)
	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"Authorization",
	)
	assertEqual(t, "DefaultPlatformAllocationPolicy", cfg.DefaultPlatformAllocationPolicy, "BALANCED")
	assertEqual(t, "ProbeTimeout", cfg.ProbeTimeout, 15*time.Second)
	assertEqual(t, "ResourceFetchTimeout", cfg.ResourceFetchTimeout, 30*time.Second)
	assertEqual(t, "ResourceFetchMaxBytes", cfg.ResourceFetchMaxBytes, 32<<20)
	assertEqual(t, "NodeDNSUpstreamsLength", len(cfg.NodeDNSUpstreams), 4)
	assertEqual(t, "NodeDNSUpstreams[0]", cfg.NodeDNSUpstreams[0], "https://doh.pub/dns-query")
	assertEqual(t, "NodeDNSUpstreams[1]", cfg.NodeDNSUpstreams[1], "https://dns.alidns.com/dns-query")
	assertEqual(t, "NodeDNSUpstreams[2]", cfg.NodeDNSUpstreams[2], "tls://223.5.5.5?sni=dns.alidns.com")
	assertEqual(t, "NodeDNSUpstreams[3]", cfg.NodeDNSUpstreams[3], "local")
	assertEqual(t, "ProxyTransportMaxIdleConns", cfg.ProxyTransportMaxIdleConns, 1024)
	assertEqual(t, "ProxyTransportMaxIdleConnsPerHost", cfg.ProxyTransportMaxIdleConnsPerHost, 64)
	assertEqual(t, "ProxyTransportIdleConnTimeout", cfg.ProxyTransportIdleConnTimeout, 90*time.Second)
	assertEqual(t, "ProxyBypassRulesLength", len(cfg.ProxyBypassRules), 0)

	// Request log
	assertEqual(t, "RequestLogQueueSize", cfg.RequestLogQueueSize, 8192)
	assertEqual(t, "RequestLogQueueFlushBatchSize", cfg.RequestLogQueueFlushBatchSize, 4096)
	assertEqual(t, "RequestLogDBMaxMB", cfg.RequestLogDBMaxMB, 512)
	assertEqual(t, "RequestLogDBRetainCount", cfg.RequestLogDBRetainCount, 2)

	// Auth
	assertEqual(t, "AuthVersion", cfg.AuthVersion, AuthVersionV1)

	// Metrics
	assertEqual(t, "MetricThroughputIntervalSeconds", cfg.MetricThroughputIntervalSeconds, 2)
	assertEqual(t, "MetricThroughputRetentionSeconds", cfg.MetricThroughputRetentionSeconds, 3600)
	assertEqual(t, "MetricBucketSeconds", cfg.MetricBucketSeconds, 3600)
	assertEqual(t, "MetricConnectionsIntervalSeconds", cfg.MetricConnectionsIntervalSeconds, 15)
	assertEqual(t, "MetricConnectionsRetentionSeconds", cfg.MetricConnectionsRetentionSeconds, 18000)
	assertEqual(t, "MetricLeasesIntervalSeconds", cfg.MetricLeasesIntervalSeconds, 5)
	assertEqual(t, "MetricLeasesRetentionSeconds", cfg.MetricLeasesRetentionSeconds, 18000)
	assertEqual(t, "MetricLatencyBinWidthMS", cfg.MetricLatencyBinWidthMS, 100)
	assertEqual(t, "MetricLatencyBinOverflowMS", cfg.MetricLatencyBinOverflowMS, 3000)
}

func TestLoadEnvConfig_EnvOverrides(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_CACHE_DIR"] = "/tmp/cache"
	envs["PRISM_LISTEN_ADDRESS"] = "127.0.0.1"
	envs["PRISM_PORT"] = "8080"
	envs["PRISM_API_MAX_BODY_BYTES"] = "2097152"
	envs["PRISM_PROBE_CONCURRENCY"] = "500"
	envs["PRISM_GEOIP_UPDATE_SCHEDULE"] = "0 0 * * *"
	envs["PRISM_DEFAULT_PLATFORM_STICKY_TTL"] = "2h"
	envs["PRISM_DEFAULT_PLATFORM_REGEX_FILTERS"] = `["^Provider/.*"]`
	envs["PRISM_DEFAULT_PLATFORM_REGION_FILTERS"] = `["us","hk"]`
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION"] = "REJECT"
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = "x-platform-account"
	envs["PRISM_DEFAULT_PLATFORM_ALLOCATION_POLICY"] = "PREFER_LOW_LATENCY"
	envs["PRISM_PROBE_TIMEOUT"] = "20s"
	envs["PRISM_RESOURCE_FETCH_TIMEOUT"] = "45s"
	envs["PRISM_NODE_DNS_UPSTREAMS"] = `["udp://10.0.0.53","local"]`
	envs["PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS"] = "2048"
	envs["PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST"] = "128"
	envs["PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT"] = "2m"
	envs["PRISM_PROXY_BYPASS"] = "localhost;127.*; 192.168.*\n<local>,10.0.0.0/8"
	envs["PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL"] = "10m"
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, "CacheDir", cfg.CacheDir, "/tmp/cache")
	assertEqual(t, "ListenAddress", cfg.ListenAddress, "127.0.0.1")
	assertEqual(t, "ProxyPort", cfg.ProxyPort, 8080)
	assertEqual(t, "APIMaxBodyBytes", cfg.APIMaxBodyBytes, 2097152)
	assertEqual(t, "ProbeConcurrency", cfg.ProbeConcurrency, 500)
	assertEqual(t, "GeoIPUpdateSchedule", cfg.GeoIPUpdateSchedule, "0 0 * * *")
	assertEqual(t, "DefaultPlatformStickyTTL", cfg.DefaultPlatformStickyTTL, 2*time.Hour)
	assertEqual(t, "DefaultPlatformRegexFiltersLength", len(cfg.DefaultPlatformRegexFilters), 1)
	assertEqual(t, "DefaultPlatformRegexFilters[0]", cfg.DefaultPlatformRegexFilters[0], "^Provider/.*")
	assertEqual(t, "DefaultPlatformRegionFiltersLength", len(cfg.DefaultPlatformRegionFilters), 2)
	assertEqual(t, "DefaultPlatformRegionFilters[0]", cfg.DefaultPlatformRegionFilters[0], "us")
	assertEqual(t, "DefaultPlatformRegionFilters[1]", cfg.DefaultPlatformRegionFilters[1], "hk")
	assertEqual(t, "DefaultPlatformReverseProxyMissAction", cfg.DefaultPlatformReverseProxyMissAction, "REJECT")
	assertEqual(
		t,
		"DefaultPlatformReverseProxyEmptyAccountBehavior",
		cfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		"FIXED_HEADER",
	)
	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"X-Platform-Account",
	)
	assertEqual(t, "DefaultPlatformAllocationPolicy", cfg.DefaultPlatformAllocationPolicy, "PREFER_LOW_LATENCY")
	assertEqual(t, "ProbeTimeout", cfg.ProbeTimeout, 20*time.Second)
	assertEqual(t, "ResourceFetchTimeout", cfg.ResourceFetchTimeout, 45*time.Second)
	assertEqual(t, "NodeDNSUpstreamsLength", len(cfg.NodeDNSUpstreams), 2)
	assertEqual(t, "NodeDNSUpstreams[0]", cfg.NodeDNSUpstreams[0], "udp://10.0.0.53")
	assertEqual(t, "NodeDNSUpstreams[1]", cfg.NodeDNSUpstreams[1], "local")
	assertEqual(t, "ProxyTransportMaxIdleConns", cfg.ProxyTransportMaxIdleConns, 2048)
	assertEqual(t, "ProxyTransportMaxIdleConnsPerHost", cfg.ProxyTransportMaxIdleConnsPerHost, 128)
	assertEqual(t, "ProxyTransportIdleConnTimeout", cfg.ProxyTransportIdleConnTimeout, 2*time.Minute)
	assertEqual(t, "ProxyBypassRulesLength", len(cfg.ProxyBypassRules), 5)
	assertEqual(t, "ProxyBypassRules[0]", cfg.ProxyBypassRules[0], "localhost")
	assertEqual(t, "ProxyBypassRules[1]", cfg.ProxyBypassRules[1], "127.*")
	assertEqual(t, "ProxyBypassRules[2]", cfg.ProxyBypassRules[2], "192.168.*")
	assertEqual(t, "ProxyBypassRules[3]", cfg.ProxyBypassRules[3], "<local>")
	assertEqual(t, "ProxyBypassRules[4]", cfg.ProxyBypassRules[4], "10.0.0.0/8")
	if cfg.RequestLogQueueFlushInterval.String() != "10m0s" {
		t.Errorf("RequestLogQueueFlushInterval: got %v, want 10m", cfg.RequestLogQueueFlushInterval)
	}
}

func TestLoadEnvConfig_DefaultPlatformFixedHeaderMultiline(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = " authorization \nx-account-id\nX-Account-Id "
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"Authorization\nX-Account-Id",
	)
}

func TestLoadEnvConfig_DefaultPlatformRegionFilters_Negation(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REGION_FILTERS"] = `["!hk","us"]`
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, "DefaultPlatformRegionFiltersLength", len(cfg.DefaultPlatformRegionFilters), 2)
	assertEqual(t, "DefaultPlatformRegionFilters[0]", cfg.DefaultPlatformRegionFilters[0], "!hk")
	assertEqual(t, "DefaultPlatformRegionFilters[1]", cfg.DefaultPlatformRegionFilters[1], "us")
}

func TestLoadEnvConfig_NodeDNSUpstreamsRejectsEmptyList(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_NODE_DNS_UPSTREAMS"] = `[]`
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for empty PRISM_NODE_DNS_UPSTREAMS")
	}
	assertContains(t, err.Error(), "PRISM_NODE_DNS_UPSTREAMS must contain at least one DNS upstream when defined")
}

func TestLoadEnvConfig_NodeDNSUpstreamsRejectsObjectFormat(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_NODE_DNS_UPSTREAMS"] = `[{"type":"udp","server":"10.0.0.53"}]`
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for object-style PRISM_NODE_DNS_UPSTREAMS")
	}
	assertContains(t, err.Error(), "PRISM_NODE_DNS_UPSTREAMS: invalid JSON string array")
}

func TestLoadEnvConfig_MissingAdminToken(t *testing.T) {
	t.Setenv("PRISM_AUTH_VERSION", "V1")
	t.Setenv("PRISM_PROXY_TOKEN", "upstream-proxy-token")
	// Ensure PRISM_ADMIN_TOKEN is not set
	os.Unsetenv("PRISM_ADMIN_TOKEN")

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for missing PRISM_ADMIN_TOKEN")
	}
	assertContains(t, err.Error(), "PRISM_ADMIN_TOKEN must be defined and non-empty")
}

func TestLoadEnvConfig_MissingProxyToken(t *testing.T) {
	t.Setenv("PRISM_AUTH_VERSION", "V1")
	t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
	os.Unsetenv("PRISM_PROXY_TOKEN")

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for missing PRISM_PROXY_TOKEN")
	}
	assertContains(t, err.Error(), "PRISM_PROXY_TOKEN must be defined and non-empty")
}

func TestLoadEnvConfig_MissingAuthVersion(t *testing.T) {
	t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
	t.Setenv("PRISM_PROXY_TOKEN", "upstream-proxy-token")
	os.Unsetenv("PRISM_AUTH_VERSION")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AuthVersion", cfg.AuthVersion, AuthVersionV1)
}

func TestLoadEnvConfig_EmptyAuthVersionDefaultsToV1(t *testing.T) {
	t.Setenv("PRISM_AUTH_VERSION", "  ")
	t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
	t.Setenv("PRISM_PROXY_TOKEN", "upstream-proxy-token")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AuthVersion", cfg.AuthVersion, AuthVersionV1)
}

func TestLoadEnvConfig_InvalidAuthVersion(t *testing.T) {
	for _, value := range []string{"V2", "LEGACY_V0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PRISM_AUTH_VERSION", value)
			t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
			t.Setenv("PRISM_PROXY_TOKEN", "upstream-proxy-token")

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("expected error for invalid PRISM_AUTH_VERSION")
			}
			assertContains(t, err.Error(), "PRISM_AUTH_VERSION: invalid value")
			assertContains(t, err.Error(), "allowed: V1")
		})
	}
}

// PRISM-DEVIATION: X2 an empty token still disables that authentication scope,
// but only with the explicit opt-in switch and a loopback listen address.
// Upstream Resin accepted the empty tokens on their own.
func TestLoadEnvConfig_EmptyTokensAllowedWhenDefined(t *testing.T) {
	t.Setenv("PRISM_AUTH_VERSION", "V1")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("PRISM_PROXY_TOKEN", "")
	t.Setenv("PRISM_ALLOW_EMPTY_ADMIN_TOKEN", "true")
	t.Setenv("PRISM_ALLOW_EMPTY_PROXY_TOKEN", "true")
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AdminToken", cfg.AdminToken, "")
	assertEqual(t, "ProxyToken", cfg.ProxyToken, "")
}

func TestLoadEnvConfig_EmptyTokensCannotDisableAuthentication(t *testing.T) {
	for _, value := range []string{"", " \t"} {
		t.Run(fmt.Sprintf("empty-%d", len(value)), func(t *testing.T) {
			setEnvs(t, requiredEnvs())
			t.Setenv("PRISM_ADMIN_TOKEN", value)
			t.Setenv("PRISM_PROXY_TOKEN", value)
			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("empty tokens must not start an unauthenticated service")
			}
			assertContains(t, err.Error(), "PRISM_ADMIN_TOKEN must be defined and non-empty")
			assertContains(t, err.Error(), "PRISM_PROXY_TOKEN must be defined and non-empty")
		})
	}
}

func TestLoadEnvConfig_ProxyTokenReservedKeywords(t *testing.T) {
	tests := []string{"api", "healthz", "ui"}
	for _, token := range tests {
		t.Run(token, func(t *testing.T) {
			t.Setenv("PRISM_AUTH_VERSION", "V1")
			t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
			t.Setenv("PRISM_PROXY_TOKEN", token)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("expected error for reserved PRISM_PROXY_TOKEN")
			}
			assertContains(t, err.Error(), "reserved keyword")
		})
	}
}

func TestLoadEnvConfig_ProxyTokenForbiddenChars_V1(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"dot", "bad.token"},
		{"colon", "bad:token"},
		{"pipe", "bad|token"},
		{"slash", "bad/token"},
		{"backslash", "bad\\token"},
		{"at", "bad@token"},
		{"question", "bad?token"},
		{"hash", "bad#token"},
		{"percent", "bad%token"},
		{"tilde", "bad~token"},
		{"space", "bad token"},
		{"tab", "bad\ttoken"},
		{"newline", "bad\ntoken"},
		{"carriage_return", "bad\rtoken"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRISM_AUTH_VERSION", "V1")
			t.Setenv("PRISM_ADMIN_TOKEN", "upstream-admin-token")
			t.Setenv("PRISM_PROXY_TOKEN", tc.token)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("expected error for forbidden char in PRISM_PROXY_TOKEN when V1")
			}
			assertContains(t, err.Error(), "PRISM_PROXY_TOKEN:")
		})
	}
}

func TestLoadEnvConfig_EmptyListenAddress(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_LISTEN_ADDRESS"] = "   "
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for empty listen address")
	}
	assertContains(t, err.Error(), "PRISM_LISTEN_ADDRESS")
}

func TestLoadEnvConfig_InvalidPort(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PORT"] = "99999"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for port out of range")
	}
	assertContains(t, err.Error(), "PRISM_PORT")
}

func TestLoadEnvConfig_InvalidPortNotNumber(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PORT"] = "abc"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for non-numeric port")
	}
	assertContains(t, err.Error(), "PRISM_PORT")
}

func TestLoadEnvConfig_ZeroPort(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PORT"] = "0"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for zero port")
	}
	assertContains(t, err.Error(), "PRISM_PORT")
}

func TestLoadEnvConfig_InvalidAPIMaxBodyBytes(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_API_MAX_BODY_BYTES"] = "0"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for non-positive API max body bytes")
	}
	assertContains(t, err.Error(), "PRISM_API_MAX_BODY_BYTES")
}

func TestLoadEnvConfig_QueueSizeTooSmall(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_REQUEST_LOG_QUEUE_SIZE"] = "100"
	envs["PRISM_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE"] = "100"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for queue size < 2x batch size")
	}
	assertContains(t, err.Error(), "at least 2x")
}

func TestLoadEnvConfig_InvalidDuration(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL"] = "not-a-duration"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
	assertContains(t, err.Error(), "PRISM_REQUEST_LOG_QUEUE_FLUSH_INTERVAL")
}

func TestLoadEnvConfig_NegativeValue(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PROBE_CONCURRENCY"] = "-5"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for negative value")
	}
	assertContains(t, err.Error(), "PRISM_PROBE_CONCURRENCY")
}

func TestLoadEnvConfig_MaxLatencyTableEntriesOutOfRange(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_MAX_LATENCY_TABLE_ENTRIES"] = "33"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for PRISM_MAX_LATENCY_TABLE_ENTRIES > 32")
	}
	assertContains(t, err.Error(), "PRISM_MAX_LATENCY_TABLE_ENTRIES")
	assertContains(t, err.Error(), "<= 32")
}

func TestLoadEnvConfig_ProbeConcurrencyOutOfRange(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PROBE_CONCURRENCY"] = "10001"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for PRISM_PROBE_CONCURRENCY > 10000")
	}
	assertContains(t, err.Error(), "PRISM_PROBE_CONCURRENCY")
	assertContains(t, err.Error(), "<= 10000")
}

func TestLoadEnvConfig_InvalidGeoIPSchedule(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_GEOIP_UPDATE_SCHEDULE"] = "not-a-cron"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid geoip schedule")
	}
	assertContains(t, err.Error(), "PRISM_GEOIP_UPDATE_SCHEDULE")
}

func TestLoadEnvConfig_InvalidDefaultPlatformAllocationPolicy(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_ALLOCATION_POLICY"] = "UNKNOWN"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform allocation policy")
	}
	assertContains(t, err.Error(), "PRISM_DEFAULT_PLATFORM_ALLOCATION_POLICY")
}

func TestLoadEnvConfig_InvalidDefaultPlatformEmptyAccountBehavior(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "UNKNOWN"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform empty-account behavior")
	}
	assertContains(t, err.Error(), "PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR")
}

func TestLoadEnvConfig_FixedHeaderModeRequiresHeader(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = "   "
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error when fixed-header mode has empty header")
	}
	assertContains(t, err.Error(), "PRISM_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER")
}

func TestLoadEnvConfig_InvalidDefaultPlatformRegex(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REGEX_FILTERS"] = `["("]`
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform regex")
	}
	assertContains(t, err.Error(), "PRISM_DEFAULT_PLATFORM_REGEX_FILTERS")
}

func TestLoadEnvConfig_DefaultPlatformRegexRuleSyntax(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_DEFAULT_PLATFORM_REGEX_FILTERS"] = `["hk","*fast","!expired","\\!literal"]`
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"hk", "*fast", "!expired", `\!literal`}
	if !reflect.DeepEqual(cfg.DefaultPlatformRegexFilters, want) {
		t.Fatalf("default platform regex rules: got %v, want %v", cfg.DefaultPlatformRegexFilters, want)
	}
}

func TestLoadEnvConfig_InvalidProbeTimeout(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PROBE_TIMEOUT"] = "0s"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid probe timeout")
	}
	assertContains(t, err.Error(), "PRISM_PROBE_TIMEOUT")
}

func TestLoadEnvConfig_InvalidProxyTransportSettings(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS"] = "16"
	envs["PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST"] = "32"
	envs["PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT"] = "0s"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid proxy transport settings")
	}
	assertContains(t, err.Error(), "PRISM_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT")
	assertContains(t, err.Error(), "PRISM_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST")
}

// --- test helpers ---

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

// --- Prism-specific cases restored from 3633100 (WP05) ---

const (
	testAdminToken = "0123456789abcdef0123456789abcdef"
	testProxyToken = "fedcba9876543210fedcba9876543210"
)

// unsetEnvForConfigTest removes the given variables for the duration of a test
// so that LoadEnvConfig never sees ambient shell state.
func unsetEnvForConfigTest(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		oldValue, hadOldValue := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("Unsetenv(%s): %v", key, err)
		}
		key := key
		t.Cleanup(func() {
			if hadOldValue {
				_ = os.Setenv(key, oldValue)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

// clearTokenEnv removes tokens and PRISM-only switches that would make a test
// configuration invalid regardless of its subject.
func clearTokenEnv(t *testing.T) {
	t.Helper()
	unsetEnvForConfigTest(t,
		"PRISM_ADMIN_TOKEN", "RESIN_ADMIN_TOKEN",
		"PRISM_PROXY_TOKEN", "RESIN_PROXY_TOKEN",
		"PRISM_AUTH_VERSION", "RESIN_AUTH_VERSION",
		"PRISM_ENFORCE_STRONG_TOKENS",
		"PRISM_ALLOW_EMPTY_ADMIN_TOKEN", "PRISM_ALLOW_EMPTY_PROXY_TOKEN",
		"PRISM_ALLOW_INSECURE_LISTEN",
	)
}

func resetLegacyEnvWarnings() {
	legacyEnvWarnMu.Lock()
	legacyEnvWarned = map[string]bool{}
	legacyEnvWarnMu.Unlock()
}

// captureLegacyEnvWarnings installs a logger that records deprecation warnings
// and returns a reader for the collected lines.
func captureLegacyEnvWarnings(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	legacyEnvWarnMu.Lock()
	previous := legacyEnvWarnLogger
	legacyEnvWarnLogger = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	legacyEnvWarnMu.Unlock()
	t.Cleanup(func() {
		legacyEnvWarnMu.Lock()
		legacyEnvWarnLogger = previous
		legacyEnvWarnMu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// --- deviation X3: RESIN_* fallback ---

func TestLookupEnvFallsBackToResinPrefix(t *testing.T) {
	resetLegacyEnvWarnings()
	clearTokenEnv(t)
	unsetEnvForConfigTest(t,
		"PRISM_PORT", "RESIN_PORT",
		"PRISM_API_MAX_BODY_BYTES", "RESIN_API_MAX_BODY_BYTES",
		"PRISM_PROBE_TIMEOUT", "RESIN_PROBE_TIMEOUT",
		"PRISM_DEFAULT_PLATFORM_REGION_FILTERS", "RESIN_DEFAULT_PLATFORM_REGION_FILTERS",
		"PRISM_TRUSTED_PROXIES", "RESIN_TRUSTED_PROXIES",
	)
	t.Setenv("PRISM_ADMIN_TOKEN", testAdminToken)
	t.Setenv("PRISM_PROXY_TOKEN", testProxyToken)

	// Nothing but the Resin spelling is defined: every helper must serve it.
	t.Setenv("RESIN_PORT", "3000")
	t.Setenv("RESIN_API_MAX_BODY_BYTES", "4096")
	t.Setenv("RESIN_PROBE_TIMEOUT", "33s")
	t.Setenv("RESIN_DEFAULT_PLATFORM_REGION_FILTERS", `["us","hk"]`)
	t.Setenv("RESIN_TRUSTED_PROXIES", "10.0.0.0/8,127.0.0.1")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("LoadEnvConfig with RESIN_ variables: %v", err)
	}
	if cfg.ProxyPort != 3000 {
		t.Fatalf("PRISM_PORT from RESIN_PORT: got %d, want 3000", cfg.ProxyPort)
	}
	if cfg.APIMaxBodyBytes != 4096 {
		t.Fatalf("PRISM_API_MAX_BODY_BYTES from RESIN_API_MAX_BODY_BYTES: got %d, want 4096", cfg.APIMaxBodyBytes)
	}
	if cfg.ProbeTimeout != 33*time.Second {
		t.Fatalf("PRISM_PROBE_TIMEOUT from RESIN_PROBE_TIMEOUT: got %s, want 33s", cfg.ProbeTimeout)
	}
	if !reflect.DeepEqual(cfg.DefaultPlatformRegionFilters, []string{"us", "hk"}) {
		t.Fatalf(
			"PRISM_DEFAULT_PLATFORM_REGION_FILTERS from RESIN_ spelling: got %v",
			cfg.DefaultPlatformRegionFilters,
		)
	}
	if !reflect.DeepEqual(cfg.TrustedProxies, []string{"10.0.0.0/8", "127.0.0.1"}) {
		t.Fatalf("PRISM_TRUSTED_PROXIES from RESIN_TRUSTED_PROXIES: got %v", cfg.TrustedProxies)
	}

	// The token reads use the same fallback.
	unsetEnvForConfigTest(t, "PRISM_ADMIN_TOKEN", "PRISM_PROXY_TOKEN")
	t.Setenv("RESIN_ADMIN_TOKEN", testAdminToken)
	t.Setenv("RESIN_PROXY_TOKEN", testProxyToken)
	cfg, err = LoadEnvConfig()
	if err != nil {
		t.Fatalf("LoadEnvConfig with RESIN_ tokens: %v", err)
	}
	if cfg.AdminToken != testAdminToken || cfg.ProxyToken != testProxyToken {
		t.Fatal("RESIN_ADMIN_TOKEN / RESIN_PROXY_TOKEN must be served through the fallback")
	}
}

func TestLookupEnvPrefersPrismPrefix(t *testing.T) {
	resetLegacyEnvWarnings()
	clearTokenEnv(t)
	unsetEnvForConfigTest(t, "PRISM_PORT", "RESIN_PORT")
	t.Setenv("PRISM_ADMIN_TOKEN", testAdminToken)
	t.Setenv("PRISM_PROXY_TOKEN", testProxyToken)

	t.Setenv("RESIN_PORT", "3000")
	t.Setenv("PRISM_PORT", "2500")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("LoadEnvConfig: %v", err)
	}
	if cfg.ProxyPort != 2500 {
		t.Fatalf("PRISM_PORT must win over RESIN_PORT: got %d, want 2500", cfg.ProxyPort)
	}
}

func TestLookupEnvWarnsOncePerVariable(t *testing.T) {
	resetLegacyEnvWarnings()
	warnings := captureLegacyEnvWarnings(t)

	unsetEnvForConfigTest(t, "PRISM_PORT", "RESIN_PORT", "PRISM_API_MAX_BODY_BYTES", "RESIN_API_MAX_BODY_BYTES")
	t.Setenv("RESIN_PORT", "3000")
	t.Setenv("RESIN_API_MAX_BODY_BYTES", "4096")

	for i := 0; i < 3; i++ {
		if _, ok := lookupEnv("PRISM_PORT"); !ok {
			t.Fatal("lookupEnv must serve RESIN_PORT")
		}
	}
	if _, ok := lookupEnv("PRISM_API_MAX_BODY_BYTES"); !ok {
		t.Fatal("lookupEnv must serve RESIN_API_MAX_BODY_BYTES")
	}

	want := []string{
		"config: RESIN_PORT is deprecated, use PRISM_PORT",
		"config: RESIN_API_MAX_BODY_BYTES is deprecated, use PRISM_API_MAX_BODY_BYTES",
	}
	if got := warnings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deprecation warnings = %v, want %v", got, want)
	}
}

// The PRISM_ spelling never warns: it is the documented name.
func TestLookupEnvDoesNotWarnForPrismVariable(t *testing.T) {
	resetLegacyEnvWarnings()
	warnings := captureLegacyEnvWarnings(t)

	unsetEnvForConfigTest(t, "PRISM_PORT", "RESIN_PORT")
	t.Setenv("PRISM_PORT", "2500")

	if _, ok := lookupEnv("PRISM_PORT"); !ok {
		t.Fatal("lookupEnv must find PRISM_PORT")
	}
	if got := warnings(); len(got) != 0 {
		t.Fatalf("PRISM_ variables must not warn, got %v", got)
	}
}

// PRISM_QUALITY_* has no Resin counterpart, so the fallback must not apply.
func TestQualityVariablesHaveNoResinFallback(t *testing.T) {
	resetLegacyEnvWarnings()
	clearTokenEnv(t)
	unsetEnvForConfigTest(t,
		"PRISM_QUALITY_API_KEY", "RESIN_QUALITY_API_KEY",
		"PRISM_QUALITY_ENABLED", "RESIN_QUALITY_ENABLED",
		"PRISM_ABUSEIPDB_API_KEY", "RESIN_ABUSEIPDB_API_KEY",
	)
	t.Setenv("PRISM_ADMIN_TOKEN", testAdminToken)
	t.Setenv("PRISM_PROXY_TOKEN", testProxyToken)
	t.Setenv("RESIN_QUALITY_API_KEY", "resin-quality-key")
	t.Setenv("RESIN_ABUSEIPDB_API_KEY", "resin-abuse-key")
	t.Setenv("RESIN_QUALITY_ENABLED", "false")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("LoadEnvConfig: %v", err)
	}
	if cfg.Quality.APIKey != "" || cfg.Quality.AbuseAPIKey != "" {
		t.Fatal("RESIN_QUALITY_* must not feed the Prism quality configuration")
	}
	if !cfg.Quality.Enabled {
		t.Fatal("RESIN_QUALITY_ENABLED must not disable the Prism quality module")
	}
}

// --- deviations X1 / X2: token semantics ---

func TestEnforceStrongTokensOffRestoresUpstreamBehaviour(t *testing.T) {
	clearTokenEnv(t)
	unsetEnvForConfigTest(t, "PRISM_LISTEN_ADDRESS", "RESIN_LISTEN_ADDRESS")
	t.Setenv("PRISM_ADMIN_TOKEN", "qwerty123")
	t.Setenv("PRISM_PROXY_TOKEN", "12345678")

	t.Setenv("PRISM_ENFORCE_STRONG_TOKENS", "true")
	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("PRISM_ENFORCE_STRONG_TOKENS=true must reject tokens shorter than 16 characters")
	}
	if !strings.Contains(err.Error(), "16 characters") {
		t.Fatalf("unexpected error: %v", err)
	}

	// PRISM-DEVIATION: X1 — with the switch off, Prism must accept weak tokens
	// exactly like upstream Resin and only report them as weak.
	t.Setenv("PRISM_ENFORCE_STRONG_TOKENS", "false")
	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("PRISM_ENFORCE_STRONG_TOKENS=false must accept a short token: %v", err)
	}
	if !IsWeakToken(cfg.AdminToken) || !IsWeakToken(cfg.ProxyToken) {
		t.Fatal("weak tokens must still be reported as weak")
	}

	// The upstream validator rules survive the deviation.
	t.Setenv("PRISM_PROXY_TOKEN", "bad:token")
	if _, err := LoadEnvConfig(); err == nil {
		t.Fatal("the V1 forbidden-character rules must still apply")
	} else if !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// PRISM-DEVIATION: X2 — an empty token disables one authentication scope and
// requires an explicit opt-in plus a loopback listener.
func TestEmptyTokenRequiresOptInAndLoopback(t *testing.T) {
	clearTokenEnv(t)
	unsetEnvForConfigTest(t, "PRISM_LISTEN_ADDRESS", "RESIN_LISTEN_ADDRESS")
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("PRISM_ADMIN_TOKEN", testAdminToken)
	t.Setenv("PRISM_PROXY_TOKEN", "")

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("an empty proxy token must be refused without PRISM_ALLOW_EMPTY_PROXY_TOKEN=true")
	}
	if !strings.Contains(err.Error(), "PRISM_ALLOW_EMPTY_PROXY_TOKEN=true") {
		t.Fatalf("the error must explain the opt-in, got: %v", err)
	}

	t.Setenv("PRISM_ALLOW_EMPTY_PROXY_TOKEN", "true")
	if _, err := LoadEnvConfig(); err != nil {
		t.Fatalf("empty proxy token on loopback with opt-in: %v", err)
	}

	// A non-loopback listener needs its own explicit override.
	t.Setenv("PRISM_LISTEN_ADDRESS", "0.0.0.0")
	_, err = LoadEnvConfig()
	if err == nil {
		t.Fatal("an empty token with a non-loopback listener must be refused")
	}
	if !strings.Contains(err.Error(), "PRISM_ALLOW_INSECURE_LISTEN=true") {
		t.Fatalf("the error must explain PRISM_ALLOW_INSECURE_LISTEN, got: %v", err)
	}

	t.Setenv("PRISM_ALLOW_INSECURE_LISTEN", "true")
	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("empty proxy token with PRISM_ALLOW_INSECURE_LISTEN=true: %v", err)
	}
	if cfg.ProxyToken != "" {
		t.Fatalf("cfg.ProxyToken = %q, want empty", cfg.ProxyToken)
	}
	if cfg.AdminToken != testAdminToken {
		t.Fatal("the admin token must stay configured")
	}
}

func TestEmptyAdminTokenRequiresItsOwnOptIn(t *testing.T) {
	clearTokenEnv(t)
	unsetEnvForConfigTest(t, "PRISM_LISTEN_ADDRESS", "RESIN_LISTEN_ADDRESS")
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("PRISM_PROXY_TOKEN", testProxyToken)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("an empty admin token must be refused without PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true")
	}
	if !strings.Contains(err.Error(), "PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true") {
		t.Fatalf("the error must explain the opt-in, got: %v", err)
	}

	t.Setenv("PRISM_ALLOW_EMPTY_ADMIN_TOKEN", "true")
	if _, err := LoadEnvConfig(); err != nil {
		t.Fatalf("empty admin token on loopback with opt-in: %v", err)
	}
}

// PRISM-DEVIATION: X2 — the empty-token loopback rule covers the independent
// management listener too, not only PRISM_LISTEN_ADDRESS.
func TestEmptyTokenRequiresLoopbackAdminListen(t *testing.T) {
	clearTokenEnv(t)
	unsetEnvForConfigTest(t,
		"PRISM_LISTEN_ADDRESS", "RESIN_LISTEN_ADDRESS",
		"PRISM_ADMIN_LISTEN", "RESIN_ADMIN_LISTEN",
	)
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("PRISM_PROXY_TOKEN", testProxyToken)
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ALLOW_EMPTY_ADMIN_TOKEN", "true")

	// A loopback management listener stays allowed with an empty admin token.
	t.Setenv("PRISM_ADMIN_LISTEN", "127.0.0.1:9090")
	if _, err := LoadEnvConfig(); err != nil {
		t.Fatalf("empty admin token with a loopback PRISM_ADMIN_LISTEN: %v", err)
	}

	// A public management listener must be refused: it is bound verbatim and
	// would expose the management plane without authentication.
	t.Setenv("PRISM_ADMIN_LISTEN", "0.0.0.0:9090")
	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("an empty token with a non-loopback PRISM_ADMIN_LISTEN must be refused")
	}
	if !strings.Contains(err.Error(), "PRISM_ADMIN_LISTEN") {
		t.Fatalf("the error must name the listener, got: %v", err)
	}
	if !strings.Contains(err.Error(), "PRISM_ALLOW_INSECURE_LISTEN=true") {
		t.Fatalf("the error must explain the override, got: %v", err)
	}

	// The documented override still works.
	t.Setenv("PRISM_ALLOW_INSECURE_LISTEN", "true")
	if _, err := LoadEnvConfig(); err != nil {
		t.Fatalf("PRISM_ALLOW_INSECURE_LISTEN=true must accept a public admin listener: %v", err)
	}
}

// PRISM-DEVIATION: X2 — an empty *proxy* token is the other way into the same
// hole: the default endpoint then serves the management surface unauthenticated
// whichever listener the operator put in front of it.
func TestEmptyProxyTokenRequiresLoopbackAdminListen(t *testing.T) {
	clearTokenEnv(t)
	unsetEnvForConfigTest(t,
		"PRISM_LISTEN_ADDRESS", "RESIN_LISTEN_ADDRESS",
		"PRISM_ADMIN_LISTEN", "RESIN_ADMIN_LISTEN",
	)
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("PRISM_ADMIN_TOKEN", testAdminToken)
	t.Setenv("PRISM_PROXY_TOKEN", "")
	t.Setenv("PRISM_ALLOW_EMPTY_PROXY_TOKEN", "true")
	t.Setenv("PRISM_ADMIN_LISTEN", "[::1]:9090")
	if _, err := LoadEnvConfig(); err != nil {
		t.Fatalf("empty proxy token with a loopback PRISM_ADMIN_LISTEN: %v", err)
	}

	t.Setenv("PRISM_ADMIN_LISTEN", "192.0.2.10:9090")
	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("an empty proxy token with a public PRISM_ADMIN_LISTEN must be refused")
	}
	if !strings.Contains(err.Error(), "PRISM_ADMIN_LISTEN") {
		t.Fatalf("the error must name the listener, got: %v", err)
	}
}
