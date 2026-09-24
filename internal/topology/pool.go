// Package topology coordinates the subscription → node pool → platform view pipeline.
// It owns the GlobalNodePool, PlatformManager, and SubscriptionManager,
// breaking import cycles between the leaf packages (node, subscription, platform).
package topology

import (
	"encoding/json"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
)

// GlobalNodePool is the system's single source of truth for nodes.
// It uses xsync.Map for concurrent access and xsync.Compute for atomic
// AddNodeFromSub / RemoveNodeFromSub operations.
type GlobalNodePool struct {
	nodes *xsync.Map[node.Hash, *node.NodeEntry]

	// Platform references for dirty-notify.
	platMu         sync.RWMutex
	platformByID   map[string]*platform.Platform // id -> platform
	platformByName map[string]*platform.Platform // name -> platform

	// Subscription lookup — injected by SubscriptionManager.
	subLookup func(subID string) *subscription.Subscription

	// GeoIP lookup — injected at construction.
	geoLookup platform.GeoLookupFunc

	// Quality lookup — injected at construction.
	qualityLookup platform.QualityLookupFunc

	// qualitySnapshot is the read-only intel projection injected by the intel
	// service (WP10 §2). It is what the quality admission path reads.
	qualitySnapshot platform.QualitySnapshotReader

	// egressIndex maps an observed egress IP to the nodes that used it, so an
	// assessment change can notify exactly the affected nodes (WP10 §2).
	egressMu    sync.Mutex
	egressIndex map[netip.Addr]map[node.Hash]struct{}
	// Persistence callbacks (optional, nil in tests without persistence).
	onNodeAdded      func(hash node.Hash)                        // called after a new node is created
	onNodeRemoved    func(hash node.Hash, entry *node.NodeEntry) // called after a node is deleted from pool
	onSubNodeChanged func(subID string, hash node.Hash, added bool)
	// extraOnNodeAdded holds additional node-added observers registered with
	// AddOnNodeAdded. They run after onNodeAdded and never replace it (WP09
	// §3.6 uses the hook for the subscription intel auto-enqueue).
	extraOnNodeAdded []func(hash node.Hash)
	// Health callbacks (optional).
	onNodeDynamicChanged func(hash node.Hash)                // fired on circuit/failure/egress changes
	onNodeLatencyChanged func(hash node.Hash, domain string) // fired on latency upserts and evictions

	// Health config
	maxLatencyTableEntries int
	maxConsecutiveFailures func() int
	latencyDecayWindow     func() time.Duration
	latencyAuthorities     func() []string
	// logf is an optional diagnostic sink for probe-failure/recovery
	// transitions (installed with SetLogf). nil disables logging entirely.
	// It is only read from recorder paths, so it must be installed before
	// background workers start.
	logf func(format string, args ...any)
}

// PoolConfig configures the GlobalNodePool.
type PoolConfig struct {
	SubLookup              func(subID string) *subscription.Subscription
	GeoLookup              platform.GeoLookupFunc
	QualityLookup          platform.QualityLookupFunc
	QualitySnapshot        platform.QualitySnapshotReader
	OnNodeAdded            func(hash node.Hash)
	OnNodeRemoved          func(hash node.Hash, entry *node.NodeEntry)
	OnSubNodeChanged       func(subID string, hash node.Hash, added bool)
	OnNodeDynamicChanged   func(hash node.Hash)
	OnNodeLatencyChanged   func(hash node.Hash, domain string)
	MaxLatencyTableEntries int
	MaxConsecutiveFailures func() int
	LatencyDecayWindow     func() time.Duration
	LatencyAuthorities     func() []string
}

var (
	// ErrPlatformNotRegistered indicates the target platform ID is not registered.
	ErrPlatformNotRegistered = errors.New("platform not registered")
	// ErrPlatformNameConflict indicates another platform already uses the target name.
	ErrPlatformNameConflict = errors.New("platform name conflict")
)

// NewGlobalNodePool creates a new GlobalNodePool.
func NewGlobalNodePool(cfg PoolConfig) *GlobalNodePool {
	maxConsecutiveFailuresFn := cfg.MaxConsecutiveFailures
	if maxConsecutiveFailuresFn == nil {
		panic("topology: NewGlobalNodePool requires non-nil MaxConsecutiveFailures")
	}

	return &GlobalNodePool{
		nodes:                  xsync.NewMap[node.Hash, *node.NodeEntry](),
		subLookup:              cfg.SubLookup,
		geoLookup:              cfg.GeoLookup,
		qualityLookup:          cfg.QualityLookup,
		qualitySnapshot:        cfg.QualitySnapshot,
		egressIndex:            make(map[netip.Addr]map[node.Hash]struct{}),
		onNodeAdded:            cfg.OnNodeAdded,
		onNodeRemoved:          cfg.OnNodeRemoved,
		onSubNodeChanged:       cfg.OnSubNodeChanged,
		onNodeDynamicChanged:   cfg.OnNodeDynamicChanged,
		onNodeLatencyChanged:   cfg.OnNodeLatencyChanged,
		maxLatencyTableEntries: cfg.MaxLatencyTableEntries,
		maxConsecutiveFailures: maxConsecutiveFailuresFn,
		latencyDecayWindow:     cfg.LatencyDecayWindow,
		latencyAuthorities:     cfg.LatencyAuthorities,
		platformByID:           make(map[string]*platform.Platform),
		platformByName:         make(map[string]*platform.Platform),
	}
}

// AddNodeFromSub adds a node to the pool with the given subscription reference.
// Uses xsync.Compute for atomic load-or-create + ref-update.
// Idempotent: adding the same (hash, subID) pair multiple times is safe.
// After mutation, notifies all platforms to re-evaluate the node.
func (p *GlobalNodePool) AddNodeFromSub(hash node.Hash, rawOpts json.RawMessage, subID string) {
	isNew := false
	p.nodes.Compute(hash, func(entry *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded {
			createdAt := time.Now()
			entry = node.NewNodeEntry(hash, rawOpts, createdAt, p.maxLatencyTableEntries)
			// New subscription nodes start as circuit-open and must be proven healthy by probes.
			entry.CircuitOpenSince.Store(createdAt.UnixNano())
			isNew = true
		}
		entry.AddSubscriptionID(subID)
		return entry, xsync.UpdateOp
	})

	if isNew {
		if p.onNodeAdded != nil {
			p.onNodeAdded(hash)
		}
		for _, extra := range p.extraOnNodeAdded {
			extra(hash)
		}
	}
	if p.onSubNodeChanged != nil {
		p.onSubNodeChanged(subID, hash, true)
	}

	p.notifyAllPlatformsDirty(hash)
}

// RemoveNodeFromSub removes a subscription reference from a node.
// If the node has no remaining references, it is deleted from the pool.
// Uses xsync.Compute for atomic ref-update + conditional delete.
// Idempotent: removing a nonexistent (hash, subID) pair is safe.
func (p *GlobalNodePool) RemoveNodeFromSub(hash node.Hash, subID string) {
	wasDeleted := false
	var deletedEntry *node.NodeEntry // capture entry before map deletion
	p.nodes.Compute(hash, func(entry *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded {
			return entry, xsync.CancelOp // idempotent no-op
		}
		empty := entry.RemoveSubscriptionID(subID)
		if empty {
			wasDeleted = true
			deletedEntry = entry
			return nil, xsync.DeleteOp
		}
		return entry, xsync.UpdateOp
	})

	if p.onSubNodeChanged != nil {
		p.onSubNodeChanged(subID, hash, false)
	}
	if wasDeleted {
		p.removeEgressIndex(hash)
	}
	if wasDeleted && p.onNodeRemoved != nil {
		p.onNodeRemoved(hash, deletedEntry)
	}

	p.notifyAllPlatformsDirty(hash)
}

// GetEntry retrieves a node entry by hash.
func (p *GlobalNodePool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	return p.nodes.Load(hash)
}

// Range iterates all nodes in the pool.
func (p *GlobalNodePool) Range(fn func(node.Hash, *node.NodeEntry) bool) {
	p.nodes.Range(fn)
}

// Size returns the number of nodes in the pool.
func (p *GlobalNodePool) Size() int {
	return p.nodes.Size()
}

// LoadNodeFromBootstrap inserts a node during bootstrap recovery.
// No dirty-marks, no platform notifications.
func (p *GlobalNodePool) LoadNodeFromBootstrap(entry *node.NodeEntry) {
	p.nodes.Store(entry.Hash, entry)
}

// RegisterPlatform adds a platform to receive dirty notifications.
func (p *GlobalNodePool) RegisterPlatform(plat *platform.Platform) {
	plat.SetQualitySnapshot(p.qualitySnapshot)
	p.platMu.Lock()
	defer p.platMu.Unlock()
	// Check for existing ID to avoid duplicates.
	if _, exists := p.platformByID[plat.ID]; exists {
		return
	}
	p.platformByID[plat.ID] = plat
	if plat.Name != "" {
		p.platformByName[plat.Name] = plat
	}
}

// UnregisterPlatform removes a platform from dirty notifications.
func (p *GlobalNodePool) UnregisterPlatform(id string) {
	p.platMu.Lock()
	defer p.platMu.Unlock()
	plat, ok := p.platformByID[id]
	if !ok {
		return
	}
	delete(p.platformByID, id)
	if plat.Name != "" {
		// Only delete when name still points at this platform.
		if current, ok := p.platformByName[plat.Name]; ok && current == plat {
			delete(p.platformByName, plat.Name)
		}
	}
}

// ReplacePlatform atomically replaces an existing platform object by ID.
// It follows a copy-on-write update path: the caller builds a new Platform
// instance, this method rebuilds its routable view, then swaps map pointers
// under platMu in one critical section.
func (p *GlobalNodePool) ReplacePlatform(next *platform.Platform) error {
	if next == nil || next.ID == "" {
		return ErrPlatformNotRegistered
	}

	// Build the new platform's view before publish so readers never observe
	// an empty, not-yet-built view due only to replacement.
	p.instrumentQuality(next)
	p.RebuildPlatform(next)

	p.platMu.Lock()
	defer p.platMu.Unlock()

	current, ok := p.platformByID[next.ID]
	if !ok {
		return ErrPlatformNotRegistered
	}

	if next.Name != "" {
		if existingByName, exists := p.platformByName[next.Name]; exists && existingByName != current {
			return ErrPlatformNameConflict
		}
	}

	p.platformByID[next.ID] = next

	if current.Name != "" {
		if mapped, exists := p.platformByName[current.Name]; exists && mapped == current {
			delete(p.platformByName, current.Name)
		}
	}
	if next.Name != "" {
		p.platformByName[next.Name] = next
	}

	return nil
}

// SetQualitySnapshot injects the intel projection used by quality admission
// (WP10 §2) and propagates it to every registered platform and to platforms
// registered later.
func (p *GlobalNodePool) SetQualitySnapshot(snap platform.QualitySnapshotReader) {
	p.platMu.Lock()
	p.qualitySnapshot = snap
	platforms := make([]*platform.Platform, 0, len(p.platformByID))
	for _, plat := range p.platformByID {
		platforms = append(platforms, plat)
	}
	p.platMu.Unlock()
	for _, plat := range platforms {
		plat.SetQualitySnapshot(snap)
	}
}

// instrumentQuality wires the injected snapshot into one platform.
func (p *GlobalNodePool) instrumentQuality(plat *platform.Platform) {
	if plat == nil {
		return
	}
	p.platMu.RLock()
	snap := p.qualitySnapshot
	p.platMu.RUnlock()
	plat.SetQualitySnapshot(snap)
}

// GetPlatform retrieves a platform by ID.
func (p *GlobalNodePool) GetPlatform(id string) (*platform.Platform, bool) {
	p.platMu.RLock()
	defer p.platMu.RUnlock()
	plat, ok := p.platformByID[id]
	return plat, ok
}

// GetPlatformByName retrieves a platform by Name.
func (p *GlobalNodePool) GetPlatformByName(name string) (*platform.Platform, bool) {
	p.platMu.RLock()
	defer p.platMu.RUnlock()
	plat, ok := p.platformByName[name]
	return plat, ok
}

// RangePlatforms iterates all registered platforms.
func (p *GlobalNodePool) RangePlatforms(fn func(*platform.Platform) bool) {
	for _, plat := range p.platformSnapshot() {
		if !fn(plat) {
			return
		}
	}
}

func (p *GlobalNodePool) platformSnapshot() []*platform.Platform {
	p.platMu.RLock()
	defer p.platMu.RUnlock()

	platforms := make([]*platform.Platform, 0, len(p.platformByID))
	for _, plat := range p.platformByID {
		platforms = append(platforms, plat)
	}
	return platforms
}

// MakeSubLookup builds the SubLookupFunc closure for MatchRegexs / tag resolution.
func (p *GlobalNodePool) MakeSubLookup() node.SubLookupFunc {
	return func(subID string, hash node.Hash) (string, bool, []string, bool) {
		// Compatibility fallback for test wiring that omits SubLookup.
		// We cannot resolve subscription metadata, so treat the reference as
		// "present+enabled" without tags.
		if p.subLookup == nil {
			return "", true, nil, true
		}

		sub := p.subLookup(subID)
		if sub == nil {
			return "", false, nil, false
		}

		managed, ok := sub.ManagedNodes().LoadNode(hash)
		if !ok || managed.Evicted {
			return "", false, nil, false
		}
		tags := managed.Tags
		return sub.Name(), sub.Enabled(), tags, true
	}
}

// ResolveNodeDisplayTag resolves a node hash to its display tag for request logs.
// Rule:
//  1. Prefer enabled subscriptions: among enabled holders, choose earliest-created.
//  2. Within that subscription, choose lexicographically smallest tag.
//  3. If no enabled holder exists, fallback to all holders with the same rule.
//  4. Return "<SubscriptionName>/<Tag>".
//
// Returns empty string when resolution is not possible.
func (p *GlobalNodePool) ResolveNodeDisplayTag(hash node.Hash) string {
	if p.subLookup == nil {
		return ""
	}

	entry, ok := p.GetEntry(hash)
	if !ok || entry == nil {
		return ""
	}
	subIDs := entry.SubscriptionIDs()
	if len(subIDs) == 0 {
		return ""
	}

	pick := func(enabledOnly bool) (string, bool) {
		bestFound := false
		var bestCreatedAtNs int64
		var bestSubID string
		var bestSubName string
		var bestTag string

		for _, subID := range subIDs {
			sub := p.subLookup(subID)
			if sub == nil {
				continue
			}
			if enabledOnly && !sub.Enabled() {
				continue
			}

			managed, ok := sub.ManagedNodes().LoadNode(hash)
			if !ok || managed.Evicted {
				continue
			}
			tags := managed.Tags
			if len(tags) == 0 {
				continue
			}

			smallestTag := tags[0]
			for _, tag := range tags[1:] {
				if tag < smallestTag {
					smallestTag = tag
				}
			}

			createdAtNs := sub.CreatedAtNs
			if !bestFound ||
				createdAtNs < bestCreatedAtNs ||
				(createdAtNs == bestCreatedAtNs && subID < bestSubID) {
				bestFound = true
				bestCreatedAtNs = createdAtNs
				bestSubID = subID
				bestSubName = sub.Name()
				bestTag = smallestTag
			}
		}

		if !bestFound || bestSubName == "" || bestTag == "" {
			return "", false
		}
		return bestSubName + "/" + bestTag, true
	}

	if tag, ok := pick(true); ok {
		return tag
	}
	if tag, ok := pick(false); ok {
		return tag
	}
	return ""
}

// IsNodeDisabled reports whether a node is disabled by subscription state:
// all referencing subscriptions are disabled (or missing / not applicable).
func (p *GlobalNodePool) IsNodeDisabled(hash node.Hash) bool {
	entry, ok := p.GetEntry(hash)
	if !ok || entry == nil {
		return true
	}
	return entry.IsDisabledBySubscriptions(p.MakeSubLookup())
}

// MakeHealthyAndEnabledEvaluator builds a predicate for pool-context health
// aggregates: the node must not be disabled by subscription state and must
// satisfy the entry-local health checks.
func (p *GlobalNodePool) MakeHealthyAndEnabledEvaluator() func(entry *node.NodeEntry) bool {
	subLookup := p.MakeSubLookup()
	return func(entry *node.NodeEntry) bool {
		if entry == nil || entry.IsDisabledBySubscriptions(subLookup) {
			return false
		}
		return entry.IsHealthy()
	}
}

// notifyAllPlatformsDirty tells every registered platform to re-evaluate a node.
func (p *GlobalNodePool) notifyAllPlatformsDirty(hash node.Hash) {
	platforms := p.platformSnapshot()
	if len(platforms) == 0 {
		return
	}

	subLookup := p.MakeSubLookup()
	getEntry := func(h node.Hash) (*node.NodeEntry, bool) {
		return p.nodes.Load(h)
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(platforms) {
		workers = len(platforms)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, plat := range platforms {
		sem <- struct{}{}
		wg.Add(1)
		go func(plat *platform.Platform) {
			defer wg.Done()
			defer func() { <-sem }()
			plat.NotifyDirty(hash, getEntry, subLookup, p.geoLookup, p.qualityLookup)
		}(plat)
	}
	wg.Wait()
}

// RebuildAllPlatforms triggers a full rebuild on all registered platforms.
func (p *GlobalNodePool) RebuildAllPlatforms() {
	platforms := p.platformSnapshot()
	if len(platforms) == 0 {
		return
	}

	subLookup := p.MakeSubLookup()
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		p.nodes.Range(fn)
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(platforms) {
		workers = len(platforms)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, plat := range platforms {
		sem <- struct{}{}
		wg.Add(1)
		go func(plat *platform.Platform) {
			defer wg.Done()
			defer func() { <-sem }()
			plat.FullRebuild(poolRange, subLookup, p.geoLookup, p.qualityLookup)
		}(plat)
	}
	wg.Wait()
}

// RebuildPlatform triggers a full rebuild on a specific platform.
func (p *GlobalNodePool) RebuildPlatform(plat *platform.Platform) {
	subLookup := p.MakeSubLookup()
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		p.nodes.Range(fn)
	}
	plat.FullRebuild(poolRange, subLookup, p.geoLookup, p.qualityLookup)
}

// --- Health Management ---

// SetOnNodeAdded sets the callback fired when a new node is added.
// Must be called before any background workers are started.
func (p *GlobalNodePool) SetOnNodeAdded(fn func(hash node.Hash)) {
	p.onNodeAdded = fn
}

// AddOnNodeAdded registers an additional node-added observer. Unlike
// SetOnNodeAdded it never replaces the upstream wiring; every registered
// observer runs after it, in registration order. It must be called before the
// background workers start (WP09 §3.6 subscription intel auto-enqueue).
func (p *GlobalNodePool) AddOnNodeAdded(fn func(hash node.Hash)) {
	if fn == nil {
		return
	}
	p.extraOnNodeAdded = append(p.extraOnNodeAdded, fn)
}

// SetOnNodeRemoved sets the callback fired when a node is removed from the pool.
// Must be called before any background workers are started.
func (p *GlobalNodePool) SetOnNodeRemoved(fn func(hash node.Hash, entry *node.NodeEntry)) {
	p.onNodeRemoved = fn
}

// SetLogf installs an optional diagnostic logger for probe-failure class
// transitions and recoveries. A nil logger disables logging (no-op). It must
// be called before background workers start.
func (p *GlobalNodePool) SetLogf(logf func(format string, args ...any)) {
	p.logf = logf
}

// NotifyNodeDirty triggers platform re-evaluation for a single node.
// Used by OutboundManager after outbound creation to update routable views.
func (p *GlobalNodePool) NotifyNodeDirty(hash node.Hash) {
	p.notifyAllPlatformsDirty(hash)
}

// RangeNodes iterates over all nodes in the pool.
// The callback receives each node's hash and entry. Return false to stop.
func (p *GlobalNodePool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {
	p.nodes.Range(fn)
}

// RecordResult records a probe or passive health-check result that carries no
// classified reason. It is a thin wrapper over RecordOutcome so every existing
// call site keeps the original semantics (failure count + circuit breaker).
// With no class/detail the pool must not invent a probe-failure reason, so the
// wrapper never attaches one.
func (p *GlobalNodePool) RecordResult(hash node.Hash, success bool) {
	p.RecordOutcome(hash, success, node.ProbeErrorNone, "")
}

// RecordOutcome records a probe or passive health-check result together with
// the classified reason of an active-probe failure.
//
// Health semantics are identical to the original RecordResult:
//   - success resets FailureCount and clears the circuit-breaker;
//   - failure increments FailureCount and opens the circuit-breaker if the
//     threshold is reached;
//   - platforms are notified only when circuit state changes (open/recover);
//   - OnNodeDynamicChanged fires only when dynamic fields actually change.
//
// Probe-failure bookkeeping (a separate axis from Entry.LastError, which means
// the node could not be built at all):
//   - success clears a previously recorded probe failure;
//   - failure stores class/detail on the entry; a None class or an empty
//     detail clears the record (see node.NodeEntry.SetProbeFailure);
//   - the optional logf hook (SetLogf) is called ONLY on a failure-class
//     change and on recovery, never on every failure: probes run continuously
//     and logging each one would flood the log.
func (p *GlobalNodePool) RecordOutcome(hash node.Hash, success bool, class node.ProbeErrorClass, detail string) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	dynamicChanged := false
	circuitStateChanged := false

	if success {
		if entry.FailureCount.Swap(0) != 0 {
			dynamicChanged = true
		}
		if entry.CircuitOpenSince.Swap(0) != 0 {
			dynamicChanged = true
			circuitStateChanged = true
		}
		if prevClass, _, _ := entry.GetProbeFailure(); prevClass != node.ProbeErrorNone {
			entry.ClearProbeFailure()
			if p.logf != nil {
				p.logf("[topology] node probe recovered: hash=%s", hash.Hex())
			}
		}
	} else {
		newCount := entry.FailureCount.Add(1)
		dynamicChanged = true
		maxConsecutiveFailures := p.currentMaxConsecutiveFailures()
		if maxConsecutiveFailures > 0 && int(newCount) >= maxConsecutiveFailures {
			// Open circuit if not already open.
			if entry.CircuitOpenSince.CompareAndSwap(0, time.Now().UnixNano()) {
				circuitStateChanged = true
			}
		}
		prevClass, _, _ := entry.GetProbeFailure()
		entry.SetProbeFailure(class, detail, time.Now())
		if class != node.ProbeErrorNone && class != prevClass && p.logf != nil {
			p.logf("[topology] node probe failed: hash=%s class=%s detail=%s", hash.Hex(), class, detail)
		}
	}

	if circuitStateChanged {
		p.notifyAllPlatformsDirty(hash)
	}
	if dynamicChanged && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

// RecordPassiveResult records health feedback from user proxy traffic.
// Failed passive traffic is ignored when the originating platform disables
// passive circuit breaking; successes still count as positive health feedback.
func (p *GlobalNodePool) RecordPassiveResult(platformID string, hash node.Hash, success bool) {
	if success || !p.passiveCircuitBreakerDisabled(platformID) {
		p.RecordResult(hash, success)
	}
}

func (p *GlobalNodePool) passiveCircuitBreakerDisabled(platformID string) bool {
	if platformID == "" {
		return false
	}

	p.platMu.RLock()
	defer p.platMu.RUnlock()

	plat, ok := p.platformByID[platformID]
	if !ok || plat == nil {
		return false
	}
	return plat.PassiveCircuitBreakerDisabled
}

func (p *GlobalNodePool) currentMaxConsecutiveFailures() int {
	return p.maxConsecutiveFailures()
}

// RecordLatency records a latency probe attempt for the given node and raw target.
// rawTarget is normalized through ExtractDomain (eTLD+1). latency may be nil,
// which means "attempt only" without latency sample writeback.
func (p *GlobalNodePool) RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	domain := netutil.ExtractDomain(rawTarget)
	isAuthority := p.isAuthorityDomain(domain)
	nowNs := time.Now().UnixNano()
	entry.LastLatencyProbeAttempt.Store(nowNs)
	if isAuthority {
		entry.LastAuthorityLatencyProbeAttempt.Store(nowNs)
	}
	if p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}

	if latency == nil || *latency <= 0 || entry.LatencyTable == nil {
		return
	}

	var decayWindow time.Duration
	if p.latencyDecayWindow != nil {
		decayWindow = p.latencyDecayWindow()
	}
	if decayWindow <= 0 {
		decayWindow = 30 * time.Second // default
	}

	wasEmpty, evictedDomain, evicted := entry.LatencyTable.UpdateClassified(domain, *latency, decayWindow, isAuthority)

	// If the table transitioned from empty to non-empty, the node might
	// now satisfy the HasLatency filter — notify platforms.
	if wasEmpty {
		p.notifyAllPlatformsDirty(hash)
	}

	if p.onNodeLatencyChanged != nil {
		p.onNodeLatencyChanged(hash, domain)
		if evicted {
			p.onNodeLatencyChanged(hash, evictedDomain)
		}
	}
}

// UpdateNodeEgressIP records an egress probe attempt and optionally updates
// the node's egress IP and explicit region metadata.
// Region update rules:
//   - ip=nil,  loc=nil: keep both IP and region unchanged.
//   - ip!=nil, loc=nil: keep region if IP unchanged; clear region if IP changed.
//   - loc!=nil: set region to loc (normalized).
func (p *GlobalNodePool) UpdateNodeEgressIP(hash node.Hash, ip *netip.Addr, loc *string) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}

	nowNs := time.Now().UnixNano()
	entry.LastEgressUpdateAttempt.Store(nowNs)

	oldIP := entry.GetEgressIP()
	oldRegion := entry.GetEgressRegion()
	ipChanged := false

	if ip != nil {
		// Record successful egress-IP sample timestamp.
		entry.LastEgressUpdate.Store(nowNs)
		if oldIP != *ip {
			entry.SetEgressIP(*ip)
			ipChanged = true
			p.indexEgressIP(hash, oldIP, *ip)
		}
	}

	regionChanged := false
	switch {
	case loc != nil:
		entry.SetEgressRegion(*loc)
		regionChanged = oldRegion != entry.GetEgressRegion()
	case ip == nil:
		// Attempt-only update: keep region as-is.
	case !ipChanged:
		// IP unchanged and no explicit region: keep existing region.
	default:
		// IP changed without explicit region: clear stale region metadata.
		if oldRegion != "" {
			entry.SetEgressRegion("")
			regionChanged = true
		}
	}

	if ipChanged || regionChanged {
		p.notifyAllPlatformsDirty(hash)
	}
	if p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

// indexEgressIP moves one node between the egress-IP buckets.
func (p *GlobalNodePool) indexEgressIP(hash node.Hash, oldIP, newIP netip.Addr) {
	p.egressMu.Lock()
	defer p.egressMu.Unlock()
	if p.egressIndex == nil {
		p.egressIndex = make(map[netip.Addr]map[node.Hash]struct{})
	}
	if oldIP.IsValid() {
		if bucket, ok := p.egressIndex[oldIP.Unmap()]; ok {
			delete(bucket, hash)
			if len(bucket) == 0 {
				delete(p.egressIndex, oldIP.Unmap())
			}
		}
	}
	if newIP.IsValid() {
		key := newIP.Unmap()
		bucket, ok := p.egressIndex[key]
		if !ok {
			bucket = make(map[node.Hash]struct{})
			p.egressIndex[key] = bucket
		}
		bucket[hash] = struct{}{}
	}
}

// removeEgressIndex drops every bucket entry of one node.
func (p *GlobalNodePool) removeEgressIndex(hash node.Hash) {
	p.egressMu.Lock()
	defer p.egressMu.Unlock()
	for ip, bucket := range p.egressIndex {
		delete(bucket, hash)
		if len(bucket) == 0 {
			delete(p.egressIndex, ip)
		}
	}
}

// EgressIPOwners returns the nodes whose last observed egress IP equals ip.
func (p *GlobalNodePool) EgressIPOwners(ip netip.Addr) []node.Hash {
	if !ip.IsValid() {
		return nil
	}
	p.egressMu.Lock()
	defer p.egressMu.Unlock()
	bucket := p.egressIndex[ip.Unmap()]
	out := make([]node.Hash, 0, len(bucket))
	for hash := range bucket {
		out = append(out, hash)
	}
	return out
}

// NotifyEgressIPDirty re-evaluates every node that used an egress IP, which is
// what the intel pipeline calls after a new assessment was stored (WP10 §2).
// It returns the number of nodes that were re-evaluated.
func (p *GlobalNodePool) NotifyEgressIPDirty(ip netip.Addr) int {
	hashes := p.EgressIPOwners(ip)
	for _, hash := range hashes {
		p.notifyAllPlatformsDirty(hash)
	}
	return len(hashes)
}

func (p *GlobalNodePool) isAuthorityDomain(domain string) bool {
	if domain == "" || p.latencyAuthorities == nil {
		return false
	}
	authorities := p.latencyAuthorities()
	for _, authority := range authorities {
		if strings.EqualFold(strings.TrimSpace(authority), domain) {
			return true
		}
	}
	return false
}
