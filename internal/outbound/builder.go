package outbound

import (
	"encoding/json"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing/service"
)

// OutboundBuilder creates outbound instances from raw node options.
type OutboundBuilder interface {
	Build(rawOptions json.RawMessage) (adapter.Outbound, error)
}

// SingboxBuilderConfig configures SingboxBuilder construction.
type SingboxBuilderConfig struct {
	// DNSUpstreams configures Prism's node DNS chain.
	// Values are DNS upstream URI strings and the slice must not be empty.
	DNSUpstreams []string
	// QuietInterfaceMonitor replaces sing-box's netlink default-interface monitor
	// with a no-op one.
	//
	// It was introduced because sing-box v1.12.21 through v1.14.1 tripped a data
	// race in route.NetworkManager (a plain `started` bool written at :220 and
	// read at :574). sing-box v1.14.2 removed that field in favour of
	// startedCtx/startedCancel, so the race is gone; the switch stays because the
	// tests want no live netlink monitor in the first place. Production keeps the
	// real monitor.
	QuietInterfaceMonitor bool
}

// ---------------------------------------------------------------------------
// SingboxBuilder — WP06 compatibility wrapper around SingboxRuntime.
// ---------------------------------------------------------------------------

// SingboxBuilder builds real sing-box outbound instances from raw JSON options.
// Since WP06 it delegates to the embedded sing-box runtime (§2); the type is
// kept so that existing callers and tests keep working unchanged.
type SingboxBuilder struct {
	runtime             *SingboxRuntime
	dnsTransportManager *dns.TransportManager
}

// NewSingboxBuilderWithConfig creates a SingboxBuilder backed by an embedded
// sing-box instance. The caller must call Close() when done.
func NewSingboxBuilderWithConfig(cfg SingboxBuilderConfig) (*SingboxBuilder, error) {
	runtime, err := NewSingboxRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return &SingboxBuilder{runtime: runtime, dnsTransportManager: runtime.DNSTransportManager()}, nil
}

// Build parses rawOptions (a form A outbound, a form B endpoint, or a form B
// detour chain) into a real adapter.Outbound instance.
func (b *SingboxBuilder) Build(rawOptions json.RawMessage) (adapter.Outbound, error) {
	return b.runtime.Build(rawOptions)
}

// Close shuts down the embedded sing-box instance.
func (b *SingboxBuilder) Close() error {
	return b.runtime.Close()
}

// DNSTransportManager exposes the runtime's DNS transport manager for tests and
// diagnostics (the secure DNS chain assertions in builder_test.go).
func (b *SingboxBuilder) DNSTransportManager() *dns.TransportManager {
	manager, _ := service.FromContext[adapter.DNSTransportManager](b.runtime.ctx).(*dns.TransportManager)
	return manager
}

// Runtime returns the underlying SingboxRuntime.
func (b *SingboxBuilder) Runtime() *SingboxRuntime {
	return b.runtime
}
