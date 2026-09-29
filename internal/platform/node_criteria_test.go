package platform

import (
	"net/netip"
	"regexp"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
)

// criteriaEntry builds a fully routable node with the fields the explicit
// criteria read: protocol (from the document type), region, egress IP (which
// keys the intel projection), subscription and tags.
func criteriaEntry(t *testing.T, doc string, region string, egressIP string, subIDs ...string) *node.NodeEntry {
	t.Helper()
	hash := makeHash(doc)
	entry := makeFullyRoutableEntry(hash, subIDs...)
	// makeFullyRoutableEntry passes nil raw options, so the protocol has to be
	// installed from a real document here.
	parsed := node.NewNodeEntry(hash, []byte(doc), time.Now(), 16)
	entry.Protocol = parsed.Protocol
	if region != "" {
		entry.SetEgressRegion(region)
	}
	if egressIP != "" {
		entry.SetEgressIP(netip.MustParseAddr(egressIP))
	}
	return entry
}

// subLookupAlwaysEnabled resolves every subscription, which is the "still holds
// this node" case.
func subLookupAlwaysEnabled(string, node.Hash) (string, bool, []string, bool) {
	return "TestSub", true, []string{"all"}, true
}

// cleanAssessment is the projection row of a node that passes every quality rule.
func cleanAssessment(band string, ipType string) intel.AssessmentLite {
	return intel.AssessmentLite{
		Score:      92,
		Band:       intel.BandCode(band),
		Verdict:    intel.VerdictCode("favorable"),
		IPType:     intel.IPTypeCode(ipType),
		Confidence: intel.ConfidenceCode("high"),
		ValidUntil: admitNow.Add(time.Hour).UnixNano(),
	}
}

// TestMatchNodeCriteria_AndAcrossCriteriaOrWithinCriterion pins the two halves of
// the contract: every criterion the operator set must hold (AND), and the values
// inside one criterion are alternatives (OR).
func TestMatchNodeCriteria_AndAcrossCriteriaOrWithinCriterion(t *testing.T) {
	// One node: US, residential, clean, vless, subscription sub-1.
	snap := newFakeSnapshot().set("9.9.9.1", cleanAssessment("clean", "residential"))
	unassessed := newFakeSnapshot()
	cases := []struct {
		name       string
		criteria   NodeCriteria
		projection *fakeSnapshot
		wantOK     bool
		wantReason string
	}{
		{
			name:       "no_criteria_admits",
			criteria:   NodeCriteria{},
			projection: snap,
			wantOK:     true,
		},
		{
			name: "every_criterion_holds",
			criteria: NodeCriteria{
				Regions:         []string{"us"},
				IPTypes:         []string{"residential"},
				PurityBands:     []string{"clean"},
				Protocols:       []string{"vless"},
				SubscriptionIDs: []string{"sub-1"},
			},
			projection: snap,
			wantOK:     true,
		},
		{
			name:       "values_inside_one_criterion_are_alternatives",
			criteria:   NodeCriteria{Regions: []string{"hk", "us"}},
			projection: snap,
			wantOK:     true,
		},
		{
			name:       "ip_types_inside_one_criterion_are_alternatives",
			criteria:   NodeCriteria{IPTypes: []string{"mobile", "residential"}},
			projection: snap,
			wantOK:     true,
		},
		{
			name:       "purity_bands_inside_one_criterion_are_alternatives",
			criteria:   NodeCriteria{PurityBands: []string{"excellent", "clean"}},
			projection: snap,
			wantOK:     true,
		},
		{
			name:       "one_failing_criterion_rejects_despite_the_others_matching",
			criteria:   NodeCriteria{Regions: []string{"hk"}, IPTypes: []string{"residential"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonRegionFilter,
		},
		{
			name:       "a_matching_criterion_cannot_rescue_a_failed_region",
			criteria:   NodeCriteria{Regions: []string{"hk", "jp"}, Protocols: []string{"vless"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonRegionFilter,
		},
		{
			name:       "network_type_must_match_when_set",
			criteria:   NodeCriteria{Regions: []string{"us"}, IPTypes: []string{"mobile"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonIPTypeFilter,
		},
		{
			name:       "purity_band_must_match_when_set",
			criteria:   NodeCriteria{PurityBands: []string{"poor"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonPurityBandFilter,
		},
		{
			name:       "protocol_must_match_when_set",
			criteria:   NodeCriteria{Protocols: []string{"trojan"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonProtocol,
		},
		{
			name:       "subscription_must_hold_the_node",
			criteria:   NodeCriteria{SubscriptionIDs: []string{"sub-9"}},
			projection: snap,
			wantOK:     false,
			wantReason: ReasonSubscription,
		},
		{
			name:       "unassessed_node_fails_a_purity_criterion",
			criteria:   NodeCriteria{Regions: []string{"us"}, PurityBands: []string{"clean"}},
			projection: unassessed,
			wantOK:     false,
			wantReason: ReasonPurityBandFilter,
		},
	}

	entry := criteriaEntry(t, `{"type":"vless","server":"9.9.9.1"}`, "us", "9.9.9.1", "sub-1")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := MatchNodeCriteria(tc.criteria, entry, subLookupAlwaysEnabled, nil, nil, tc.projection, admitNow)
			if ok != tc.wantOK {
				t.Fatalf("MatchNodeCriteria() = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if !tc.wantOK && reason != tc.wantReason {
				t.Fatalf("rejection reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestMatchNodeCriteria_AndBeatsOr is the negative case that would pass under OR
// admission: this node matches the protocol criterion and nothing else, so an OR
// implementation would admit it while the AND contract must reject it.
func TestMatchNodeCriteria_AndBeatsOr(t *testing.T) {
	criteria := NodeCriteria{
		Regions:     []string{"hk"},
		IPTypes:     []string{"mobile"},
		PurityBands: []string{"excellent"},
		Protocols:   []string{"vless"},
	}
	entry := criteriaEntry(t, `{"type":"vless","server":"9.9.9.2"}`, "us", "9.9.9.2", "sub-1")
	snap := newFakeSnapshot().set("9.9.9.2", cleanAssessment("clean", "residential"))

	ok, reason := MatchNodeCriteria(criteria, entry, subLookupAlwaysEnabled, nil, nil, snap, admitNow)
	if ok {
		t.Fatal("a node matching one criterion out of four was admitted: the criteria are ORed, not ANDed")
	}
	if reason != ReasonRegionFilter {
		t.Fatalf("rejection reason = %q, want %q (the first criterion that fails)", reason, ReasonRegionFilter)
	}
}

// TestMatchNodeCriteria_RegionAndProtocolCombine pins the two axes the live smoke
// test intersects: an empty intersection must admit nothing even though each
// criterion on its own matches something.
func TestMatchNodeCriteria_RegionAndProtocolCombine(t *testing.T) {
	hk := criteriaEntry(t, `{"type":"vless","server":"9.9.9.3"}`, "hk", "9.9.9.3", "sub-1")
	us := criteriaEntry(t, `{"type":"trojan","server":"9.9.9.4"}`, "us", "9.9.9.4", "sub-1")

	// hk+vless matches the first node only.
	intersected := NodeCriteria{Regions: []string{"hk"}, Protocols: []string{"vless"}}
	if ok, reason := MatchNodeCriteria(intersected, hk, subLookupAlwaysEnabled, nil, nil, nil, admitNow); !ok {
		t.Fatalf("hk+vless node rejected: %s", reason)
	}
	if ok, _ := MatchNodeCriteria(intersected, us, subLookupAlwaysEnabled, nil, nil, nil, admitNow); ok {
		t.Fatal("us+trojan node admitted by the hk+vless intersection")
	}

	// us+vless matches nothing.
	empty := NodeCriteria{Regions: []string{"us"}, Protocols: []string{"vless"}}
	for name, candidate := range map[string]*node.NodeEntry{"hk": hk, "us": us} {
		if ok, _ := MatchNodeCriteria(empty, candidate, subLookupAlwaysEnabled, nil, nil, nil, admitNow); ok {
			t.Fatalf("%s node admitted by an empty intersection (region us + protocol vless)", name)
		}
	}
}

// TestPlatform_LegacyRegexOnlyPlatformIsUnchanged proves backward compatibility:
// a platform persisted before the explicit criteria existed (regex filters only,
// with the plain/*/! rule kinds) filters exactly as it did.
func TestPlatform_LegacyRegexOnlyPlatformIsUnchanged(t *testing.T) {
	// The legacy semantics are pinned by node.MatchTagFilter: plain rules are
	// alternatives, a "*" rule must hold and a "!" rule rejects the node. A
	// candidate is always one "<subscription>/<tag>" string, so a plain and a
	// must rule only agree on a node whose single tag satisfies both.
	legacy := model.Platform{
		ID:   "legacy-platform",
		Name: "Legacy",
		// Plain alternatives plus a must-not rule: the shape the UI offered
		// before the explicit criteria existed.
		RegexFilters: []string{"^TestSub/hk", "^TestSub/jp", "!slow"},
		// BuildFromModel requires a valid miss action, exactly like a persisted row.
		ReverseProxyMissAction: string(ReverseProxyMissActionTreatAsEmpty),
	}
	plat, err := BuildFromModel(legacy)
	if err != nil {
		t.Fatalf("BuildFromModel: %v", err)
	}
	if len(plat.IPTypes) != 0 || len(plat.PurityBands) != 0 || len(plat.Protocols) != 0 {
		t.Fatal("a legacy platform must not gain explicit criteria")
	}

	match := func(tags ...string) bool {
		entry := makeFullyRoutableEntry(makeHash(`{"type":"ss"}`), "sub1")
		lookup := func(string, node.Hash) (string, bool, []string, bool) {
			return "TestSub", true, tags, true
		}
		ok, _ := MatchNodeCriteria(plat.Criteria(), entry, lookup, usGeoLookup, nil, nil, time.Now())
		return ok
	}

	// Any one of the two plain rules admits the node: that OR inside the legacy
	// rule set is the pre-change behaviour and must survive.
	if !match("hk") {
		t.Fatal("legacy platform rejected a node matching the first plain rule")
	}
	if !match("jp") {
		t.Fatal("legacy platform rejected a node matching the second plain rule")
	}
	// A must-not rule rejects the node.
	if match("hk", "slow") {
		t.Fatal("legacy platform admitted a node matching a must-not rule")
	}
	// A node matching no plain rule is rejected.
	if match("us") {
		t.Fatal("legacy platform admitted a node matching no plain rule")
	}

	// The "*" rule keeps the AND-inside-one-candidate behaviour it always had.
	mustPlatform, err := BuildFromModel(model.Platform{
		ID:                     "legacy-must",
		Name:                   "LegacyMust",
		RegexFilters:           []string{"*fast"},
		ReverseProxyMissAction: string(ReverseProxyMissActionTreatAsEmpty),
	})
	if err != nil {
		t.Fatalf("BuildFromModel: %v", err)
	}
	ok, _ := MatchNodeCriteria(
		mustPlatform.Criteria(),
		makeFullyRoutableEntry(makeHash(`{"type":"ss"}`), "sub1"),
		func(string, node.Hash) (string, bool, []string, bool) { return "TestSub", true, []string{"fast"}, true },
		usGeoLookup, nil, nil, time.Now(),
	)
	if !ok {
		t.Fatal("legacy * rule rejected a node whose tag matches it")
	}
	ok, _ = MatchNodeCriteria(
		mustPlatform.Criteria(),
		makeFullyRoutableEntry(makeHash(`{"type":"ss"}`), "sub1"),
		func(string, node.Hash) (string, bool, []string, bool) { return "TestSub", true, []string{"slow"}, true },
		usGeoLookup, nil, nil, time.Now(),
	)
	if ok {
		t.Fatal("legacy * rule admitted a node whose tags do not match it")
	}
}

// TestPlatform_ExplicitCriteriaFlowIntoTheRebuild proves the new criteria are
// applied where the old ones are: in the routable-view rebuild.
func TestPlatform_ExplicitCriteriaFlowIntoTheRebuild(t *testing.T) {
	plat := NewPlatformWithTagFilter("p1", "Test", node.TagFilter{}, nil)
	plat.SetNodeCriteria(NodeCriteria{
		Regions:   []string{"hk"},
		IPTypes:   []string{"mobile", "residential"},
		Protocols: []string{"vless"},
	})
	plat.SetQualitySnapshot(newFakeSnapshot().
		set("9.9.9.11", cleanAssessment("clean", "residential")).
		set("9.9.9.12", cleanAssessment("clean", "datacenter")))

	hk := criteriaEntry(t, `{"type":"vless","server":"9.9.9.11"}`, "hk", "9.9.9.11", "sub-1")
	us := criteriaEntry(t, `{"type":"vless","server":"9.9.9.12"}`, "us", "9.9.9.12", "sub-1")
	hkDatacenter := criteriaEntry(t, `{"type":"vless","server":"9.9.9.12"}`, "hk", "9.9.9.12", "sub-1")
	hkTrojan := criteriaEntry(t, `{"type":"trojan","server":"9.9.9.11"}`, "hk", "9.9.9.11", "sub-1")

	entries := []*node.NodeEntry{hk, us, hkDatacenter, hkTrojan}
	plat.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		for _, entry := range entries {
			if !fn(entry.Hash, entry) {
				return
			}
		}
	}, subLookupAlwaysEnabled, usGeoLookup, nil)

	if plat.View().Size() != 1 {
		t.Fatalf("routable view size = %d, want 1 (only the hk+residential+vless node)", plat.View().Size())
	}
	if !plat.View().Contains(hk.Hash) {
		t.Fatal("the hk+residential+vless node is missing from the routable view")
	}
}

// TestMatchNodeCriteria_FailsClosedWithoutProjection documents the fail-closed
// rule of the assessment-backed criteria.
func TestMatchNodeCriteria_FailsClosedWithoutProjection(t *testing.T) {
	entry := criteriaEntry(t, `{"type":"vless","server":"9.9.9.13"}`, "us", "9.9.9.13", "sub-1")
	for _, criteria := range []NodeCriteria{
		{IPTypes: []string{"residential"}},
		{PurityBands: []string{"clean"}},
	} {
		ok, reason := MatchNodeCriteria(criteria, entry, subLookupAlwaysEnabled, nil, nil, nil, admitNow)
		if ok {
			t.Fatalf("criteria %+v admitted a node without an intel projection", criteria)
		}
		if reason == "" {
			t.Fatal("a rejected node must carry a reason")
		}
	}
}

// TestValidateNodeCriteria rejects values the inventory can never produce, so a
// criterion that silently selects nothing cannot be persisted.
func TestValidateNodeCriteria(t *testing.T) {
	if err := ValidateNodeCriteria(nil, nil, nil, nil); err != nil {
		t.Fatalf("empty criteria must be valid: %v", err)
	}
	if err := ValidateNodeCriteria([]string{"residential"}, []string{"clean"}, []string{"sub-1"}, []string{"vless"}); err != nil {
		t.Fatalf("known values must be valid: %v", err)
	}
	if err := ValidateNodeCriteria([]string{"hosting"}, nil, nil, nil); err == nil {
		t.Fatal("an unknown ip_type must be rejected")
	}
	if err := ValidateNodeCriteria(nil, []string{"Perfect"}, nil, nil); err == nil {
		t.Fatal("an unknown purity band must be rejected")
	}
	if err := ValidateNodeCriteria(nil, nil, []string{""}, nil); err == nil {
		t.Fatal("an empty subscription id must be rejected")
	}
	if err := ValidateNodeCriteria(nil, nil, nil, []string{"VLESS"}); err == nil {
		t.Fatal("an upper-case protocol must be rejected")
	}
}

// TestCompileRegexFilters_KeepsLegacyRuleKinds keeps the escape hatch honest:
// plain rules stay ANY, "*" stays MUST and "!" stays MUST_NOT.
func TestCompileRegexFilters_KeepsLegacyRuleKinds(t *testing.T) {
	compiled, err := CompileRegexFilters([]string{"hk", "*fast", "!slow"})
	if err != nil {
		t.Fatalf("CompileRegexFilters: %v", err)
	}
	if len(compiled.Any) != 1 || compiled.Any[0].String() != "hk" {
		t.Fatalf("plain rule kind changed: got %v", compiled.Any)
	}
	if len(compiled.Must) != 1 || compiled.Must[0].String() != "fast" {
		t.Fatalf("must rule kind changed: got %v", compiled.Must)
	}
	if len(compiled.MustNot) != 1 || compiled.MustNot[0].String() != "slow" {
		t.Fatalf("must-not rule kind changed: got %v", compiled.MustNot)
	}
	if _, err := CompileRegexFilters([]string{"["}); err == nil {
		t.Fatal("an invalid regex must be rejected")
	}
	if regexp.MustCompile("hk").String() != compiled.Any[0].String() {
		t.Fatal("compiled pattern differs from the rule text")
	}
}
