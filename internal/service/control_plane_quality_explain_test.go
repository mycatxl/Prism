package service

import (
	"errors"
	"net/netip"
	"regexp"
	"testing"
	"time"

	"prism/internal/geoip"
	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
)

// WP10 §2.1 explain endpoint. These tests exercise the service layer directly,
// because internal/api has no handler test for GET
// /api/v1/platforms/{id}/nodes/{hash}/explain: every branch here (unknown
// platform, malformed hash, unknown node, the admission reasons) is reachable
// only through ControlPlaneService.ExplainPlatformNode.

func explainTestRegexps(t *testing.T, patterns ...string) []*regexp.Regexp {
	t.Helper()
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		out = append(out, re)
	}
	return out
}

func explainTestPlatform(pool *topology.GlobalNodePool, id string, filter node.TagFilter, regions []string, policy model.QualityPolicy) *platform.Platform {
	plat := platform.NewPlatformWithTagFilter(id, id, filter, regions)
	plat.QualityPolicy = policy
	pool.RegisterPlatform(plat)
	return plat
}

// explainTestNode adds a node owned by sub and applies the mutators to it.
func explainTestNode(
	t *testing.T,
	pool *topology.GlobalNodePool,
	sub *subscription.Subscription,
	server string,
	mutators ...func(*node.NodeEntry),
) node.Hash {
	t.Helper()
	raw := []byte(`{"type":"ss","server":"` + server + `","port":443}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, sub.ID)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"tag"}})
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing after add", hash.Hex())
	}
	for _, mutate := range mutators {
		mutate(entry)
	}
	// New subscription nodes start circuit-open and must be proven healthy by a
	// probe result, exactly like the production add path.
	pool.RecordResult(hash, true)
	return hash
}

func explainNodeEgress(ip string) func(*node.NodeEntry) {
	return func(entry *node.NodeEntry) { entry.SetEgressIP(netip.MustParseAddr(ip)) }
}

func explainNodeRegion(region string) func(*node.NodeEntry) {
	return func(entry *node.NodeEntry) { entry.SetEgressRegion(region) }
}

func explainNodeOutbound() func(*node.NodeEntry) {
	return func(entry *node.NodeEntry) {
		outbound := testutil.NewNoopOutbound()
		entry.Outbound.Store(&outbound)
	}
}

func explainNodeLatency() func(*node.NodeEntry) {
	return func(entry *node.NodeEntry) {
		entry.LatencyTable.Update("example.com", 25*time.Millisecond, 10*time.Minute)
	}
}

// explainRules runs the explain endpoint and returns the rules by name.
func explainRules(t *testing.T, cp *ControlPlaneService, platformID string, hash node.Hash) map[string]NodeRuleResult {
	t.Helper()
	results, err := cp.ExplainPlatformNode(platformID, hash.Hex())
	if err != nil {
		t.Fatalf("ExplainPlatformNode: %v", err)
	}
	out := make(map[string]NodeRuleResult, len(results))
	for _, result := range results {
		out[result.Rule] = result
	}
	return out
}

func assertExplainRule(t *testing.T, rules map[string]NodeRuleResult, rule string, passed bool, detail string) {
	t.Helper()
	got, ok := rules[rule]
	if !ok {
		t.Fatalf("rule %q missing from the explain result: %v", rule, rules)
	}
	if got.Passed != passed {
		t.Errorf("rule %q passed = %v, want %v", rule, got.Passed, passed)
	}
	if got.Detail != detail {
		t.Errorf("rule %q detail = %q, want %q", rule, got.Detail, detail)
	}
}

func TestExplainPlatformNode_Errors(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	sub := subscription.NewSubscription("sub-explain", "sub-explain", "https://example.com/a", true, false)
	subMgr.Register(sub)
	hash := explainTestNode(t, pool, sub, "1.1.1.1", explainNodeEgress("203.0.113.10"))

	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr}

	if _, err := cp.ExplainPlatformNode("missing-platform", hash.Hex()); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("unknown platform error = %v, want NOT_FOUND", err)
	}

	plat := explainTestPlatform(pool, "plat-explain", node.TagFilter{}, nil, model.QualityPolicy{})
	if _, err := cp.ExplainPlatformNode(plat.ID, "not-a-hash"); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("malformed node hash error = %v, want INVALID_ARGUMENT", err)
	}

	unknownHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"9.9.9.9","port":443}`))
	if _, err := cp.ExplainPlatformNode(plat.ID, unknownHash.Hex()); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("unknown node error = %v, want NOT_FOUND", err)
	}

	// A platform registered without a routable view still explains: the
	// endpoint never mutates or reads admission state.
	if _, err := cp.ExplainPlatformNode("", hash.Hex()); err == nil {
		t.Fatal("empty platform id must not be found")
	}
}

func isServiceErrorCode(err error, code string) bool {
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		return false
	}
	return svcErr.Code == code
}

func TestExplainPlatformNode_AcceptsRoutableNode(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	sub := subscription.NewSubscription("sub-explain", "sub-explain", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := explainTestNode(t, pool, sub, "1.1.1.1",
		explainNodeEgress("203.0.113.10"),
		explainNodeRegion("jp"),
		explainNodeOutbound(),
		explainNodeLatency(),
	)

	plat := explainTestPlatform(pool, "plat-explain",
		node.TagFilter{Must: explainTestRegexps(t, "^sub-explain/tag$", "tag$")}, []string{"jp"}, model.QualityPolicy{})

	// A non-nil GeoIP service is the production wiring; the stored egress
	// region still wins (node.GetRegion prefers the probe metadata).
	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr, GeoIP: &geoip.Service{}}

	rules := explainRules(t, cp, plat.ID, hash)

	assertExplainRule(t, rules, "node.enabled", true, "enabled")
	assertExplainRule(t, rules, "node.healthy", true, "outbound ready")
	assertExplainRule(t, rules, "regex", true, "filters=must=^sub-explain/tag$|tag$ any= must_not=")
	assertExplainRule(t, rules, "egress", true, "203.0.113.10")
	assertExplainRule(t, rules, "region", true, "region=jp")
	assertExplainRule(t, rules, "latency", true, "latency known")
	assertExplainRule(t, rules, "quality", true, "no quality policy configured")

	results, err := cp.ExplainPlatformNode(plat.ID, hash.Hex())
	if err != nil {
		t.Fatalf("ExplainPlatformNode: %v", err)
	}
	if len(results) != 7 {
		t.Fatalf("rule count = %d, want 7 (%+v)", len(results), results)
	}
	for _, result := range results {
		if !result.Passed {
			t.Errorf("rule %q must pass for a routable node: %+v", result.Rule, result)
		}
	}
}

func TestExplainPlatformNode_ReportsEveryExclusionReason(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	enabled := subscription.NewSubscription("sub-on", "sub-on", "https://example.com/on", true, false)
	disabled := subscription.NewSubscription("sub-off", "sub-off", "https://example.com/off", false, false)
	subMgr.Register(enabled)
	subMgr.Register(disabled)

	plat := explainTestPlatform(pool, "plat-explain",
		node.TagFilter{Must: explainTestRegexps(t, "^sub-on/tag$")}, []string{"jp"}, model.QualityPolicy{})
	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr}

	t.Run("disabled by subscription", func(t *testing.T) {
		hash := explainTestNode(t, pool, disabled, "2.2.2.2",
			explainNodeEgress("203.0.113.20"), explainNodeOutbound(), explainNodeLatency())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "node.enabled", false, "disabled by subscription")
	})

	t.Run("outbound not ready", func(t *testing.T) {
		hash := explainTestNode(t, pool, enabled, "3.3.3.3",
			explainNodeEgress("203.0.113.30"), explainNodeLatency())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "node.healthy", false, "outbound not ready")
	})

	t.Run("tag filter mismatch", func(t *testing.T) {
		other := subscription.NewSubscription("sub-other", "sub-other", "https://example.com/other", true, false)
		subMgr.Register(other)
		hash := explainTestNode(t, pool, other, "4.4.4.4",
			explainNodeEgress("203.0.113.40"), explainNodeOutbound(), explainNodeLatency())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "regex", false, "filters=must=^sub-on/tag$ any= must_not=")
	})

	t.Run("no egress ip observed", func(t *testing.T) {
		hash := explainTestNode(t, pool, enabled, "5.5.5.5", explainNodeOutbound(), explainNodeLatency())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "egress", false, "no egress ip observed")
	})

	t.Run("region mismatch", func(t *testing.T) {
		hash := explainTestNode(t, pool, enabled, "6.6.6.6",
			explainNodeEgress("203.0.113.60"), explainNodeRegion("us"), explainNodeOutbound(), explainNodeLatency())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "region", false, "region=us")
	})

	t.Run("no latency record", func(t *testing.T) {
		hash := explainTestNode(t, pool, enabled, "7.7.7.7",
			explainNodeEgress("203.0.113.70"), explainNodeRegion("jp"), explainNodeOutbound())
		rules := explainRules(t, cp, plat.ID, hash)
		assertExplainRule(t, rules, "latency", false, "no latency record")
	})
}

func TestExplainPlatformNode_QualityPolicyRules(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	sub := subscription.NewSubscription("sub-explain", "sub-explain", "https://example.com/a", true, false)
	subMgr.Register(sub)

	intelSvc := newIntelProjectionForTest(t)
	now := time.Now().UTC()

	policy := model.QualityPolicy{
		MinPurity:        qualityTestIntPtr(80),
		AllowedVerdicts:  []string{"favorable"},
		MinConfidence:    "high",
		RequireNative:    true,
		RequiredChecks:   map[string]string{"chatgpt": "available"},
		MaxAssessmentAge: "1h",
		MaxEgressAge:     "1h",
	}
	plat := explainTestPlatform(pool, "plat-explain-quality",
		node.TagFilter{}, []string{"jp"}, policy)
	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr, Intel: intelSvc}

	assessed := explainTestNode(t, pool, sub, "1.1.1.1",
		explainNodeEgress("203.0.113.10"), explainNodeRegion("jp"), explainNodeOutbound(), explainNodeLatency())
	intelSvc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score:      40,
		Band:       intel.BandPoor,
		Verdict:    intel.VerdictHighRisk,
		IPType:     intel.IPTypeResidential,
		Confidence: intel.ConfidenceLow,
		State:      intel.StateValid,
		Native:     0,
		ValidUntil: now.Add(time.Hour).UnixNano(),
		ComputedAt: now.Add(-2 * time.Hour).UnixNano(),
	})
	intelSvc.Snapshot().SetCheck(assessed.Hex(), "chatgpt", intel.OutcomeLite{
		Outcome:    intel.OutcomeBlocked,
		ValidUntil: now.Add(time.Hour).UnixNano(),
		ObservedAt: now.UnixNano(),
	})

	rules := explainRules(t, cp, plat.ID, assessed)
	assertExplainRule(t, rules, "quality.egress", true, "203.0.113.10")
	assertExplainRule(t, rules, "quality.exclude_high_risk", false, "verdict=high_risk")
	assertExplainRule(t, rules, "quality.allowed_verdicts", false, "verdict=high_risk allowed=favorable")
	assertExplainRule(t, rules, "quality.min_purity", false, "score=40<80")
	assertExplainRule(t, rules, "quality.min_confidence", false, "confidence=low<high")
	assertExplainRule(t, rules, "quality.require_native", false, "native=false")
	assertExplainRule(t, rules, "quality.check.chatgpt", false, "outcome=blocked want=available")
	assertExplainRule(t, rules, "quality.max_assessment_age", false, "age=2h0m0s>1h0m0s")
	assertExplainRule(t, rules, "quality.max_egress_age", false, "egress_observed_at=unknown")
	if _, ok := rules["quality.unknown"]; ok {
		t.Errorf("an assessed node must not report quality.unknown: %v", rules)
	}

	// A node without any assessment stops at the unknown-action rule when the
	// policy allows unassessed nodes.
	allowPolicy := model.QualityPolicy{UnknownAction: "allow"}
	allowPlat := explainTestPlatform(pool, "plat-explain-allow", node.TagFilter{}, nil, allowPolicy)
	unassessed := explainTestNode(t, pool, sub, "2.2.2.2",
		explainNodeEgress("203.0.113.99"), explainNodeOutbound(), explainNodeLatency())

	allowResults, err := cp.ExplainPlatformNode(allowPlat.ID, unassessed.Hex())
	if err != nil {
		t.Fatalf("ExplainPlatformNode: %v", err)
	}
	names := make([]string, 0, len(allowResults))
	for _, result := range allowResults {
		names = append(names, result.Rule)
	}
	want := []string{"node.enabled", "node.healthy", "regex", "egress", "region", "latency", "quality.egress", "quality.unknown"}
	if len(names) != len(want) {
		t.Fatalf("quality rules for an unassessed node = %v, want %v", names, want)
	}
	for i, rule := range want {
		if names[i] != rule {
			t.Fatalf("rule %d = %q, want %q (%v)", i, names[i], rule, names)
		}
	}
	assertExplainRule(t, rulesByName(t, allowResults), "quality.unknown", true, "state=unobserved")
}

func rulesByName(t *testing.T, results []NodeRuleResult) map[string]NodeRuleResult {
	t.Helper()
	out := make(map[string]NodeRuleResult, len(results))
	for _, result := range results {
		out[result.Rule] = result
	}
	return out
}

func qualityTestIntPtr(value int) *int { return &value }

func TestExplainHelpers(t *testing.T) {
	if got := boolText(true, "yes", "no"); got != "yes" {
		t.Errorf("boolText(true) = %q, want %q", got, "yes")
	}
	if got := boolText(false, "yes", "no"); got != "no" {
		t.Errorf("boolText(false) = %q, want %q", got, "no")
	}

	if got := egressText(false, "203.0.113.1"); got != "no egress ip observed" {
		t.Errorf("egressText(invalid) = %q", got)
	}
	if got := egressText(true, "203.0.113.1"); got != "203.0.113.1" {
		t.Errorf("egressText(valid) = %q, want the address", got)
	}

	if got := joinRegexps(nil); got != "" {
		t.Errorf("joinRegexps(nil) = %q, want an empty string", got)
	}
	multi := []*regexp.Regexp{regexp.MustCompile("^a$"), regexp.MustCompile("^b$")}
	if got := joinRegexps(multi); got != "^a$|^b$" {
		t.Errorf("joinRegexps(two) = %q, want %q", got, "^a$|^b$")
	}

	if got := filtersText(node.TagFilter{}); got != "must= any= must_not=" {
		t.Errorf("filtersText(empty) = %q", got)
	}
	full := node.TagFilter{
		Must:    explainTestRegexps(t, "^m$"),
		Any:     explainTestRegexps(t, "^a$"),
		MustNot: explainTestRegexps(t, "^n$"),
	}
	if got := filtersText(full); got != "must=^m$ any=^a$ must_not=^n$" {
		t.Errorf("filtersText(full) = %q, want %q", got, "must=^m$ any=^a$ must_not=^n$")
	}
}
