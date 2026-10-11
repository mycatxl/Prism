package platform

import (
	"net/netip"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/model"
	"prism/internal/node"
)

// DefaultPlatformID is the well-known UUID of the built-in Default platform.
const DefaultPlatformID = "00000000-0000-0000-0000-000000000000"

// DefaultPlatformName is the built-in platform name.
const DefaultPlatformName = "Default"

// GeoLookupFunc resolves an IP address to a lowercase ISO country code.
type GeoLookupFunc func(netip.Addr) string

// PoolRangeFunc iterates all nodes in the global pool.
type PoolRangeFunc func(fn func(node.Hash, *node.NodeEntry) bool)

// GetEntryFunc retrieves a node entry from the global pool by hash.
type GetEntryFunc func(node.Hash) (*node.NodeEntry, bool)

// Platform represents a routing platform with its filtered routable view.
type Platform struct {
	ID   string
	Name string

	// Filter configuration. The criteria below are ANDed with each other and the
	// values inside one criterion are alternatives (see NodeCriteria). Region
	// filters hold lowercase ISO codes and support negation "!xx".
	RegexFilters  node.TagFilter
	RegionFilters []string
	// IPTypes, PurityBands, SubscriptionFilters and Protocols are the explicit
	// criteria the operator picks from the inventory (migration 000015).
	IPTypes             []string
	PurityBands         []string
	SubscriptionFilters []string
	Protocols           []string
	QualityPolicy       model.QualityPolicy

	// Other config fields.
	StickyTTLNs                      int64
	ReverseProxyMissAction           string
	ReverseProxyEmptyAccountBehavior string
	ReverseProxyFixedAccountHeader   string
	ReverseProxyFixedAccountHeaders  []string
	AllocationPolicy                 AllocationPolicy
	PassiveCircuitBreakerDisabled    bool
	ScheduledRotationIntervalNs      int64
	ScheduledRotationEnabled         bool
	// RotationAvoidPreviousIP makes the router avoid the account's previous
	// egress IP on the next rotation (WP10 §3).
	RotationAvoidPreviousIP bool
	// Routable view & its lock.
	// viewMu serializes both FullRebuild and NotifyDirty.
	//
	// view is an atomic pointer rather than a plain field because the readers
	// (View().RandomPick / Range / Contains in the data plane) deliberately do
	// not take viewMu: a rebuild that mutated the published view in place would
	// expose an empty or half-filled view to them, and RandomPick reporting
	// "nothing routable" is a 503 (audit 3.1 #9). FullRebuild therefore fills a
	// private view and publishes it in one atomic store, so a reader sees either
	// the previous complete generation or the next complete one. NotifyDirty
	// keeps mutating the published generation in place, which is safe because it
	// only ever adds or removes single entries.
	view   atomic.Pointer[RoutableView]
	viewMu sync.Mutex

	// qualitySnapshot is the read-only intel projection the quality admission
	// path reads (WP10 §2). It is nil until the intel service is wired.
	//
	// It is an atomic pointer rather than a plain interface field because the
	// writers and the readers do not share a lock: the pool re-installs the
	// projection (GlobalNodePool.instrumentQuality / SetQualitySnapshot /
	// RegisterPlatform) while a concurrent dirty event reads it under viewMu.
	// A plain interface value is two words, so that pair is a data race, and a
	// torn read would hand the admission path a mismatched type and data word.
	// A nil pointer is stored as a typed-nil-free zero: the reader treats it as
	// "no projection yet", exactly like the nil interface before.
	qualitySnapshot atomic.Pointer[qualitySnapshotBox]
}

// qualitySnapshotBox carries the reader behind the atomic pointer. A plain
// atomic.Pointer[QualitySnapshotReader] cannot be used: storing a nil interface
// through an interface-typed pointer would make the load non-nil, and the
// admission path must see "no projection" as a nil reader.
type qualitySnapshotBox struct {
	reader QualitySnapshotReader
}

// NewPlatform creates a Platform with an empty routable view.
// The regex slice is treated as MUST rules for compatibility with internal callers.
func NewPlatform(id, name string, regexFilters []*regexp.Regexp, regionFilters []string) *Platform {
	return NewPlatformWithTagFilter(id, name, node.TagFilter{Must: regexFilters}, regionFilters)
}

// NewPlatformWithTagFilter creates a Platform with compiled line-oriented tag rules.
func NewPlatformWithTagFilter(id, name string, regexFilters node.TagFilter, regionFilters []string) *Platform {
	p := &Platform{
		ID:            id,
		Name:          name,
		RegexFilters:  regexFilters,
		RegionFilters: regionFilters,
	}
	p.view.Store(NewRoutableView())
	return p
}

// View returns the platform's routable view as a read-only interface.
// External callers cannot Add/Remove — only FullRebuild and NotifyDirty can mutate.
//
// The load is atomic and lock-free on purpose: the data plane calls this on
// every request, and FullRebuild publishes whole views rather than mutating the
// published one (audit 3.1 #9).
func (p *Platform) View() ReadOnlyView {
	return p.view.Load()
}

// FullRebuild re-evaluates all nodes from the pool and publishes the resulting
// routable view in a single atomic store.
//
// Acquires viewMu — any concurrent NotifyDirty calls block until rebuild
// completes. The new view is filled privately and only then published, so a
// reader that does not hold viewMu (View().RandomPick / Range / Contains) sees
// either the previous complete view or the next complete one — never the empty
// or half-filled intermediate state (audit 3.1 #9).
func (p *Platform) FullRebuild(
	poolRange PoolRangeFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	next := NewRoutableView()
	poolRange(func(h node.Hash, entry *node.NodeEntry) bool {
		if p.evaluateNode(entry, subLookup, geoLookup, qualityLookup) {
			next.Add(h)
		}
		return true
	})
	p.view.Store(next)
}

// NotifyDirty re-evaluates a single node and adds/removes it from the published
// view. Acquires viewMu — serialized with FullRebuild.
func (p *Platform) NotifyDirty(
	h node.Hash,
	getEntry GetEntryFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	view := p.view.Load()
	entry, ok := getEntry(h)
	if !ok {
		// Node was deleted from pool.
		view.Remove(h)
		return
	}

	if p.evaluateNode(entry, subLookup, geoLookup, qualityLookup) {
		view.Add(h)
	} else {
		view.Remove(h)
	}
}

// evaluateNode checks all filter conditions for platform routability.
//
// The decision itself lives in MatchNodeCriteria: the routable-view rebuild and
// the API live preview must never disagree about which nodes a platform loads.
func (p *Platform) evaluateNode(
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
) bool {
	ok, _ := MatchNodeCriteria(
		p.Criteria(),
		entry,
		subLookup,
		geoLookup,
		qualityLookup,
		p.qualityReader(),
		time.Now(),
	)
	return ok
}

// qualityReader returns the injected projection, or nil when none is installed.
func (p *Platform) qualityReader() QualitySnapshotReader {
	if box := p.qualitySnapshot.Load(); box != nil {
		return box.reader
	}
	return nil
}

// MatchRegionFilter applies include/exclude region filters.
// Positive entries (xx) build an include set; negative entries (!xx) build an exclude set.
// Unknown regions never match when region filters are configured.
// Final result is: region known AND (include empty OR region in include) AND (region not in exclude).
func MatchRegionFilter(region string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	if region == "" {
		return false
	}

	included := false
	hasInclude := false

	for _, filter := range filters {
		if len(filter) > 0 && filter[0] == '!' {
			if region == filter[1:] {
				return false
			}
			continue
		}
		hasInclude = true
		if region == filter {
			included = true
		}
	}

	if hasInclude && !included {
		return false
	}
	return true
}

// GetID returns the platform ID.
func (p *Platform) GetID() string {
	return p.ID
}

// IsScheduledRotationEnabled returns whether scheduled rotation is enabled for this platform.
func (p *Platform) IsScheduledRotationEnabled() bool {
	return p.ScheduledRotationEnabled
}

// GetScheduledRotationInterval returns the configured rotation interval.
func (p *Platform) GetScheduledRotationInterval() time.Duration {
	return time.Duration(p.ScheduledRotationIntervalNs)
}

// SetQualitySnapshot injects the intel projection used by quality admission.
// It must be called before the first FullRebuild/NotifyDirty that uses a
// non-empty quality policy.
//
// The store is atomic so it is safe to call while another goroutine is inside a
// rebuild or a dirty event; see the field comment for why that pair is real.
func (p *Platform) SetQualitySnapshot(snap QualitySnapshotReader) {
	p.qualitySnapshot.Store(&qualitySnapshotBox{reader: snap})
}
