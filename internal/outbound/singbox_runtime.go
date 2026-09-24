package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	sJson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"prism/internal/node"
)

const (
	// envMaxEndpoints bounds the number of concurrently active endpoint nodes.
	envMaxEndpoints = "PRISM_MAX_ENDPOINTS"
	// defaultMaxEndpoints is the default endpoint budget (WP06 §2).
	defaultMaxEndpoints = int64(256)

	errEndpointsExhausted = "ENDPOINT_LIMIT: too many active endpoint nodes"

	// outboundTagPrefix prefixes every runtime-generated sing-box tag.
	outboundTagPrefix = "n/"
)

// SingboxRuntime owns an embedded sing-box instance and creates node outbounds,
// endpoints and detour chains inside it (WP06 §2, decisions D2 and D4).
//
// Every node gets a unique tag (`n/<hash>/<seq>`) because
// OutboundManager.Create replaces an existing instance with the same tag; two
// concurrent builders of the same node must not delete each other's instance
// when the CAS loser closes its handle.
type SingboxRuntime struct {
	ctx          context.Context
	inst         *box.Box
	logger       log.ContextLogger
	seq          atomic.Uint64
	endpoints    atomic.Int64
	maxEndpoints int64
}

// NewSingboxRuntime starts an embedded sing-box instance with Prism's secure
// node DNS chain. The caller must call Close when done.
func NewSingboxRuntime(cfg SingboxBuilderConfig) (*SingboxRuntime, error) {
	ctx := include.Context(context.Background())
	registry, ok := service.FromContext[adapter.DNSTransportRegistry](ctx).(*dns.TransportRegistry)
	if !ok {
		return nil, fmt.Errorf("singbox runtime: unexpected DNS transport registry type %T",
			service.FromContext[adapter.DNSTransportRegistry](ctx))
	}
	registerSecureDNSTransport(registry)
	if cfg.QuietInterfaceMonitor {
		// See SingboxBuilderConfig.QuietInterfaceMonitor.
		ctx = service.ContextWith[adapter.PlatformInterface](ctx, newQuietPlatformInterface())
	}
	specs, err := secureDNSTransportSpecsForUpstreams(cfg.DNSUpstreams)
	if err != nil {
		return nil, err
	}
	servers := make([]option.DNSServerOptions, 0, len(specs))
	for _, spec := range specs {
		servers = append(servers, option.DNSServerOptions{
			Type:    spec.transportType,
			Tag:     spec.tag,
			Options: spec.options,
		})
	}

	inst, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{Disabled: true},
			DNS: &option.DNSOptions{
				RawDNSOptions: option.RawDNSOptions{
					Servers: servers,
					Final:   secureDNSFailoverTransportTag,
				},
			},
			Route: &option.RouteOptions{
				DefaultDomainResolver: &option.DomainResolveOptions{Server: secureDNSFailoverTransportTag},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("singbox runtime: create instance: %w", err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		return nil, fmt.Errorf("singbox runtime: start instance: %w", err)
	}
	return &SingboxRuntime{
		ctx:          ctx,
		inst:         inst,
		logger:       inst.LogFactory().NewLogger("prism-outbound"),
		maxEndpoints: resolveMaxEndpoints(),
	}, nil
}

// resolveMaxEndpoints reads PRISM_MAX_ENDPOINTS with a bound and a default.
func resolveMaxEndpoints() int64 {
	raw := os.Getenv(envMaxEndpoints)
	if raw == "" {
		return defaultMaxEndpoints
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return defaultMaxEndpoints
	}
	return value
}

// DNSTransportManager returns the DNS transport manager of the embedded
// instance, which carries Prism's secure DNS chain.
func (r *SingboxRuntime) DNSTransportManager() *dns.TransportManager {
	manager, _ := service.FromContext[adapter.DNSTransportManager](r.ctx).(*dns.TransportManager)
	return manager
}

// ActiveEndpoints reports the current number of live endpoint nodes.
func (r *SingboxRuntime) ActiveEndpoints() int64 {
	return r.endpoints.Load()
}

// Close shuts down the embedded sing-box instance.
func (r *SingboxRuntime) Close() error {
	if r.inst == nil {
		return nil
	}
	return r.inst.Close()
}

// Build creates the sing-box instances for one node document and returns an
// adapter.Outbound handle. Closing the handle removes every created instance.
func (r *SingboxRuntime) Build(rawOptions json.RawMessage) (adapter.Outbound, error) {
	doc, err := node.ParseNodeDoc(rawOptions)
	if err != nil {
		return nil, err
	}
	if doc.Kind == node.DocProxy {
		return nil, fmt.Errorf("ENGINE_NOT_BUILT:%s requires the with_mihomo build", doc.Type)
	}

	base := outboundTagPrefix + doc.Hash.Hex()[:16] + "/" + strconv.FormatUint(r.seq.Add(1), 10)
	handle := &singboxHandle{rt: r}

	switch doc.Kind {
	case node.DocOutbound:
		if err := r.createObject(handle, base, doc.Main, false); err != nil {
			handle.Close()
			return nil, err
		}
	case node.DocEndpoint:
		if err := r.createObject(handle, base, doc.Main, true); err != nil {
			handle.Close()
			return nil, err
		}
	case node.DocChain:
		for index, dep := range doc.Deps {
			depTag := base + "/" + node.DepTag(index)
			depRaw, err := node.RewriteDepDetours(dep, base)
			if err != nil {
				handle.Close()
				return nil, err
			}
			isEndpoint, err := isEndpointObject(depRaw)
			if err != nil {
				handle.Close()
				return nil, err
			}
			if err := r.createObject(handle, depTag, depRaw, isEndpoint); err != nil {
				handle.Close()
				return nil, err
			}
		}
		mainRaw, err := node.RewriteDepDetours(doc.Main, base)
		if err != nil {
			handle.Close()
			return nil, err
		}
		isEndpoint, err := isEndpointObject(mainRaw)
		if err != nil {
			handle.Close()
			return nil, err
		}
		if err := r.createObject(handle, base, mainRaw, isEndpoint); err != nil {
			handle.Close()
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported node kind %s", doc.Kind)
	}

	created, ok := r.inst.Outbound().Outbound(base)
	if !ok || created == nil {
		handle.Close()
		return nil, fmt.Errorf("singbox runtime: created instance %q not found", base)
	}
	handle.Outbound = created
	return handle, nil
}

func isEndpointObject(raw json.RawMessage) (bool, error) {
	typeName, err := node.TypeOfObject(raw)
	if err != nil {
		return false, err
	}
	return node.IsEndpointType(typeName), nil
}

// createObject creates one outbound or endpoint and records it on the handle.
func (r *SingboxRuntime) createObject(handle *singboxHandle, tag string, raw json.RawMessage, endpoint bool) error {
	if endpoint {
		if err := r.reserveEndpoint(); err != nil {
			return err
		}
		if err := r.createEndpoint(tag, raw); err != nil {
			r.endpoints.Add(-1)
			return err
		}
		handle.record(tag, true)
		return nil
	}
	if err := r.createOutbound(tag, raw); err != nil {
		return err
	}
	handle.record(tag, false)
	return nil
}

func (r *SingboxRuntime) reserveEndpoint() error {
	if r.endpoints.Add(1) > r.maxEndpoints {
		r.endpoints.Add(-1)
		return errors.New(errEndpointsExhausted)
	}
	return nil
}

func (r *SingboxRuntime) createOutbound(tag string, raw json.RawMessage) error {
	var parsed option.Outbound
	if err := sJson.UnmarshalContext(r.ctx, raw, &parsed); err != nil {
		return fmt.Errorf("parse outbound options: %w", err)
	}
	logger := r.logger
	if logger == nil {
		logger = r.inst.LogFactory().NewLogger("outbound/" + parsed.Type)
	}
	if err := r.inst.Outbound().Create(r.ctx, r.inst.Router(), logger, tag, parsed.Type, parsed.Options); err != nil {
		return fmt.Errorf("create outbound [%s]: %w", parsed.Type, err)
	}
	return nil
}

func (r *SingboxRuntime) createEndpoint(tag string, raw json.RawMessage) error {
	var parsed option.Endpoint
	if err := sJson.UnmarshalContext(r.ctx, raw, &parsed); err != nil {
		return fmt.Errorf("parse endpoint options: %w", err)
	}
	logger := r.logger
	if logger == nil {
		logger = r.inst.LogFactory().NewLogger("endpoint/" + parsed.Type)
	}
	if err := r.inst.Endpoint().Create(r.ctx, r.inst.Router(), logger, tag, parsed.Type, parsed.Options); err != nil {
		return fmt.Errorf("create endpoint [%s]: %w", parsed.Type, err)
	}
	return nil
}

// singboxHandle is the per-node handle returned by SingboxRuntime.Build.
// It removes every instance the build created, in reverse creation order.
type singboxHandle struct {
	adapter.Outbound
	rt     *SingboxRuntime
	tags   []string
	isEP   []bool
	closed atomic.Bool
	once   sync.Once
}

func (h *singboxHandle) record(tag string, endpoint bool) {
	h.tags = append(h.tags, tag)
	h.isEP = append(h.isEP, endpoint)
}

// Close removes the created instances (idempotent).
func (h *singboxHandle) Close() error {
	h.once.Do(func() {
		h.closed.Store(true)
		for i := len(h.tags) - 1; i >= 0; i-- {
			tag := h.tags[i]
			if h.isEP[i] {
				if err := h.rt.inst.Endpoint().Remove(tag); err == nil {
					h.rt.endpoints.Add(-1)
				}
				continue
			}
			_ = h.rt.inst.Outbound().Remove(tag)
		}
	})
	return nil
}

// SingboxRuntime implements OutboundBuilder.
var _ OutboundBuilder = (*SingboxRuntime)(nil)

// init registers the engine runtimes this package can construct. Only sing-box
// is registered: Build rejects mihomo proxy documents with ENGINE_NOT_BUILT, so
// node.EngineCapabilities() must keep reporting mihomo as not built even in a
// `with_mihomo` build (docs/ENGINE_DECISIONS.md D-1). Registering an engine here
// is the only way a capability can be reported as built.
func init() {
	node.RegisterEngineRuntime(node.EngineSingbox)
}
