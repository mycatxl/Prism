package platform

import (
	"net/netip"
	"regexp"
	"sync"
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
	view   *RoutableView
	viewMu sync.Mutex

	// qualitySnapshot is the read-only intel projection the quality admission
	// path reads (WP10 §2). It is nil until the intel service is wired.
	qualitySnapshot QualitySnapshotReader
}

// NewPlatform creates a Platform with an empty routable view.
// The regex slice is treated as MUST rules for compatibility with internal callers.
func NewPlatform(id, name string, regexFilters []*regexp.Regexp, regionFilters []string) *Platform {
	return NewPlatformWithTagFilter(id, name, node.TagFilter{Must: regexFilters}, regionFilters)
}

// NewPlatformWithTagFilter creates a Platform with compiled line-oriented tag rules.
func NewPlatformWithTagFilter(id, name string, regexFilters node.TagFilter, regionFilters []string) *Platform {
	return &Platform{
		ID:            id,
		Name:          name,
		RegexFilters:  regexFilters,
		RegionFilters: regionFilters,
		view:          NewRoutableView(),
	}
}

// View returns the platform's routable view as a read-only interface.
// External callers cannot Add/Remove/Clear — only FullRebuild and NotifyDirty can mutate.
func (p *Platform) View() ReadOnlyView {
	return p.view
}

// FullRebuild clears the routable view and re-evaluates all nodes from the pool.
// Acquires viewMu — any concurrent NotifyDirty calls block until rebuild completes.
func (p *Platform) FullRebuild(
	poolRange PoolRangeFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	p.view.Clear()
	poolRange(func(h node.Hash, entry *node.NodeEntry) bool {
		if p.evaluateNode(entry, subLookup, geoLookup, qualityLookup) {
			p.view.Add(h)
		}
		return true
	})
}

// NotifyDirty re-evaluates a single node and adds/removes it from the view.
// Acquires viewMu — serialized with FullRebuild.
func (p *Platform) NotifyDirty(
	h node.Hash,
	getEntry GetEntryFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	entry, ok := getEntry(h)
	if !ok {
		// Node was deleted from pool.
		p.view.Remove(h)
		return
	}

	if p.evaluateNode(entry, subLookup, geoLookup, qualityLookup) {
		p.view.Add(h)
	} else {
		p.view.Remove(h)
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
		p.qualitySnapshot,
		time.Now(),
	)
	return ok
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
func (p *Platform) SetQualitySnapshot(snap QualitySnapshotReader) {
	p.qualitySnapshot = snap
}
