package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/node"
	"prism/internal/probe"
	"prism/internal/quality"
	"prism/internal/subscription"
)

// ------------------------------------------------------------------
// Nodes
// ------------------------------------------------------------------

// NodeFilters holds query filters for listing nodes.
type NodeFilters struct {
	IPType         *string
	QualityState   *string
	RiskGrade      *string
	PurityBand     *string
	Protocol       *string
	PlatformID     *string
	SubscriptionID *string
	Enabled        *bool
	Region         *string
	CircuitOpen    *bool
	HasOutbound    *bool
	EgressIP       *string
	ProbedSince    *time.Time
	TagKeyword     *string

	// intel filters. They are evaluated against the in-memory
	// assessment projection (see nodeIntelMatchesFilters): ip_type and
	// purity_band read the projected names, and a node without an assessment
	// never satisfies a purity/verdict/confidence/native/asn/country/check
	// filter. Verdicts holds the accepted verdict names and Checks the
	// `check=<id>:<outcome>` pairs; the API layer bounds both.
	PurityMin     *int
	PurityMax     *int
	Verdicts      []string
	ConfidenceMin *string
	Native        *bool
	ASN           *int
	Country       *string
	Checks        []NodeCheckFilter
}

// ListNodes returns nodes from the pool with optional filters.
//
// This compatibility method is intentionally materializing; HTTP list handlers
// should use RangeFilteredNodes so large pools do not become one response-sized
// intermediate slice.
func (s *ControlPlaneService) ListNodes(filters NodeFilters) ([]NodeSummary, error) {
	result := make([]NodeSummary, 0)
	_, err := s.RangeFilteredNodes(filters, func(_ node.Hash, summary NodeSummary) bool {
		result = append(result, summary)
		return true
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// NodeListStats are aggregate facts accumulated while traversing the pool.
// Distinct-IP sets have a fixed cap and report when their counts are lower
// bounds, avoiding memory proportional to pool size.
type NodeListStats struct {
	Scanned                         int
	Total                           int
	UniqueEgressIPs                 int
	UniqueHealthyEgressIPs          int
	UniqueEgressIPsTruncated        bool
	UniqueHealthyEgressIPsTruncated bool
}

const nodeListUniqueIPLimit = 65_536

type boundedStringSet struct {
	values    map[string]struct{}
	limit     int
	truncated bool
}

func newBoundedStringSet(limit int) boundedStringSet {
	return boundedStringSet{values: make(map[string]struct{}, min(limit, 256)), limit: limit}
}

func (s *boundedStringSet) Add(value string) {
	if value == "" {
		return
	}
	if _, ok := s.values[value]; ok {
		return
	}
	if len(s.values) >= s.limit {
		s.truncated = true
		return
	}
	s.values[value] = struct{}{}
}

// RangeFilteredNodes walks the selected node set and invokes visit once per
// match. It keeps no candidate slice. Returning false from visit stops traversal.
func (s *ControlPlaneService) RangeFilteredNodes(
	filters NodeFilters,
	visit func(node.Hash, NodeSummary) bool,
) (NodeListStats, error) {
	return s.RangeFilteredNodesContext(context.Background(), filters, visit)
}

// RangeFilteredNodesContext is the cancelable summary-compatible form.
func (s *ControlPlaneService) RangeFilteredNodesContext(
	ctx context.Context,
	filters NodeFilters,
	visit func(node.Hash, NodeSummary) bool,
) (NodeListStats, error) {
	return s.RangeFilteredNodeEntriesContext(ctx, filters, func(hash node.Hash, entry *node.NodeEntry) bool {
		return visit(hash, s.nodeEntryToSummary(hash, entry))
	})
}

// RangeFilteredNodeEntriesContext scans without allocating full summaries. This
// is the list path's primitive: callers retain only selected rows and build
// response summaries after the bounded selection is complete.
func (s *ControlPlaneService) RangeFilteredNodeEntriesContext(
	ctx context.Context,
	filters NodeFilters,
	visit func(node.Hash, *node.NodeEntry) bool,
) (NodeListStats, error) {
	var stats NodeListStats
	if s == nil || s.Pool == nil {
		return stats, internal("list nodes", fmt.Errorf("service not initialized"))
	}
	if visit == nil {
		return stats, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	subLookup := s.Pool.MakeSubLookup()
	var platformView interface {
		Contains(node.Hash) bool
		Size() int
		Range(func(node.Hash) bool)
	}
	if filters.PlatformID != nil {
		plat, ok := s.Pool.GetPlatform(*filters.PlatformID)
		if !ok {
			return stats, notFound("platform not found")
		}
		platformView = plat.View()
	}

	var managedNodes *subscription.ManagedNodes
	if filters.SubscriptionID != nil {
		if s.SubMgr == nil {
			return stats, notFound("subscription not found")
		}
		sub := s.SubMgr.Lookup(*filters.SubscriptionID)
		if sub == nil {
			return stats, notFound("subscription not found")
		}
		managedNodes = sub.ManagedNodes()
	}

	allIPs := newBoundedStringSet(nodeListUniqueIPLimit)
	healthyIPs := newBoundedStringSet(nodeListUniqueIPLimit)
	var scanErr error
	visitHash := func(h node.Hash) bool {
		if err := ctx.Err(); err != nil {
			scanErr = err
			return false
		}
		stats.Scanned++
		entry, ok := s.Pool.GetEntry(h)
		if !ok || !s.nodeEntryMatchesFilters(entry, filters, subLookup) {
			return true
		}
		stats.Total++
		if ip := entry.GetEgressIP(); ip.IsValid() {
			ipString := ip.String()
			allIPs.Add(ipString)
			if !s.Pool.IsNodeDisabled(h) && entry.HasOutbound() && entry.CircuitOpenSince.Load() <= 0 {
				healthyIPs.Add(ipString)
			}
		}
		return visit(h, entry)
	}

	switch {
	case platformView != nil && managedNodes != nil:
		if platformView.Size() <= managedNodes.Size() {
			platformView.Range(func(h node.Hash) bool {
				managed, ok := managedNodes.LoadNode(h)
				if !ok || managed.Evicted {
					return true
				}
				return visitHash(h)
			})
		} else {
			managedNodes.RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
				if managed.Evicted || !platformView.Contains(h) {
					return true
				}
				return visitHash(h)
			})
		}
	case platformView != nil:
		platformView.Range(visitHash)
	case managedNodes != nil:
		managedNodes.RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
			if managed.Evicted {
				return true
			}
			return visitHash(h)
		})
	default:
		s.Pool.Range(func(h node.Hash, _ *node.NodeEntry) bool { return visitHash(h) })
	}

	if scanErr != nil {
		return stats, scanErr
	}
	stats.UniqueEgressIPs = len(allIPs.values)
	stats.UniqueHealthyEgressIPs = len(healthyIPs.values)
	stats.UniqueEgressIPsTruncated = allIPs.truncated
	stats.UniqueHealthyEgressIPsTruncated = healthyIPs.truncated
	return stats, nil
}

// NodeListSortKey returns only the field needed by the node list comparator.
func (s *ControlPlaneService) NodeListSortKey(hash node.Hash, entry *node.NodeEntry, sortBy string) NodeListSortKey {
	key := NodeListSortKey{NodeHash: hash.Hex(), Present: true}
	if entry == nil {
		return key
	}
	switch sortBy {
	case "created_at":
		key.Text = entry.CreatedAt.UTC().Format(time.RFC3339Nano)
	case "failure_count":
		key.Integer = int64(entry.FailureCount.Load())
	case "region":
		key.Text = entry.GetRegion(nil)
		if s.GeoIP != nil {
			key.Text = entry.GetRegion(s.GeoIP.Lookup)
		}
	case "purity_score", "assessed_at":
		ip := entry.GetEgressIP()
		if ip.IsValid() && s.Intel != nil {
			if lite, ok := s.Intel.Snapshot().Assessment(ip); ok {
				if sortBy == "purity_score" {
					if score := lite.ScoreValue(); score != nil {
						key.Integer = int64(*score)
					} else {
						key.Present = false
					}
				} else {
					key.Integer = lite.ComputedAt
					key.Present = lite.ComputedAt > 0
				}
			} else {
				key.Present = false
			}
		} else {
			key.Present = false
		}
	case "latency":
		if s.RuntimeCfg != nil {
			if cfg := s.RuntimeCfg.Load(); cfg != nil {
				if average, ok := node.AverageEWMAForDomainsMs(entry, cfg.LatencyAuthorities); ok {
					key.Number = average
				} else {
					key.Present = false
				}
			} else {
				key.Present = false
			}
		} else {
			key.Present = false
		}
	default:
		key.Text = s.Pool.ResolveNodeDisplayTag(hash)
	}
	return key
}

type NodeListSortKey struct {
	NodeHash string
	Text     string
	Integer  int64
	Number   float64
	Present  bool
}

func (s *ControlPlaneService) nodeEntryMatchesFilters(
	entry *node.NodeEntry,
	filters NodeFilters,
	subLookup node.SubLookupFunc,
) bool {
	if filters.Protocol != nil && entry.Protocol != *filters.Protocol {
		return false
	}
	// Legacy quality filters (quality_state, risk_grade) still read the
	// inspection summary, exactly as before.
	if filters.QualityState != nil || filters.RiskGrade != nil {
		state := "unobserved"
		summary := s.nodeQuality(entry.GetEgressIP())
		grade := quality.EffectiveRiskGrade(summary)
		if summary.Assessment != nil {
			state = summary.Assessment.State
		}
		if filters.QualityState != nil && state != *filters.QualityState {
			return false
		}
		if filters.RiskGrade != nil && grade != *filters.RiskGrade {
			return false
		}
	}
	// intel filters, served from the in-memory assessment projection.
	if hasIntelFilters(filters) {
		if !s.nodeIntelMatchesFilters(s.nodeIntel(entry, time.Now().UTC()), filters) {
			return false
		}
	}
	// Enabled/disabled filter.
	if filters.Enabled != nil {
		enabled := true
		if subLookup != nil {
			enabled = entry.HasEnabledSubscription(subLookup)
		}
		if enabled != *filters.Enabled {
			return false
		}
	}

	// Node tag fuzzy search filter.
	if filters.TagKeyword != nil {
		keyword := strings.ToLower(strings.TrimSpace(*filters.TagKeyword))
		if keyword != "" {
			matched := false
			for _, subID := range entry.SubscriptionIDs() {
				sub := s.SubMgr.Lookup(subID)
				if sub == nil {
					continue
				}
				managed, ok := sub.ManagedNodes().LoadNode(entry.Hash)
				if !ok {
					continue
				}
				tags := managed.Tags
				for _, tag := range tags {
					displayTag := sub.Name() + "/" + tag
					if strings.Contains(strings.ToLower(displayTag), keyword) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				return false
			}
		}
	}

	// Region filter.
	if filters.Region != nil {
		region := entry.GetRegion(nil)
		if s.GeoIP != nil {
			region = entry.GetRegion(s.GeoIP.Lookup)
		}
		if region == "" || region != *filters.Region {
			return false
		}
	}
	// Circuit open filter.
	if filters.CircuitOpen != nil {
		if entry.IsCircuitOpen() != *filters.CircuitOpen {
			return false
		}
	}
	// Has outbound filter.
	if filters.HasOutbound != nil {
		if entry.HasOutbound() != *filters.HasOutbound {
			return false
		}
	}
	// Egress IP filter.
	if filters.EgressIP != nil {
		egressIP := entry.GetEgressIP()
		if !egressIP.IsValid() || egressIP.String() != *filters.EgressIP {
			return false
		}
	}
	// Probed since filter.
	if filters.ProbedSince != nil {
		lastUpdate := entry.LastLatencyProbeAttempt.Load()
		if lastUpdate < filters.ProbedSince.UnixNano() {
			return false
		}
	}
	return true
}

// GetNode returns a single node by hash.
func (s *ControlPlaneService) GetNode(hashStr string) (*NodeSummary, error) {
	h, err := node.ParseHex(hashStr)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return nil, notFound("node not found")
	}
	ns := s.nodeEntryToSummary(h, entry)
	return &ns, nil
}

// ProbeEgress triggers a synchronous egress probe and returns results.
func (s *ControlPlaneService) ProbeEgress(hashStr string) (*probe.EgressProbeResult, error) {
	h, err := node.ParseHex(hashStr)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return nil, notFound("node not found")
	}
	result, err := s.ProbeMgr.ProbeEgressSync(h)
	if err != nil {
		return nil, internal("egress probe failed", err)
	}
	result.Region = entry.GetRegion(nil)
	if s.GeoIP != nil {
		result.Region = entry.GetRegion(s.GeoIP.Lookup)
	}
	return result, nil
}

// ProbeLatency triggers a synchronous latency probe and returns results.
func (s *ControlPlaneService) ProbeLatency(hashStr string) (*probe.LatencyProbeResult, error) {
	h, err := node.ParseHex(hashStr)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	if _, ok := s.Pool.GetEntry(h); !ok {
		return nil, notFound("node not found")
	}
	result, err := s.ProbeMgr.ProbeLatencySync(h)
	if err != nil {
		return nil, internal("latency probe failed", err)
	}
	return result, nil
}
