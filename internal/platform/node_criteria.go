package platform

import (
	"strings"
	"time"

	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
)

// Rejection reasons of MatchNodeCriteria. They name the criterion that rejected
// the node, so the live preview can report exactly why a node is missing from a
// platform's routable view.
const (
	ReasonNodeDisabled     = "NODE_DISABLED"
	ReasonNodeUnhealthy    = "NODE_UNHEALTHY"
	ReasonTagFilter        = "TAG_FILTER"
	ReasonEgressUnknown    = "EGRESS_UNKNOWN"
	ReasonRegionFilter     = "REGION_FILTER"
	ReasonSubscription     = "SUBSCRIPTION_FILTER"
	ReasonProtocol         = "PROTOCOL_FILTER"
	ReasonLatencyUnknown   = "LATENCY_UNKNOWN"
	ReasonIPTypeFilter     = "IP_TYPE_FILTER"
	ReasonPurityBandFilter = "PURITY_BAND_FILTER"
	ReasonQualityRejected  = "QUALITY_REJECTED"
	// bandReview is the legacy purity-band value that selects the review and
	// conflicting verdicts instead of a band.
	bandReview = "review"
)

// NodeCriteria is the node-selection contract of a platform.
//
// Semantics — this is the contract the API and the UI must describe:
//
//   - Every criterion the operator set must hold at the same time (AND). A node
//     is routable only when all of them pass.
//   - Inside one criterion the listed values are alternatives (OR): a node that
//     matches any single value of that criterion passes that criterion.
//   - An empty criterion restricts nothing ("any").
//
// TagRules is the one exception, because it is a legacy escape hatch: it keeps
// its pre-existing line-oriented regex semantics (plain rules are ORed, "*"
// rules are ANDed inside one "<subscription>/<tag>" candidate, "!" rules reject
// the node) so a platform persisted before the explicit criteria existed keeps
// filtering exactly as it did.
type NodeCriteria struct {
	// TagRules is the legacy compiled tag regex filter.
	TagRules node.TagFilter
	// Regions holds lowercase ISO 3166-1 alpha-2 egress region codes; an entry
	// prefixed with "!" excludes that region instead of selecting it.
	Regions []string
	// IPTypes holds network types as the quality model emits them
	// (residential, mobile, business, wireless, datacenter, non_residential).
	IPTypes []string
	// PurityBands holds the purity bands the quality model emits (excellent,
	// clean, fair, mixed, poor, unknown); "review" additionally selects the
	// review and conflicting verdicts, exactly like the node-list filter.
	PurityBands []string
	// SubscriptionIDs selects nodes that are referenced by any of these
	// subscriptions.
	SubscriptionIDs []string
	// Protocols holds node.NodeEntry.Protocol values (shadowsocks, vless, ...).
	Protocols []string
	// QualityPolicy is the WP10 §2 admission policy.
	QualityPolicy model.QualityPolicy
}

// Criteria returns the platform's current node-selection criteria.
func (p *Platform) Criteria() NodeCriteria {
	return NodeCriteria{
		TagRules:        p.RegexFilters,
		Regions:         p.RegionFilters,
		IPTypes:         p.IPTypes,
		PurityBands:     p.PurityBands,
		SubscriptionIDs: p.SubscriptionFilters,
		Protocols:       p.Protocols,
		QualityPolicy:   p.QualityPolicy,
	}
}

// SetNodeCriteria installs the operator-set criteria lists on a platform.
//
// It must run before the platform is published to the pool: the routable view is
// built from these fields, so a platform that is registered first and patched
// afterwards admits every node until the next rebuild.
func (p *Platform) SetNodeCriteria(c NodeCriteria) {
	p.IPTypes = append([]string(nil), c.IPTypes...)
	p.PurityBands = append([]string(nil), c.PurityBands...)
	p.SubscriptionFilters = append([]string(nil), c.SubscriptionIDs...)
	p.Protocols = append([]string(nil), c.Protocols...)
}

// MatchNodeCriteria reports whether entry is admitted by c. When it is not, the
// second return value names the criterion that rejected it (see the Reason*
// constants); the reason is empty when the node is admitted.
//
// The criteria are ANDed and the values inside one criterion are alternatives,
// which is the contract documented on NodeCriteria. This function is the single
// admission decision of the routable view: both the full rebuild and the
// dirty-node re-evaluation go through it, so a view and a count can never
// disagree about which nodes a platform loads.
//
// It applies the runtime health gates (disabled, unhealthy, unknown egress, no
// latency) on top of the operator-set criteria.
func MatchNodeCriteria(
	c NodeCriteria,
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
	snap QualitySnapshotReader,
	now time.Time,
) (bool, string) {
	if entry == nil {
		return false, ReasonNodeDisabled
	}
	// Disabled nodes are never routable.
	if entry.IsDisabledBySubscriptions(subLookup) {
		return false, ReasonNodeDisabled
	}
	// Healthy for routing (outbound ready + circuit not open).
	if !entry.IsHealthy() {
		return false, ReasonNodeUnhealthy
	}
	// The egress IP must be known: every remaining criterion is about it.
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return false, ReasonEgressUnknown
	}
	if ok, reason := matchSelection(c, entry, subLookup, geoLookup, qualityLookup, snap, now); !ok {
		return false, reason
	}
	// At least one latency record.
	if !entry.HasLatency() {
		return false, ReasonLatencyUnknown
	}
	return true, ""
}

// MatchNodeCriteriaForPreview evaluates the operator-set criteria of c without
// the runtime health gates, and reports the criterion that rejected the node.
//
// It exists for the WP10 §2.1 preview-filter endpoint, which explains which
// *criteria* exclude a node; a node that is merely unhealthy right now must
// still be counted under its criteria counter there. The interactive form
// preview uses MatchNodeCriteria instead, because its count claims to be what
// the platform would load.
func MatchNodeCriteriaForPreview(
	c NodeCriteria,
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
	snap QualitySnapshotReader,
	now time.Time,
) (bool, string) {
	return matchSelection(c, entry, subLookup, geoLookup, qualityLookup, snap, now)
}

// matchSelection evaluates the operator-set criteria themselves, without the
// health/egress/latency gates of the routable view. MatchNodeCriteria layers
// those gates on top; MatchNodeCriteriaForPreview uses this half alone.
func matchSelection(
	c NodeCriteria,
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
	qualityLookup QualityLookupFunc,
	snap QualitySnapshotReader,
	now time.Time,
) (bool, string) {
	if entry == nil {
		return false, ReasonNodeDisabled
	}
	// Legacy tag regex rules.
	if !entry.MatchTagFilter(c.TagRules, subLookup) {
		return false, ReasonTagFilter
	}
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return false, ReasonEgressUnknown
	}
	// Region: an unknown region never matches a configured region list, which is
	// MatchRegionFilter's documented behaviour.
	if len(c.Regions) > 0 {
		if !MatchRegionFilter(entry.GetRegion(geoLookup), c.Regions) {
			return false, ReasonRegionFilter
		}
	}
	// Subscription: at least one of the selected subscriptions must still hold
	// the node, with the same enabled-subscription rule the rest of the pool
	// uses.
	if len(c.SubscriptionIDs) > 0 && !referencedBySubscription(entry, c.SubscriptionIDs, subLookup) {
		return false, ReasonSubscription
	}
	// Protocol: compared case-insensitively against the unified protocol name.
	if len(c.Protocols) > 0 && !containsFold(c.Protocols, entry.Protocol) {
		return false, ReasonProtocol
	}
	// Network type and purity band read the intel projection. They fail closed:
	// a node without an assessment cannot satisfy either criterion, exactly like
	// the node-list filters (internal/service/control_plane_node_intel.go).
	if len(c.IPTypes) > 0 {
		lite, ok := assessmentOf(entry, snap)
		if !ok || !containsFold(c.IPTypes, intel.IPTypeName(lite.IPType)) {
			return false, ReasonIPTypeFilter
		}
	}
	if len(c.PurityBands) > 0 {
		lite, ok := assessmentOf(entry, snap)
		if !ok || !purityBandAllowed(c.PurityBands, lite) {
			return false, ReasonPurityBandFilter
		}
	}
	// Quality policy (when configured).
	if !c.QualityPolicy.IsEmpty() {
		switch {
		case snap != nil:
			if ok, reason := AdmitQuality(c.QualityPolicy, entry, snap, now); !ok {
				return false, qualityReason(reason)
			}
		case qualityLookup != nil:
			// Legacy quality.Summary path; it runs the same rule engine.
			summary := qualityLookup(egressIP)
			if !EvaluateQuality(c.QualityPolicy, summary, entry.GetLastEgressUpdate(), now) {
				return false, ReasonQualityRejected
			}
		default:
			// Fail closed: a non-empty policy that cannot be verified never
			// admits a node (WP10 §2, deviation X5 covers the empty policy).
			return false, ReasonQualityRejected
		}
	}
	return true, ""
}

// referencedBySubscription reports whether any of the wanted subscription ids
// still references the node through an enabled subscription.
func referencedBySubscription(entry *node.NodeEntry, wanted []string, subLookup node.SubLookupFunc) bool {
	if subLookup == nil {
		return false
	}
	for _, subID := range entry.SubscriptionIDs() {
		if !containsFold(wanted, subID) {
			continue
		}
		if _, enabled, _, ok := subLookup(subID, entry.Hash); ok && enabled {
			return true
		}
	}
	return false
}

// assessmentOf resolves the projected assessment of one node's egress IP.
func assessmentOf(entry *node.NodeEntry, snap QualitySnapshotReader) (intel.AssessmentLite, bool) {
	if snap == nil || entry == nil {
		return intel.AssessmentLite{}, false
	}
	ip := entry.GetEgressIP()
	if !ip.IsValid() {
		return intel.AssessmentLite{}, false
	}
	return snap.Assessment(ip)
}

// purityBandAllowed applies one purity band list. "review" is the legacy band
// name for the review and conflicting verdicts; the node list maps it the same
// way (internal/service/control_plane_node_intel.go:250), so one filter value
// selects the same nodes on both paths.
func purityBandAllowed(bands []string, lite intel.AssessmentLite) bool {
	band := intel.BandName(lite.Band)
	verdict := intel.VerdictName(lite.Verdict)
	for _, want := range bands {
		want = strings.ToLower(strings.TrimSpace(want))
		if want == bandReview {
			if verdict == intel.VerdictName(intel.VerdictReview) ||
				verdict == intel.VerdictName(intel.VerdictConflicting) {
				return true
			}
			continue
		}
		if strings.EqualFold(band, want) {
			return true
		}
	}
	return false
}

// containsFold reports whether values holds want, ignoring case and surrounding
// space. An empty want never matches: a criterion value must name something.
func containsFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// qualityReason maps an admission reason onto a preview exclusion key, so the
// preview counter of a quality rejection is the admission reason itself.
func qualityReason(reason string) string {
	if reason == "" {
		return ReasonQualityUnknown
	}
	return reason
}
