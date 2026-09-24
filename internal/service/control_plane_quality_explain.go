package service

import (
	"regexp"
	"strings"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
)

// NodeRuleResult is one explainable admission rule of WP10 §2.1.
type NodeRuleResult struct {
	Rule   string `json:"rule"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// ExplainPlatformNode reports, rule by rule, whether a node would be routable
// through a platform and why not. It never mutates state.
func (s *ControlPlaneService) ExplainPlatformNode(platformID, nodeHash string) ([]NodeRuleResult, error) {
	plat, ok := s.Pool.GetPlatform(platformID)
	if !ok || plat == nil {
		return nil, notFound("platform not found")
	}
	h, err := node.ParseHex(nodeHash)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok || entry == nil {
		return nil, notFound("node not found")
	}

	results := make([]NodeRuleResult, 0, 12)
	add := func(rule string, passed bool, detail string) {
		results = append(results, NodeRuleResult{Rule: rule, Passed: passed, Detail: detail})
	}

	disabled := false
	if s.Pool != nil {
		disabled = s.Pool.IsNodeDisabled(h)
	}
	add("node.enabled", !disabled, boolText(disabled, "disabled by subscription", "enabled"))
	add("node.healthy", entry.IsHealthy(), boolText(entry.IsHealthy(), "outbound ready", "outbound not ready"))

	subLookup := s.Pool.MakeSubLookup()
	add("regex", entry.MatchTagFilter(plat.RegexFilters, subLookup), "filters="+filtersText(plat.RegexFilters))

	egress := entry.GetEgressIP()
	add("egress", egress.IsValid(), egressText(egress.IsValid(), egress.String()))

	region := entry.GetRegion(nil)
	if s.GeoIP != nil {
		region = entry.GetRegion(s.GeoIP.Lookup)
	}
	add("region", platform.MatchRegionFilter(region, plat.RegionFilters), "region="+region)

	add("latency", entry.HasLatency(), boolText(entry.HasLatency(), "latency known", "no latency record"))

	var snap platform.QualitySnapshotReader
	if s.Intel != nil {
		snap = s.Intel.Snapshot()
	}
	for _, decision := range platform.ExplainQuality(plat.QualityPolicy, entry, snap, time.Now()) {
		add(decision.Rule, decision.Passed, decision.Detail)
	}
	return results, nil
}

func boolText(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}

func egressText(valid bool, value string) string {
	if !valid {
		return "no egress ip observed"
	}
	return value
}

func filtersText(filters node.TagFilter) string {
	out := "must=" + joinRegexps(filters.Must) + " any=" + joinRegexps(filters.Any) +
		" must_not=" + joinRegexps(filters.MustNot)
	return out
}

func joinRegexps(rules []*regexp.Regexp) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, rule.String())
	}
	return strings.Join(parts, "|")
}
