package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"prism/internal/config"
	"prism/internal/inspection"
	"prism/internal/intel"
	"prism/internal/netutil"
	"prism/internal/proxy"
	"prism/internal/routing"
	"prism/internal/state"
	"prism/internal/topology"
)

// GeoIPService defines the GeoIP interface for testing.
type GeoIPService interface {
	Lookup(ip netip.Addr) string
	LastUpdated() time.Time
	NextScheduledUpdate() time.Time
	UpdateNow() error
	Start() error
	Stop()
}

// ServiceError wraps an error with a code for API response mapping.
type ServiceError struct {
	Code    string // INVALID_ARGUMENT, NOT_FOUND, CONFLICT, INTERNAL
	Message string
	Err     error
}

func (e *ServiceError) Error() string { return e.Message }
func (e *ServiceError) Unwrap() error { return e.Err }

func invalidArg(msg string) *ServiceError {
	return &ServiceError{Code: "INVALID_ARGUMENT", Message: msg}
}

func notFound(msg string) *ServiceError {
	return &ServiceError{Code: "NOT_FOUND", Message: msg}
}

func conflict(msg string) *ServiceError {
	return &ServiceError{Code: "CONFLICT", Message: msg}
}

func internal(msg string, err error) *ServiceError {
	return &ServiceError{Code: "INTERNAL", Message: msg, Err: err}
}

// --- ControlPlaneService ---

// ControlPlaneService provides all control plane operations.
// Handlers call its methods; business logic lives here, not in handlers.
type ControlPlaneService struct {
	Engine          *state.StateEngine
	Pool            *topology.GlobalNodePool
	SubMgr          *topology.SubscriptionManager
	Scheduler       *topology.SubscriptionScheduler
	Router          *routing.Router
	GeoIP           GeoIPService
	ProbeMgr        ProbeManager
	MatcherRuntime  *proxy.AccountMatcherRuntime
	RuntimeCfg      *atomic.Pointer[config.RuntimeConfig]
	EnvCfg          *config.EnvConfig
	EndpointRuntime EndpointRuntime
	Inspection      InspectionManager
	IPPure          *inspection.IPPureChecker
	TorRegistry     *inspection.TorRegistry
	// Intel is the WP08 facade: the authoritative intel.db store, the in-memory
	// projection and the batch job executor. It is nil until WP08 is wired.
	Intel *intel.Service

	configMu      sync.Mutex
	configVersion int
	endpointMu    sync.RWMutex
}

// ------------------------------------------------------------------
// System Config
// ------------------------------------------------------------------

// runtimeConfigAllowedFields is the set of JSON field names that can be patched.
var runtimeConfigAllowedFields = map[string]bool{
	"request_log_enabled":                      true,
	"reverse_proxy_log_detail_enabled":         true,
	"reverse_proxy_log_req_headers_max_bytes":  true,
	"reverse_proxy_log_req_body_max_bytes":     true,
	"reverse_proxy_log_resp_headers_max_bytes": true,
	"reverse_proxy_log_resp_body_max_bytes":    true,
	"max_consecutive_failures":                 true,
	"max_latency_test_interval":                true,
	"max_authority_latency_test_interval":      true,
	"max_egress_test_interval":                 true,
	"latency_test_url":                         true,
	"latency_authorities":                      true,
	"p2c_latency_window":                       true,
	"latency_decay_window":                     true,
	"cache_flush_interval":                     true,
	"cache_flush_dirty_threshold":              true,
	"intel_enabled":                            true,
	"intel_node_workers":                       true,
	"intel_check_concurrency_per_check":        true,
	"intel_max_running_jobs":                   true,
	"intel_auto_checks":                        true,
	"intel_refresh_schedule":                   true,
}

var platformPatchAllowedFields = map[string]bool{
	"name":                                 true,
	"sticky_ttl":                           true,
	"regex_filters":                        true,
	"region_filters":                       true,
	"reverse_proxy_miss_action":            true,
	"reverse_proxy_empty_account_behavior": true,
	"reverse_proxy_fixed_account_header":   true,
	"allocation_policy":                    true,
	"passive_circuit_breaker_disabled":     true,
	"scheduled_rotation_interval":          true,
	"scheduled_rotation_enabled":           true,
	"rotation_avoid_previous_ip":           true,
	"quality_policy":                       true,
}

var subscriptionPatchAllowedFields = map[string]bool{
	"name":                       true,
	"url":                        true,
	"content":                    true,
	"update_interval":            true,
	"enabled":                    true,
	"ephemeral":                  true,
	"incremental_alive_nodes":    true,
	"ephemeral_node_evict_delay": true,
	// WP08 §3.6: the per-subscription automatic-intel switch.
	"auto_intel": true,
}

func parseRuntimeConfigPatch(patchJSON json.RawMessage, out *config.RuntimeConfig) *ServiceError {
	var rawPatch map[string]json.RawMessage
	if err := json.Unmarshal(patchJSON, &rawPatch); err != nil {
		return invalidArg("invalid JSON: " + err.Error())
	}
	if len(rawPatch) == 0 {
		return invalidArg("empty patch")
	}
	for key, raw := range rawPatch {
		if !runtimeConfigAllowedFields[key] {
			return invalidArg(fmt.Sprintf("unknown or read-only field: %q", key))
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return invalidArg(fmt.Sprintf("null value not allowed for field: %q", key))
		}
	}

	dec := json.NewDecoder(bytes.NewReader(patchJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return invalidArg("validation failed: " + err.Error())
	}
	return nil
}

func copyRuntimeConfig(cfg *config.RuntimeConfig) *config.RuntimeConfig {
	if cfg == nil {
		return config.NewDefaultRuntimeConfig()
	}
	out := *cfg
	out.LatencyAuthorities = append([]string(nil), cfg.LatencyAuthorities...)
	return &out
}

// PatchRuntimeConfig applies a constrained partial patch to the runtime config.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
// Pipeline: validate → persist → atomic swap.
func (s *ControlPlaneService) PatchRuntimeConfig(patchJSON json.RawMessage) (*config.RuntimeConfig, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	// 3. Deep-copy current config → apply patch.
	newCfg := copyRuntimeConfig(s.RuntimeCfg.Load())
	if verr := parseRuntimeConfigPatch(patchJSON, newCfg); verr != nil {
		return nil, verr
	}

	// 4. Additional validation.
	if err := validateRuntimeConfig(newCfg); err != nil {
		return nil, err
	}

	// On process start, initialize local configVersion from persisted state
	// so PATCH keeps monotonically increasing versions across restarts.
	if s.configVersion == 0 && s.Engine != nil {
		_, persistedVersion, err := s.Engine.GetSystemConfig()
		if err != nil {
			return nil, internal("load persisted config version", err)
		}
		if persistedVersion > s.configVersion {
			s.configVersion = persistedVersion
		}
	}

	// 5. Persist.
	newVersion := s.configVersion + 1
	if err := s.Engine.SaveSystemConfig(newCfg, newVersion, time.Now().UnixNano()); err != nil {
		return nil, internal("persist config", err)
	}

	// 6. Atomic swap.
	s.RuntimeCfg.Store(newCfg)
	s.configVersion = newVersion

	return newCfg, nil
}

// requestLogCaptureMaxBytes is the largest value accepted for the four
// `reverse_proxy_log_*_max_bytes` runtime settings.
//
// The capture buffer is allocated per in-flight proxied request
// (internal/proxy/request_log_capture.go, payloadCaptureReadCloser), so a
// value near 1 GiB would let anyone holding the admin token make every
// concurrent proxied request allocate that much heap. The shipped defaults are
// 1 KiB and the largest realistic diagnostic need is a few hundred KiB of
// request/response body, so 1 MiB is the ceiling a real operator never has to
// exceed. `0` keeps meaning "capture nothing"; negatives stay rejected.
const requestLogCaptureMaxBytes = 1 << 20

func validateRuntimeConfig(cfg *config.RuntimeConfig) *ServiceError {
	latencyURL := strings.TrimSpace(cfg.LatencyTestURL)
	u, verr := parseHTTPAbsoluteURL("latency_test_url", latencyURL)
	if verr != nil {
		return verr
	}
	latencyDomain := strings.ToLower(netutil.ExtractDomain(u.Host))
	if cfg.MaxConsecutiveFailures < 0 {
		return invalidArg("max_consecutive_failures: must be non-negative")
	}
	if cfg.CacheFlushDirtyThreshold < 0 {
		return invalidArg("cache_flush_dirty_threshold: must be non-negative")
	}
	// Request log capture limits must be non-negative and bounded: the buffer
	// is per in-flight request, so the ceiling is what keeps a single runtime
	// config patch from turning into an unbounded per-request allocation.
	for _, field := range []struct {
		name  string
		value int
	}{
		{"reverse_proxy_log_req_headers_max_bytes", cfg.ReverseProxyLogReqHeadersMaxBytes},
		{"reverse_proxy_log_req_body_max_bytes", cfg.ReverseProxyLogReqBodyMaxBytes},
		{"reverse_proxy_log_resp_headers_max_bytes", cfg.ReverseProxyLogRespHeadersMaxBytes},
		{"reverse_proxy_log_resp_body_max_bytes", cfg.ReverseProxyLogRespBodyMaxBytes},
	} {
		if field.value < 0 || field.value > requestLogCaptureMaxBytes {
			return invalidArg(fmt.Sprintf(
				"%s: must be between 0 and %d", field.name, requestLogCaptureMaxBytes))
		}
	}
	minProbeInterval := 30 * time.Second
	// Probe intervals must be at least 30s (DESIGN.md).
	if time.Duration(cfg.MaxLatencyTestInterval) < minProbeInterval {
		return invalidArg("max_latency_test_interval: must be >= 30s")
	}
	if time.Duration(cfg.MaxAuthorityLatencyTestInterval) < minProbeInterval {
		return invalidArg("max_authority_latency_test_interval: must be >= 30s")
	}
	if time.Duration(cfg.MaxEgressTestInterval) < minProbeInterval {
		return invalidArg("max_egress_test_interval: must be >= 30s")
	}
	if cfg.P2CLatencyWindow < 0 {
		return invalidArg("p2c_latency_window: must be non-negative")
	}
	if cfg.LatencyDecayWindow < 0 {
		return invalidArg("latency_decay_window: must be non-negative")
	}
	minCacheFlushInterval := 5 * time.Second
	if time.Duration(cfg.CacheFlushInterval) < minCacheFlushInterval {
		return invalidArg("cache_flush_interval: must be >= 5s")
	}

	// LatencyTestURL domain must be in LatencyAuthorities.
	// If absent, append it instead of returning an error.
	if latencyDomain != "" {
		found := false
		for _, authority := range cfg.LatencyAuthorities {
			if strings.EqualFold(strings.TrimSpace(authority), latencyDomain) {
				found = true
				break
			}
		}
		if !found {
			cfg.LatencyAuthorities = append(cfg.LatencyAuthorities, latencyDomain)
		}
	}
	if verr := validateIntelConfig(cfg); verr != nil {
		return verr
	}
	return nil
}

// validateIntelConfig bounds the intel batch settings of WP08 §3.4.
func validateIntelConfig(cfg *config.RuntimeConfig) *ServiceError {
	if cfg.IntelNodeWorkers < 1 || cfg.IntelNodeWorkers > 128 {
		return invalidArg("intel_node_workers: must be between 1 and 128")
	}
	if cfg.IntelMaxRunningJobs < 1 {
		return invalidArg("intel_max_running_jobs: must be at least 1")
	}
	if cfg.IntelCheckConcurrencyPerCheck < 1 {
		return invalidArg("intel_check_concurrency_per_check: must be at least 1")
	}
	schedule := strings.TrimSpace(cfg.IntelRefreshSchedule)
	if schedule == "" {
		return invalidArg("intel_refresh_schedule: must not be empty")
	}
	if _, err := cron.ParseStandard(schedule); err != nil {
		return invalidArg("intel_refresh_schedule: " + err.Error())
	}
	return nil
}
