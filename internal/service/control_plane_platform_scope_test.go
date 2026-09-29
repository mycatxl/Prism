package service

import (
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
)

// The explicit node-selection criteria are ANDed with each other and the values
// inside one criterion are alternatives. These tests pin that contract at the
// service boundary: the live preview the form shows, the option lists the form
// offers, and the create/update round trip that persists them.

// makeFixtureNodesRoutable clears the initial circuit break and records one
// latency sample on every pool node of the preview fixture, so the preview
// answers the "what would the platform load" question instead of failing closed
// on the runtime health gates.
func makeFixtureNodesRoutable(t *testing.T, cp *ControlPlaneService) {
	t.Helper()
	now := time.Now()
	cp.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		entry.CircuitOpenSince.Store(0)
		if entry.LatencyTable != nil {
			entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
				Ewma:        100 * time.Millisecond,
				LastUpdated: now,
			})
		}
		return true
	})
}

func TestPreviewPlatformScope_AndSemantics(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)
	makeFixtureNodesRoutable(t, fixture.cp)

	cases := []struct {
		name     string
		spec     *PlatformSpecFilter
		want     int
		wantHash string
	}{
		{
			name: "single_criterion_selects_its_region",
			spec: &PlatformSpecFilter{RegexFilters: []string{".*"}, RegionFilters: []string{"hk"}},
			want: 1, wantHash: fixture.hkHash,
		},
		{
			name: "region_values_are_alternatives",
			spec: &PlatformSpecFilter{RegexFilters: []string{".*"}, RegionFilters: []string{"hk", "us"}},
			want: 2,
		},
		{
			name: "protocol_criterion_combines_with_region_under_and",
			spec: &PlatformSpecFilter{
				RegexFilters:  []string{".*"},
				RegionFilters: []string{"hk"},
				Protocols:     []string{"ss"},
			},
			want: 1, wantHash: fixture.hkHash,
		},
		{
			// This is the case an OR implementation would answer with 1: the
			// region criterion matches a node, the protocol criterion matches
			// none, and under AND the intersection is empty.
			name: "cross_product_with_no_intersection_admits_nothing",
			spec: &PlatformSpecFilter{
				RegexFilters:  []string{".*"},
				RegionFilters: []string{"hk"},
				Protocols:     []string{"trojan"},
			},
			want: 0,
		},
		{
			// The fixture nodes carry no intel assessment, so an assessment-backed
			// criterion fails closed instead of matching everything.
			name: "ip_type_criterion_fails_closed_without_an_assessment",
			spec: &PlatformSpecFilter{
				RegexFilters:  []string{".*"},
				RegionFilters: []string{"hk"},
				IPTypes:       []string{"residential"},
			},
			want: 0,
		},
		{
			name: "purity_band_criterion_fails_closed_without_an_assessment",
			spec: &PlatformSpecFilter{
				RegexFilters: []string{".*"},
				PurityBands:  []string{"clean"},
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := fixture.cp.PreviewPlatformScope(PreviewFilterRequest{PlatformSpec: tc.spec})
			if err != nil {
				t.Fatalf("PreviewPlatformScope: %v", err)
			}
			if result.Matched != tc.want {
				t.Fatalf("matched = %d, want %d (excluded_by=%v)", result.Matched, tc.want, result.ExcludedBy)
			}
			if result.Truncated {
				t.Fatal("a three-node pool must never truncate the preview")
			}
			if tc.wantHash != "" {
				if len(result.Sample) != 1 || result.Sample[0].NodeHash != tc.wantHash {
					t.Fatalf("sample = %+v, want only %s", result.Sample, tc.wantHash)
				}
			}
		})
	}
}

func TestPreviewPlatformScope_SubscriptionCriterion(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)
	makeFixtureNodesRoutable(t, fixture.cp)

	// The fixture's nodes all belong to sub-1, so the criterion admits all three.
	all, err := fixture.cp.PreviewPlatformScope(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{RegexFilters: []string{".*"}, SubscriptionFilters: []string{"sub-1"}},
	})
	if err != nil {
		t.Fatalf("PreviewPlatformScope: %v", err)
	}
	if all.Matched != 3 {
		t.Fatalf("matched = %d, want 3 for the owning subscription", all.Matched)
	}

	// An unrelated subscription admits nothing, and the counter names the reason.
	none, err := fixture.cp.PreviewPlatformScope(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{RegexFilters: []string{".*"}, SubscriptionFilters: []string{"sub-other"}},
	})
	if err != nil {
		t.Fatalf("PreviewPlatformScope: %v", err)
	}
	if none.Matched != 0 {
		t.Fatalf("matched = %d, want 0 for an unrelated subscription", none.Matched)
	}
	if none.ExcludedBy[platform.ReasonSubscription] != 3 {
		t.Fatalf("excluded_by = %v, want %d %s", none.ExcludedBy, 3, platform.ReasonSubscription)
	}
}

func TestPreviewPlatformScope_RejectsUnknownCriteriaValues(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	cases := map[string]*PlatformSpecFilter{
		"unknown ip type":  {IPTypes: []string{"hosting"}},
		"unknown band":     {PurityBands: []string{"perfect"}},
		"bad protocol":     {Protocols: []string{"VLESS"}},
		"bad region":       {RegionFilters: []string{"HONGKONG"}},
		"empty sub filter": {SubscriptionFilters: []string{""}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.cp.PreviewPlatformScope(PreviewFilterRequest{PlatformSpec: spec}); err == nil {
				t.Fatal("an unknown criterion value must be rejected, not silently matched against nothing")
			}
		})
	}
}

func TestPlatformNodeFacets_ListsInventoryValues(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	facets, err := fixture.cp.PlatformNodeFacets()
	if err != nil {
		t.Fatalf("PlatformNodeFacets: %v", err)
	}
	if facets.TotalNodes != 3 || facets.Scanned != 3 || facets.Truncated {
		t.Fatalf("facets inventory = %d/%d truncated=%v, want 3/3/false", facets.TotalNodes, facets.Scanned, facets.Truncated)
	}
	wantRegions := map[string]bool{"hk": true, "us": true}
	for _, region := range facets.Regions {
		delete(wantRegions, region)
	}
	if len(wantRegions) != 0 {
		t.Fatalf("regions = %v, missing %v", facets.Regions, wantRegions)
	}
	if len(facets.Protocols) != 1 || facets.Protocols[0] != "ss" {
		t.Fatalf("protocols = %v, want [ss] (the value the pool actually carries)", facets.Protocols)
	}
	if len(facets.Subscriptions) != 1 || facets.Subscriptions[0].ID != "sub-1" {
		t.Fatalf("subscriptions = %+v, want sub-1", facets.Subscriptions)
	}
	if facets.Subscriptions[0].NodeCount != 3 {
		t.Fatalf("sub-1 node_count = %d, want 3", facets.Subscriptions[0].NodeCount)
	}
}

func TestCreateAndUpdatePlatform_PersistNodeCriteria(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	name := "criteria-round-trip"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                &name,
		IPTypes:             []string{"residential", "mobile"},
		PurityBands:         []string{"clean"},
		SubscriptionFilters: []string{"sub-1"},
		Protocols:           []string{"vless"},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if len(created.IPTypes) != 2 || created.PurityBands[0] != "clean" ||
		created.SubscriptionFilters[0] != "sub-1" || created.Protocols[0] != "vless" {
		t.Fatalf("create response dropped criteria: %+v", created)
	}

	// The persisted row must carry them, and the runtime platform must apply them
	// (the routable-view rebuild reads the runtime object, not the model).
	stored, err := cp.Engine.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	if len(stored.IPTypes) != 2 || stored.PurityBands[0] != "clean" ||
		stored.SubscriptionFilters[0] != "sub-1" || stored.Protocols[0] != "vless" {
		t.Fatalf("persisted row dropped criteria: %+v", stored)
	}
	runtime, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatalf("platform %s missing from the pool", created.ID)
	}
	if len(runtime.IPTypes) != 2 || runtime.PurityBands[0] != "clean" ||
		len(runtime.SubscriptionFilters) != 1 || runtime.Protocols[0] != "vless" {
		t.Fatalf("runtime platform dropped criteria: %+v", runtime)
	}

	// A patch replaces one criterion and leaves the others alone.
	patched, err := cp.UpdatePlatform(created.ID, []byte(`{"ip_types":["datacenter"],"purity_bands":[]}`))
	if err != nil {
		t.Fatalf("UpdatePlatform: %v", err)
	}
	if len(patched.IPTypes) != 1 || patched.IPTypes[0] != "datacenter" {
		t.Fatalf("patched ip_types = %v, want [datacenter]", patched.IPTypes)
	}
	if len(patched.PurityBands) != 0 {
		t.Fatalf("patched purity_bands = %v, want empty (an empty list clears the criterion)", patched.PurityBands)
	}
	if len(patched.Protocols) != 1 || patched.Protocols[0] != "vless" {
		t.Fatalf("patch dropped an untouched criterion: %v", patched.Protocols)
	}

	// An unknown value is rejected before it can reach the store.
	_, err = cp.UpdatePlatform(created.ID, []byte(`{"protocols":["VLESS"]}`))
	svcErr, ok := err.(*ServiceError)
	if !ok || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("invalid protocol error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestUpdatePlatform_KeepsLegacyRegexFiltersWorking(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	name := "legacy-regex"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:         &name,
		RegexFilters: []string{`^sub-1/hk`, `!slow`},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if len(created.RegexFilters) != 2 {
		t.Fatalf("regex_filters = %v, want the two rules back", created.RegexFilters)
	}

	// A patch that only touches the new criteria must not disturb the legacy
	// rules: they keep filtering for platforms written before the new fields
	// existed.
	patched, err := cp.UpdatePlatform(created.ID, []byte(`{"purity_bands":["clean"]}`))
	if err != nil {
		t.Fatalf("UpdatePlatform: %v", err)
	}
	if len(patched.RegexFilters) != 2 || patched.RegexFilters[0] != `^sub-1/hk` {
		t.Fatalf("regex_filters changed by an unrelated patch: %v", patched.RegexFilters)
	}
	runtime, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatalf("platform %s missing from the pool", created.ID)
	}
	if runtime.RegexFilters.Empty() {
		t.Fatal("runtime platform lost its compiled legacy rules")
	}
}
