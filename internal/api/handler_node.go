package api

import (
	"cmp"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"prism/internal/service"
)

// validProtocolFilter validates the syntax of the `protocol` query parameter.
// The value is matched verbatim against node.NodeEntry.Protocol, whose values
// are lowercase identifiers such as "vless", "wireguard", "openvpn-client"
// or "ssr" (WP06 §1.4).
func validProtocolFilter(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func nodeTagSortKey(n service.NodeSummary) string {
	if n.DisplayTag != "" {
		return n.DisplayTag
	}
	if len(n.Tags) == 0 {
		return ""
	}
	bestCreated := int64(math.MaxInt64)
	bestTag := ""
	for _, t := range n.Tags {
		if t.SubscriptionCreatedAtNs < bestCreated {
			bestCreated = t.SubscriptionCreatedAtNs
			bestTag = t.Tag
			continue
		}
		if t.SubscriptionCreatedAtNs == bestCreated && (bestTag == "" || t.Tag < bestTag) {
			bestTag = t.Tag
		}
	}
	return bestTag
}

func compareNodeSummaries(sortBy string, a, b service.NodeSummary) int {
	order := 0
	switch sortBy {
	case "created_at":
		order = strings.Compare(a.CreatedAt, b.CreatedAt)
	case "failure_count":
		order = cmp.Compare(a.FailureCount, b.FailureCount)
	case "region":
		order = strings.Compare(a.Region, b.Region)
	case "purity_score":
		order = cmp.Compare(nodePurityScore(a), nodePurityScore(b))
	case "latency":
		order = cmp.Compare(nodeReferenceLatency(a), nodeReferenceLatency(b))
	case "assessed_at":
		order = cmp.Compare(a.Intel.AssessedAtNs, b.Intel.AssessedAtNs)
	default:
		order = strings.Compare(nodeTagSortKey(a), nodeTagSortKey(b))
	}
	if order != 0 {
		return order
	}
	return strings.Compare(a.NodeHash, b.NodeHash)
}

// nodePurityScore and nodeReferenceLatency read the sort keys of the WP10 §4
// intel fields. They are only compared between nodes that both have the value;
// compareNodeSortKeyPresence keeps the missing ones last.
func nodePurityScore(n service.NodeSummary) int {
	if n.Intel.PurityScore == nil {
		return 0
	}
	return *n.Intel.PurityScore
}

func nodeReferenceLatency(n service.NodeSummary) float64 {
	if n.ReferenceLatencyMs == nil {
		return 0
	}
	return *n.ReferenceLatencyMs
}

// nodeSortKeyPresent reports whether a node carries the key sortBy orders by.
func nodeSortKeyPresent(sortBy string, n service.NodeSummary) bool {
	switch sortBy {
	case "purity_score":
		return n.Intel.PurityScore != nil
	case "latency":
		return n.ReferenceLatencyMs != nil
	case "assessed_at":
		return n.Intel.AssessedAtNs > 0
	default:
		// Every other sort key always exists (an empty tag, a zero failure
		// count and an unset created_at are values, not absences).
		return true
	}
}

// compareNodeSortKeyPresence orders the nodes that have the sort key before the
// ones that do not. The order is deliberately not reversed by sort_order: a node
// without a purity score, without a latency measurement or without an
// assessment is always last, so "worst first" descending views stay meaningful
// and paging stays stable.
func compareNodeSortKeyPresence(sortBy string, a, b service.NodeSummary) int {
	hasA, hasB := nodeSortKeyPresent(sortBy, a), nodeSortKeyPresent(sortBy, b)
	switch {
	case hasA == hasB:
		return 0
	case hasA:
		return -1
	default:
		return 1
	}
}

// sortNodeSummaries sorts in place. Ties are broken by the node hash, so the
// order is total and paging through the same view never skips or repeats a
// node.
func sortNodeSummaries(nodes []service.NodeSummary, sorting Sorting) {
	slices.SortStableFunc(nodes, func(a, b service.NodeSummary) int {
		if order := compareNodeSortKeyPresence(sorting.SortBy, a, b); order != 0 {
			return order
		}
		return applySortOrder(compareNodeSummaries(sorting.SortBy, a, b), sorting.SortOrder)
	})
}

type nodeListPageResponse struct {
	Items                  []service.NodeSummary `json:"items"`
	Total                  int                   `json:"total"`
	Limit                  int                   `json:"limit"`
	Offset                 int                   `json:"offset"`
	UniqueEgressIPs        int                   `json:"unique_egress_ips"`
	UniqueHealthyEgressIPs int                   `json:"unique_healthy_egress_ips"`
}

func countUniqueEgressIPs(nodes []service.NodeSummary) int {
	seen := make(map[string]struct{})
	for _, n := range nodes {
		if n.EgressIP == "" {
			continue
		}
		seen[n.EgressIP] = struct{}{}
	}
	return len(seen)
}

func countUniqueHealthyAndEnabledEgressIPs(nodes []service.NodeSummary) int {
	seen := make(map[string]struct{})
	for _, n := range nodes {
		if n.EgressIP == "" {
			continue
		}
		if !n.IsHealthyAndEnabled() {
			continue
		}
		seen[n.EgressIP] = struct{}{}
	}
	return len(seen)
}

// HandleListNodes returns a handler for GET /api/v1/nodes.
func HandleListNodes(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filters := service.NodeFilters{}
		for _, filter := range []struct {
			name    string
			allowed []string
			target  **string
		}{
			{"ip_type", []string{"unknown", "residential", "non_residential", "datacenter", "business", "wireless", "mobile", "conflicting"}, &filters.IPType},
			{"quality_state", []string{"unobserved", "pending", "partial", "valid", "stale", "conflicting", "unsupported"}, &filters.QualityState},
			{"risk_grade", []string{"unknown", "low", "moderate", "high", "severe", "review"}, &filters.RiskGrade},
			{"purity_band", []string{"unknown", "excellent", "clean", "fair", "mixed", "poor", "review"}, &filters.PurityBand},
		} {
			if value := q.Get(filter.name); value != "" {
				if !slices.Contains(filter.allowed, value) {
					writeInvalidArgument(w, filter.name+": unsupported value")
					return
				}
				*filter.target = &value
			}
		}
		if value := q.Get("protocol"); value != "" {
			if !validProtocolFilter(value) {
				writeInvalidArgument(w, "protocol: unsupported value")
				return
			}
			// NodeEntry.Protocol is stored lowercase, so the filter is an
			// exact lowercase match (see NodeFilters.Protocol).
			filters.Protocol = &value
		}

		// WP10 §4 intel filters (purity, verdict, confidence, native, asn,
		// country and the repeatable check filter).
		intelFilters := nodeIntelFilterValues{
			PurityMin:     q.Get("purity_min"),
			PurityMax:     q.Get("purity_max"),
			Verdict:       q.Get("verdict"),
			ConfidenceMin: q.Get("confidence_min"),
			ASN:           q.Get("asn"),
			Country:       q.Get("country"),
			Checks:        q["check"],
		}
		native, ok := parseBoolQueryOrWriteInvalid(w, r, "native")
		if !ok {
			return
		}
		intelFilters.Native = native
		if err := intelFilters.apply(&filters); err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}

		platformID, ok := parseOptionalUUIDQuery(w, r, "platform_id", "platform_id")
		if !ok {
			return
		}
		filters.PlatformID = platformID

		subscriptionID, ok := parseOptionalUUIDQuery(w, r, "subscription_id", "subscription_id")
		if !ok {
			return
		}
		filters.SubscriptionID = subscriptionID

		if v := q.Get("region"); v != "" {
			filters.Region = &v
		}
		if v := q.Get("egress_ip"); v != "" {
			filters.EgressIP = &v
		}
		if v := strings.TrimSpace(q.Get("tag_keyword")); v != "" {
			filters.TagKeyword = &v
		}

		circuitOpen, ok := parseBoolQueryOrWriteInvalid(w, r, "circuit_open")
		if !ok {
			return
		}
		filters.CircuitOpen = circuitOpen

		hasOutbound, ok := parseBoolQueryOrWriteInvalid(w, r, "has_outbound")
		if !ok {
			return
		}
		filters.HasOutbound = hasOutbound

		enabled, ok := parseBoolQueryOrWriteInvalid(w, r, "enabled")
		if !ok {
			return
		}
		filters.Enabled = enabled

		if v := q.Get("probed_since"); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				writeInvalidArgument(w, "probed_since: invalid RFC3339 timestamp")
				return
			}
			filters.ProbedSince = &t
		}

		nodes, err := cp.ListNodes(filters)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		sorting, ok := parseSortingOrWriteInvalid(w, r, []string{
			"tag", "created_at", "failure_count", "region",
			"purity_score", "latency", "assessed_at",
		}, "tag", "asc")
		if !ok {
			return
		}
		sortNodeSummaries(nodes, sorting)

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		// WP10 §4: the node_egress display facts of the page are read in one
		// batch (never one query per node); the assessment itself already came
		// from the in-memory projection.
		page := PaginateSlice(nodes, pg)
		cp.FillNodeIntelEgress(r.Context(), page)
		WriteJSON(w, http.StatusOK, nodeListPageResponse{
			Items:                  page,
			Total:                  len(nodes),
			Limit:                  pg.Limit,
			Offset:                 pg.Offset,
			UniqueEgressIPs:        countUniqueEgressIPs(nodes),
			UniqueHealthyEgressIPs: countUniqueHealthyAndEnabledEgressIPs(nodes),
		})
	}
}

// HandleGetNode returns a handler for GET /api/v1/nodes/{hash}.
func HandleGetNode(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := PathParam(r, "hash")
		if len(hash) > 128 {
			writeInvalidArgument(w, "node_hash: invalid length")
			return
		}
		n, err := cp.GetNode(hash)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		items := []service.NodeSummary{*n}
		cp.FillNodeIntelEgress(r.Context(), items)
		WriteJSON(w, http.StatusOK, items[0])
	}
}

// HandleProbeEgress returns a handler for POST /api/v1/nodes/{hash}/actions/probe-egress.
func HandleProbeEgress(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := PathParam(r, "hash")
		if len(hash) > 128 {
			writeInvalidArgument(w, "node_hash: invalid length")
			return
		}
		result, err := cp.ProbeEgress(hash)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

// HandleProbeLatency returns a handler for POST /api/v1/nodes/{hash}/actions/probe-latency.
func HandleProbeLatency(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := PathParam(r, "hash")
		if len(hash) > 128 {
			writeInvalidArgument(w, "node_hash: invalid length")
			return
		}
		result, err := cp.ProbeLatency(hash)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}
