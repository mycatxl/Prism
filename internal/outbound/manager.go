package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sagernet/sing-box/adapter"
	"prism/internal/addrpolicy"
	"prism/internal/netutil"
	"prism/internal/node"
)

var ErrOutboundNotReady = errors.New("outbound not ready")

// maxNodeErrorBytes bounds the error text stored on a node entry. Engine build
// errors can quote the content of a file named by the node document (a node
// document is untrusted subscription content: see node.RejectLocalFileRefs),
// and the stored text is returned through the node APIs and the request log, so
// it is capped instead of growing with whatever the engine read.
const maxNodeErrorBytes = 512

// boundNodeError truncates msg at a rune boundary.
func boundNodeError(msg string) string {
	if len(msg) <= maxNodeErrorBytes {
		return msg
	}
	cut := maxNodeErrorBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "(truncated)"
}

// PoolAccessor provides read-only access to the node pool.
type PoolAccessor interface {
	GetEntry(hash node.Hash) (*node.NodeEntry, bool)
	RangeNodes(fn func(node.Hash, *node.NodeEntry) bool)
}

// closeOutbound closes an outbound if it implements io.Closer.
func closeOutbound(ob adapter.Outbound) {
	if c, ok := ob.(io.Closer); ok {
		_ = c.Close()
	}
}

func buildOutboundSafely(builder OutboundBuilder, rawOptions json.RawMessage) (ob adapter.Outbound, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ob != nil {
				closeOutbound(ob)
			}
			ob = nil
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return builder.Build(rawOptions)
}

// OutboundManager manages outbound lifecycle and provides unified HTTP execution.
type OutboundManager struct {
	pool    PoolAccessor
	builder OutboundBuilder
	// denyForbiddenTargets enables the node-admission address policy: a node
	// whose server names loopback, the LAN or a cloud metadata endpoint is
	// refused before an outbound is built for it. Off by default, so a deployment
	// that intentionally routes through a private node keeps working.
	denyForbiddenTargets bool
}

func NewOutboundManager(pool PoolAccessor, builder OutboundBuilder) *OutboundManager {
	return &OutboundManager{pool: pool, builder: builder}
}

// SetDenyForbiddenTargets turns the node-admission address policy on or off.
// It must be called before the pool starts serving traffic.
func (m *OutboundManager) SetDenyForbiddenTargets(enabled bool) {
	if m == nil {
		return
	}
	m.denyForbiddenTargets = enabled
}

func (m *OutboundManager) isLiveEntry(hash node.Hash, entry *node.NodeEntry) bool {
	current, ok := m.pool.GetEntry(hash)
	return ok && current == entry
}

// forbiddenTargetReason applies the node-admission address policy to a node.
//
// It is the second gate over node targets, and the one that matters for nodes
// that entered the pool by some route other than the public-source collector (a
// hand-added subscription, a restored state.db). The collector screens untrusted
// gist content lexically; this screens the *resolved* target of anything that is
// about to be dialled, which is what catches a public-looking hostname whose DNS
// answer is loopback ("127.0.0.1.nip.io").
//
// The policy is opt-in. A deployment that deliberately routes through a node on
// a private network (a home server, a jump host) keeps working with it off.
func (m *OutboundManager) forbiddenTargetReason(entry *node.NodeEntry) (string, bool) {
	if m == nil || !m.denyForbiddenTargets || entry == nil || len(entry.RawOptions) == 0 {
		return "", false
	}
	hosts := nodeServerHosts(entry.RawOptions)
	if len(hosts) == 0 {
		// No server field means the builder will refuse it anyway; nothing to
		// classify here.
		return "", false
	}
	for _, host := range hosts {
		if denied, reason := addrpolicy.NodeTargetIsForbidden(context.Background(), host, nil); denied {
			return reason + ": " + host, true
		}
	}
	return "", false
}

// nodeServerHosts returns every "server" value a node document carries, at any
// nesting depth.
//
// A single node can name more than one target: a chain names its first hop in the
// main object and the next hop in a dep, and an endpoint names its peer. Every one
// of them is a dial target, so every one of them is classified. The walk is
// depth-bounded so a hostile document cannot drive unbounded recursion.
func nodeServerHosts(raw json.RawMessage) []string {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil
	}
	hosts := make([]string, 0, 2)
	collectNodeServerHosts(document, 0, &hosts)
	return hosts
}

// maxNodeTargetDepth bounds nodeServerHosts' walk. A node document nests a few
// levels (envelope, main, transport, tls); anything deeper is not a node.
const maxNodeTargetDepth = 16

func collectNodeServerHosts(value any, depth int, out *[]string) {
	if depth > maxNodeTargetDepth {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		// "server" is the dial target of a proxy outbound; a wireguard endpoint
		// names the same thing as a peer's "address".
		keys := []string{"server"}
		if isWireGuardPeer(typed) {
			keys = append(keys, "address")
		}
		for _, key := range keys {
			if host, ok := typed[key].(string); ok && strings.TrimSpace(host) != "" {
				*out = append(*out, host)
			}
		}
		for _, entry := range typed {
			collectNodeServerHosts(entry, depth+1, out)
		}
	case []any:
		for _, entry := range typed {
			collectNodeServerHosts(entry, depth+1, out)
		}
	}
}

// isWireGuardPeer reports whether object is a wireguard endpoint peer, whose
// dial target lives in "address" rather than "server".
func isWireGuardPeer(object map[string]any) bool {
	if _, ok := object["public_key"]; !ok {
		return false
	}
	_, ok := object["address"]
	return ok
}

// EnsureNodeOutbound idempotently creates and stores an outbound for a node.
// Uses CompareAndSwap(nil, &wrapped) to guarantee only one goroutine's build
// result is stored. Losers discard their result (stage 6 adds io.Closer release).
func (m *OutboundManager) EnsureNodeOutbound(hash node.Hash) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return
	}
	// Fast path: already has outbound.
	if entry.Outbound.Load() != nil {
		return
	}
	if reason, denied := m.forbiddenTargetReason(entry); denied {
		entry.SetLastError(boundNodeError("outbound denied: " + reason))
		return
	}

	ob, err := buildOutboundSafely(m.builder, entry.RawOptions)
	if err != nil {
		entry.SetLastError(boundNodeError("outbound build: " + err.Error()))
		return
	}

	// Build can race with node deletion/replacement. If this entry is no longer
	// the pool's live value for the hash, discard the build result.
	if !m.isLiveEntry(hash, entry) {
		closeOutbound(ob)
		return
	}

	if !entry.Outbound.CompareAndSwap(nil, &ob) {
		// Another goroutine won the race. Close the losing build result.
		closeOutbound(ob)
		return
	}

	// Close-and-clear if the node disappeared/replaced right after CAS.
	if !m.isLiveEntry(hash, entry) {
		old := entry.Outbound.Swap(nil)
		if old != nil {
			closeOutbound(*old)
		}
	}
}

// RemoveNodeOutbound clears a node's outbound reference.
// Accepts the entry directly because the node may already be deleted from the pool
// (RemoveNodeFromSub deletes before firing onNodeRemoved callback).
func (m *OutboundManager) RemoveNodeOutbound(entry *node.NodeEntry) {
	if entry == nil {
		return
	}
	old := entry.Outbound.Swap(nil)
	if old != nil {
		closeOutbound(*old)
	}
}

// WarmupAll iterates all nodes in the pool and ensures each has an outbound.
// Called once after bootstrap to avoid ErrOutboundNotReady on restart.
func (m *OutboundManager) WarmupAll() {
	m.pool.RangeNodes(func(h node.Hash, _ *node.NodeEntry) bool {
		m.EnsureNodeOutbound(h)
		return true
	})
}

// Fetch executes HTTP request using the node's outbound.
// Returns ErrOutboundNotReady if the node's outbound is not yet initialized.
// ctx controls timeout/cancellation.
func (m *OutboundManager) Fetch(ctx context.Context, hash node.Hash, url string) ([]byte, time.Duration, error) {
	return m.FetchWithUserAgent(ctx, hash, url, "")
}

// FetchWithUserAgent executes HTTP request using the node's outbound and
// applies the given User-Agent if non-empty.
func (m *OutboundManager) FetchWithUserAgent(
	ctx context.Context,
	hash node.Hash,
	url string,
	userAgent string,
) ([]byte, time.Duration, error) {
	return m.FetchWithOptions(ctx, hash, url, netutil.OutboundHTTPOptions{
		RequireStatusOK: true,
		UserAgent:       userAgent,
	})
}

// FetchWithOptions keeps resource size limits consistent across direct and
// proxy-backed download attempts.
func (m *OutboundManager) FetchWithOptions(ctx context.Context, hash node.Hash, url string, opts netutil.OutboundHTTPOptions) ([]byte, time.Duration, error) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, 0, errors.New("node not found")
	}
	outboundPtr := entry.Outbound.Load() // *adapter.Outbound
	if outboundPtr == nil {
		return nil, 0, ErrOutboundNotReady
	}
	return netutil.HTTPGetViaOutbound(ctx, *outboundPtr, url, opts)
}
