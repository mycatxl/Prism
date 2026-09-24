package service

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/intel"
	"prism/internal/node"
)

// ------------------------------------------------------------------
// WP10 §4: the assessment view of a node
// ------------------------------------------------------------------
//
// Everything below reads the in-memory intel projection. The only database read
// is the batched node_egress lookup of FillNodeIntelEgress, which is called by
// the admin API handlers only: the proxy/routing request path keeps reading the
// projection alone (R3).

// Node assessment states reported in NodeIntel.State.
//
// IntelStateUnassessed means the projection has no row for the node's egress IP:
// every assessment field is then an explicit empty value (purity_score null,
// band unknown, confidence none, verdict pending, empty flags), never a
// fabricated score.
const (
	IntelStateUnassessed  = "unassessed"
	IntelStateValid       = "valid"
	IntelStatePending     = "pending"
	IntelStateUnsupported = "unsupported"
	IntelStateStale       = "stale"
)

// maxNodeIntelChecks bounds the per-node check map of the node list: the first
// maxNodeIntelChecks check ids in lexicographic order are reported and the rest
// are omitted (over-limit behaviour of WP10 §4).
const maxNodeIntelChecks = 32

// NodeIntel is the WP10 §4 per-node intel object.
type NodeIntel struct {
	State       string            `json:"state"`
	EgressIPv4  string            `json:"egress_ipv4"`
	EgressIPv6  string            `json:"egress_ipv6"`
	Colo        string            `json:"colo"`
	ASN         int               `json:"asn"`
	ASOrg       string            `json:"as_org"`
	Country     string            `json:"country"`
	City        string            `json:"city"`
	IPType      string            `json:"ip_type"`
	Native      *bool             `json:"native"`
	PurityScore *int              `json:"purity_score"`
	PurityBand  string            `json:"purity_band"`
	Confidence  string            `json:"confidence"`
	Verdict     string            `json:"verdict"`
	Flags       []string          `json:"flags"`
	Checks      map[string]string `json:"checks"`
	AssessedAt  string            `json:"assessed_at"`
	// AssessedAtNs is the assessment timestamp used for sorting (sort_by=
	// assessed_at). The wire shape carries the RFC3339 form above instead.
	AssessedAtNs int64 `json:"-"`
}

// NodeCheckFilter is one `check=<id>:<outcome>` node list filter (WP10 §4).
type NodeCheckFilter struct {
	ID      string
	Outcome string
}

// intelProjection returns the in-memory assessment projection, or nil when the
// intel subsystem is not wired into this build.
func (s *ControlPlaneService) intelProjection() *intel.Snapshot {
	if s == nil || s.Intel == nil {
		return nil
	}
	return s.Intel.Snapshot()
}

// nodeIntel resolves the intel view of one node from the projection, plus the
// legacy quality summary for the two filters that predate WP10 (see
// nodeIntelMatchesFilters).
//
// egress_ipv4, egress_ipv6 and colo come from node_egress and stay empty here:
// the list path fills them for a whole page at once with FillNodeIntelEgress.
func (s *ControlPlaneService) nodeIntel(entry *node.NodeEntry, now time.Time) NodeIntel {
	out := NodeIntel{
		State:      IntelStateUnassessed,
		IPType:     intel.IPTypeName(intel.IPTypeUnknown),
		PurityBand: intel.BandName(intel.BandUnknown),
		Confidence: intel.ConfidenceName(intel.ConfidenceNone),
		Verdict:    intel.VerdictName(intel.VerdictPending),
		Flags:      []string{},
		Checks:     map[string]string{},
	}
	if entry == nil {
		return out
	}
	ip := entry.GetEgressIP()
	if !ip.IsValid() {
		return out
	}
	snap := s.intelProjection()
	if snap == nil {
		return out
	}
	lite, ok := snap.Assessment(ip)
	if !ok {
		return out
	}

	out.State = intelState(lite, now)
	out.IPType = intel.IPTypeName(lite.IPType)
	out.Native = lite.NativeValue()
	out.PurityScore = lite.ScoreValue()
	out.PurityBand = intel.BandName(lite.Band)
	out.Confidence = intel.ConfidenceName(lite.Confidence)
	out.Verdict = intel.VerdictName(lite.Verdict)
	out.Flags = intel.FlagNames(lite.Flags)
	out.ASN = lite.ASNValue()
	out.ASOrg = lite.ASOrg
	out.Country = lite.Country
	out.City = lite.City
	if lite.ComputedAt > 0 {
		out.AssessedAtNs = lite.ComputedAt
		out.AssessedAt = time.Unix(0, lite.ComputedAt).UTC().Format(time.RFC3339Nano)
	}
	out.Checks = s.nodeIntelChecks(entry.Hash, now)
	return out
}

// intelState names the projection state at now. An assessment whose valid_until
// has passed (or is unknown) is stale, which is the fail-closed answer: it can
func intelState(lite intel.AssessmentLite, now time.Time) string {
	if !lite.Valid(now.UnixNano()) {
		return IntelStateStale
	}
	switch lite.State {
	case intel.StateValid:
		return IntelStateValid
	case intel.StateUnsupported:
		return IntelStateUnsupported
	default:
		return IntelStatePending
	}
}

// nodeIntelChecks projects the node's check results. Expired results are never
// reported and never match a filter: an outdated outcome is not an answer.
func (s *ControlPlaneService) nodeIntelChecks(hash node.Hash, now time.Time) map[string]string {
	out := map[string]string{}
	snap := s.intelProjection()
	if snap == nil {
		return out
	}
	outcomes := snap.NodeCheckOutcomes(hash.Hex())
	if len(outcomes) == 0 {
		return out
	}
	ids := make([]string, 0, len(outcomes))
	for id := range outcomes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		outcome := outcomes[id]
		if !outcome.Valid(now.UnixNano()) {
			continue
		}
		out[id] = intel.OutcomeName(outcome.Outcome)
		if len(out) >= maxNodeIntelChecks {
			break
		}
	}
	return out
}

// FillNodeIntelEgress adds the node_egress display facts (egress IPv4/IPv6 and
// the Cloudflare colo) to a page of summaries.
//
// It is one batched read for the whole page, never one query per node (§4), and
// it is best effort: without an intel store, or on a store error, the three
// egress fields simply stay empty. The assessment itself is never read from the
// database here.
func (s *ControlPlaneService) FillNodeIntelEgress(ctx context.Context, nodes []NodeSummary) {
	if s == nil || s.Intel == nil || len(nodes) == 0 {
		return
	}
	if s.Intel.Store() == nil {
		return
	}
	hashes := make([]string, 0, len(nodes))
	for _, summary := range nodes {
		if summary.NodeHash != "" {
			hashes = append(hashes, summary.NodeHash)
		}
	}
	if len(hashes) == 0 {
		return
	}
	rows, err := s.Intel.Store().NodeEgressByHashes(ctx, hashes)
	if err != nil || len(rows) == 0 {
		return
	}
	for i := range nodes {
		row, ok := rows[nodes[i].NodeHash]
		if !ok {
			continue
		}
		nodes[i].Intel.EgressIPv4 = row.IPv4
		nodes[i].Intel.EgressIPv6 = row.IPv6
		nodes[i].Intel.Colo = row.Colo
	}
}

// ------------------------------------------------------------------
// WP10 §4 node list filters
// ------------------------------------------------------------------

// hasIntelFilters reports whether any §4 intel filter is set.
func hasIntelFilters(filters NodeFilters) bool {
	return filters.PurityMin != nil ||
		filters.PurityMax != nil ||
		len(filters.Verdicts) > 0 ||
		filters.ConfidenceMin != nil ||
		filters.Native != nil ||
		filters.ASN != nil ||
		filters.Country != nil ||
		len(filters.Checks) > 0 ||
		filters.IPType != nil ||
		filters.PurityBand != nil
}

// nodeIntelMatchesFilters applies the §4 intel filters to one node's resolved
// intel view.
//
// Every one of them requires an assessment: a node that has none (state
// "unassessed") never matches, because an unknown value cannot satisfy a filter.
// That is the same fail-closed rule the platform admission uses. ip_type and
// purity_band match the projected names; purity_band=review keeps its pre-WP10
// meaning and selects the review/conflicting verdicts.
func (s *ControlPlaneService) nodeIntelMatchesFilters(intelView NodeIntel, filters NodeFilters) bool {
	if intelView.State == IntelStateUnassessed {
		return false
	}
	if filters.IPType != nil && intelView.IPType != *filters.IPType {
		return false
	}
	if filters.PurityBand != nil {
		want := *filters.PurityBand
		if want == "review" {
			if intelView.Verdict != intel.VerdictName(intel.VerdictReview) &&
				intelView.Verdict != intel.VerdictName(intel.VerdictConflicting) {
				return false
			}
		} else if intelView.PurityBand != want {
			return false
		}
	}
	if filters.PurityMin != nil && (intelView.PurityScore == nil || *intelView.PurityScore < *filters.PurityMin) {
		return false
	}
	if filters.PurityMax != nil && (intelView.PurityScore == nil || *intelView.PurityScore > *filters.PurityMax) {
		return false
	}
	if len(filters.Verdicts) > 0 && !intelFilterContains(filters.Verdicts, intelView.Verdict) {
		return false
	}
	if filters.ConfidenceMin != nil &&
		intelConfidenceRank(intelView.Confidence) < intelConfidenceRank(*filters.ConfidenceMin) {
		return false
	}
	if filters.Native != nil && (intelView.Native == nil || *intelView.Native != *filters.Native) {
		return false
	}
	if filters.ASN != nil && intelView.ASN != *filters.ASN {
		return false
	}
	if filters.Country != nil && !strings.EqualFold(intelView.Country, *filters.Country) {
		return false
	}
	for _, check := range filters.Checks {
		outcome, ok := intelView.Checks[check.ID]
		if !ok || outcome != check.Outcome {
			return false
		}
	}
	return true
}

// intelConfidenceRank orders the §1.2 confidence values; it mirrors the ranking
// the platform admission uses.
func intelConfidenceRank(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func intelFilterContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// intelFilterEgressIP exposes the egress IP a node's assessment is keyed by. It
// is only used by tests and by the response builder.
func intelFilterEgressIP(entry *node.NodeEntry) netip.Addr {
	if entry == nil {
		return netip.Addr{}
	}
	return entry.GetEgressIP()
}
