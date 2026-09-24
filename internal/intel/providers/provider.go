// Package providers implements the pluggable intel data sources of WP09 §1–§3:
// offline databases, server-side online lookups and via-node lookups. Every
// provider returns a normalised quality.Evidence plus an optional bounded raw
// response; nothing in this package talks to the network during a unit test
// (all HTTP endpoints, DNS resolvers and mmdb readers are injectable).
package providers

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"prism/internal/quality"
)

// Kind classifies a data source (WP09 §1).
type Kind int

const (
	// KindOffline queries a local database; it never touches the network.
	KindOffline Kind = iota
	// KindOnlineIP asks a third-party API about an IP from the Prism host.
	KindOnlineIP
	// KindViaNode asks a "what is my IP" style API through the tested node.
	KindViaNode
)

// String renders the wire form used by the API and the settings table.
func (k Kind) String() string {
	switch k {
	case KindOffline:
		return "offline"
	case KindOnlineIP:
		return "online-ip"
	case KindViaNode:
		return "via-node"
	default:
		return "unknown"
	}
}

// MarshalJSON renders the stable textual kind.
func (k Kind) MarshalJSON() ([]byte, error) { return []byte(`"` + k.String() + `"`), nil }

// Error codes of ProviderError (WP09 §1).
const (
	CodeLimit        = "PROVIDER_LIMIT"
	CodeAuth         = "PROVIDER_AUTH"
	CodeUnavailable  = "PROVIDER_UNAVAILABLE"
	CodeResponse     = "PROVIDER_RESPONSE"
	CodeUnsupported  = "UNSUPPORTED_IP"
	CodeEgressMismat = "EGRESS_MISMATCH"
	CodeRequest      = "PROVIDER_REQUEST"
)

// MaxRawBytes bounds the stored raw provider response (WP09 §1: ≤ 32 KiB).
const MaxRawBytes = 32 * 1024

// Spec is the static description of one data source (WP09 §1).
type Spec struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Website string `json:"website"`
	// Terms is the quota/terms hint shown on the settings page. It must quote
	// the vendor's published limits; Prism never hides a third-party condition.
	Terms                    string        `json:"terms"`
	Kind                     Kind          `json:"kind"`
	Profile                  string        `json:"profile"`
	RequiresKey              bool          `json:"requires_key"`
	DefaultEnabled           bool          `json:"default_enabled"`
	DefaultDailyLimit        int           `json:"default_daily_limit"`
	DefaultDailyLimitWithKey int           `json:"default_daily_limit_with_key,omitempty"`
	DefaultQPS               float64       `json:"default_qps"`
	BatchSize                int           `json:"batch_size"`
	DefaultTTL               time.Duration `json:"-"`
	SupportsIPv6             bool          `json:"supports_ipv6"`
	// MaxDailyLimit caps the user-configurable daily limit (0 = no cap).
	MaxDailyLimit int `json:"max_daily_limit"`
	// CredentialFields names the config keys that hold a secret, so the API can
	// report has_key for providers such as maxmind_geolite2.
	CredentialFields []string `json:"credential_fields,omitempty"`
}

// TTLString renders the default TTL as a Go duration string (R8).
func (s Spec) TTLString() string { return DurationString(s.DefaultTTL) }

// DurationString renders a duration the way the API documents it.
func DurationString(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.String()
}

// ProviderError is a bounded, explainable failure of one lookup.
type ProviderError struct {
	Code       string        `json:"code"`
	Message    string        `json:"message"`
	RetryAfter time.Duration `json:"-"`
	// Pause asks the queue worker to pause the provider (401/403).
	Pause bool `json:"-"`
}

// Error implements error.
func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Result is the outcome of one provider lookup.
type Result struct {
	// Evidence is non-nil when the lookup produced usable evidence.
	Evidence *quality.Evidence
	// Raw is the bounded original response; nil means "do not store".
	Raw []byte
	// Err describes why no evidence was produced.
	Err *ProviderError
}

// Failed reports whether the lookup produced an explainable failure.
func (r Result) Failed() bool { return r.Err != nil }

// Failure builds an error result.
func Failure(code, message string) Result {
	return Result{Err: &ProviderError{Code: code, Message: message}}
}

// FailureErr wraps an error as a PROVIDER_UNAVAILABLE result. The message is a
// fixed string: provider URLs may embed credentials, so raw transport errors are
// never propagated (R6).
func FailureErr(err error, message string) Result {
	if err == nil {
		return Failure(CodeUnavailable, message)
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return Result{Err: providerErr}
	}
	return Failure(CodeUnavailable, message)
}

// OfflineProvider queries a local database.
type OfflineProvider interface {
	Spec() Spec
	Lookup(ip netip.Addr) Result
}

// OnlineProvider queries a third-party API from the Prism host. BatchSize of the
// Spec tells the caller how many IPs one Lookup may resolve.
type OnlineProvider interface {
	Spec() Spec
	Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result
}

// ViaNodeProvider queries an API through the node under test.
type ViaNodeProvider interface {
	Spec() Spec
	Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result
}

// Factory builds one provider implementation from its effective setting. The
// registry rebuilds instances whenever the persisted settings change, so a key
// rotation takes effect without a restart.
type Factory func(Setting) any

// Registry holds the built-in data sources of §3 and resolves their effective
// settings against the persisted per-provider configuration.
type Registry struct {
	mu        sync.RWMutex
	specs     map[string]Spec
	order     []string
	factories map[string]Factory
	impls     map[string]any
	settings  map[string]Setting
}

// NewRegistry returns an empty registry. Use RegisterBuiltins to fill it.
func NewRegistry() *Registry {
	return &Registry{
		specs:     make(map[string]Spec),
		factories: make(map[string]Factory),
		impls:     make(map[string]any),
		settings:  make(map[string]Setting),
	}
}

// Define registers the spec and factory of one data source. A duplicate id
// panics: it is a programming error (R5).
func (r *Registry) Define(spec Spec, factory Factory) {
	if strings.TrimSpace(spec.ID) == "" {
		panic("providers: provider id is required")
	}
	if factory == nil {
		panic("providers: provider factory is required for " + spec.ID)
	}
	if strings.TrimSpace(spec.Profile) == "" {
		panic("providers: provider profile is required for " + spec.ID)
	}
	if spec.BatchSize <= 0 {
		spec.BatchSize = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.specs[spec.ID]; exists {
		panic("providers: duplicate provider id " + spec.ID)
	}
	r.specs[spec.ID] = spec
	r.factories[spec.ID] = factory
	r.order = append(r.order, spec.ID)
	sort.Strings(r.order)
}

// Spec returns one spec by id.
func (r *Registry) Spec(id string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[id]
	return spec, ok
}

// Specs returns every spec ordered by id.
func (r *Registry) Specs() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.specs[id])
	}
	return out
}

// IDs returns every provider id ordered by id.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Apply installs the effective settings and rebuilds the provider instances
// that need a credential or a per-provider config value. Unknown ids and
// disabled providers are skipped; the previous instance of a disabled provider
// is removed so nothing keeps using a cleared key.
func (r *Registry) Apply(settings []Setting) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nextImpls := make(map[string]any, len(r.order))
	nextSettings := make(map[string]Setting, len(r.order))
	for _, setting := range settings {
		factory, ok := r.factories[setting.Spec.ID]
		if !ok {
			continue
		}
		nextSettings[setting.Spec.ID] = setting
		if !setting.Enabled || !setting.Runnable() {
			continue
		}
		nextImpls[setting.Spec.ID] = factory(setting)
	}
	// Providers without an explicit setting keep their default spec and stay
	// unavailable until Apply is called with their resolved setting.
	r.impls = nextImpls
	r.settings = nextSettings
}

// Setting returns the effective setting of one provider.
func (r *Registry) Setting(id string) (Setting, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	setting, ok := r.settings[id]
	return setting, ok
}

// Settings returns every effective setting ordered by id.
func (r *Registry) Settings() []Setting {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Setting, 0, len(r.order))
	for _, id := range r.order {
		if setting, ok := r.settings[id]; ok {
			out = append(out, setting)
		}
	}
	return out
}

// Offline returns the offline implementation of an id.
func (r *Registry) Offline(id string) (OfflineProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.impls[id].(OfflineProvider)
	return p, ok
}

// Online returns the online-ip implementation of an id.
func (r *Registry) Online(id string) (OnlineProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.impls[id].(OnlineProvider)
	return p, ok
}

// ViaNode returns the via-node implementation of an id.
func (r *Registry) ViaNode(id string) (ViaNodeProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.impls[id].(ViaNodeProvider)
	return p, ok
}

// EnabledOffline returns every runnable offline provider of a selection. An
// empty selection means "every enabled provider" (WP09 §3).
func (r *Registry) EnabledOffline(selection []string) []OfflineProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]OfflineProvider, 0, len(r.order))
	for _, id := range r.selectedOrder(selection) {
		if p, ok := r.impls[id].(OfflineProvider); ok {
			out = append(out, p)
		}
	}
	return out
}

// EnabledOnline returns every runnable online-ip provider of a selection.
func (r *Registry) EnabledOnline(selection []string) []OnlineProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]OnlineProvider, 0, len(r.order))
	for _, id := range r.selectedOrder(selection) {
		if p, ok := r.impls[id].(OnlineProvider); ok {
			out = append(out, p)
		}
	}
	return out
}

// EnabledViaNode returns every runnable via-node provider of a selection.
func (r *Registry) EnabledViaNode(selection []string) []ViaNodeProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ViaNodeProvider, 0, len(r.order))
	for _, id := range r.selectedOrder(selection) {
		if p, ok := r.impls[id].(ViaNodeProvider); ok {
			out = append(out, p)
		}
	}
	return out
}

// selectedOrder resolves a provider selection to ids in registry order. An
// empty selection means every provider.
func (r *Registry) selectedOrder(selection []string) []string {
	if len(selection) == 0 {
		ids := make([]string, 0, len(r.order))
		for _, id := range r.order {
			if _, ok := r.impls[id]; ok {
				ids = append(ids, id)
			}
		}
		return ids
	}
	wanted := make(map[string]struct{}, len(selection))
	for _, id := range selection {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	ids := make([]string, 0, len(wanted))
	for _, id := range r.order {
		if _, ok := wanted[id]; !ok {
			continue
		}
		if _, ok := r.impls[id]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// QueueSpecs projects the enabled online-ip providers onto the queue-worker
// configuration used by internal/intel/jobs.
func (r *Registry) QueueSpecs() []QueueSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]QueueSpec, 0, len(r.order))
	for _, id := range r.order {
		setting, ok := r.settings[id]
		if !ok || setting.Spec.Kind != KindOnlineIP {
			continue
		}
		out = append(out, QueueSpec{
			ProviderID:   id,
			Enabled:      setting.Enabled && setting.Runnable(),
			DailyLimit:   setting.DailyLimit,
			QPS:          setting.QPS,
			BatchSize:    setting.Spec.BatchSize,
			CredentialID: setting.CredentialID(),
		})
	}
	return out
}

// Unsupported maps a non-public IP to the documented unsupported result.
func Unsupported(ip netip.Addr) Result {
	return Failure(CodeUnsupported, fmt.Sprintf("%s is not a public IP address", ip))
}

// PublicIP validates an address for a provider lookup.
func PublicIP(ip netip.Addr) (netip.Addr, Result, bool) {
	public, err := quality.PublicIP(ip)
	if err != nil {
		return netip.Addr{}, Unsupported(ip), false
	}
	return public, Result{}, true
}

// Supports reports whether a provider accepts an address family (WP09 §3).
func Supports(spec Spec, ip netip.Addr) bool {
	if !spec.SupportsIPv6 && ip.Is6() {
		return false
	}
	return true
}

// boundedRaw caps a stored raw response at MaxRawBytes.
func boundedRaw(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > MaxRawBytes {
		return raw[:MaxRawBytes]
	}
	return raw
}
