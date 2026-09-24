package service

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
	"prism/internal/topology"
)

// WP10 §4: the node list intel field, its filters and the preview excluded_by
// counters. Every assessment here comes from the in-memory projection; no test
// touches the network.

func intelTestIntPtr(value int) *int       { return &value }
func intelTestStrPtr(value string) *string { return &value }
func intelTestBoolPtr(value bool) *bool    { return &value }

// newIntelProjectionForTest opens a throw-away intel.db and returns the service
// whose in-memory projection the node list reads.
func newIntelProjectionForTest(t *testing.T) *intel.Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("open intel store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc, err := intel.NewService(intel.Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("intel.NewService: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

type intelNodeFixture struct {
	cp     *ControlPlaneService
	svc    *intel.Service
	clean  node.Hash
	poor   node.Hash
	absent node.Hash
	now    time.Time
}

// buildIntelNodeFixture builds three nodes: one clean assessment, one poor
// assessment and one without any assessment at all.
func buildIntelNodeFixture(t *testing.T) intelNodeFixture {
	t.Helper()

	svc := newIntelProjectionForTest(t)
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	sub := subscription.NewSubscription("sub-intel", "sub-intel", "https://example.com/a", true, false)
	subMgr.Register(sub)

	add := func(server, egress, tag string) node.Hash {
		raw := []byte(`{"type":"ss","server":"` + server + `","port":443}`)
		hash := node.HashFromRawOptions(raw)
		pool.AddNodeFromSub(hash, raw, sub.ID)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{tag}})
		entry, ok := pool.GetEntry(hash)
		if !ok {
			t.Fatalf("node %s missing after add", hash.Hex())
		}
		entry.SetEgressIP(netip.MustParseAddr(egress))
		return hash
	}

	clean := add("1.1.1.1", "203.0.113.10", "clean")
	poor := add("2.2.2.2", "203.0.113.11", "poor")
	absent := add("3.3.3.3", "203.0.113.12", "absent")

	now := time.Now().UTC()
	validUntil := now.Add(time.Hour).UnixNano()
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score:      95,
		Band:       intel.BandExcellent,
		Verdict:    intel.VerdictFavorable,
		IPType:     intel.IPTypeDatacenter,
		Confidence: intel.ConfidenceHigh,
		State:      intel.StateValid,
		Native:     1,
		Flags:      intel.FlagProxy,
		ASN:        13335,
		ASOrg:      "Cloudflare",
		Country:    "JP",
		City:       "Tokyo",
		ValidUntil: validUntil,
		ComputedAt: now.Add(-time.Minute).UnixNano(),
	})
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.11"), intel.AssessmentLite{
		Score:      40,
		Band:       intel.BandPoor,
		Verdict:    intel.VerdictHighRisk,
		IPType:     intel.IPTypeResidential,
		Confidence: intel.ConfidenceLow,
		State:      intel.StateValid,
		Native:     0,
		Flags:      intel.FlagAbuse,
		ASN:        64500,
		ASOrg:      "Example ISP",
		Country:    "US",
		City:       "Dallas",
		ValidUntil: validUntil,
		ComputedAt: now.Add(-2 * time.Hour).UnixNano(),
	})
	svc.Snapshot().SetCheck(clean.Hex(), "chatgpt", intel.OutcomeLite{
		Outcome:    intel.OutcomeAvailable,
		ValidUntil: validUntil,
		ObservedAt: now.UnixNano(),
	})
	svc.Snapshot().SetCheck(poor.Hex(), "chatgpt", intel.OutcomeLite{
		Outcome:    intel.OutcomeBlocked,
		ValidUntil: validUntil,
		ObservedAt: now.UnixNano(),
	})

	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr, Intel: svc}
	return intelNodeFixture{cp: cp, svc: svc, clean: clean, poor: poor, absent: absent, now: now}
}

func listNodeHashesByHash(t *testing.T, cp *ControlPlaneService, filters NodeFilters) map[string]NodeSummary {
	t.Helper()
	nodes, err := cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	out := make(map[string]NodeSummary, len(nodes))
	for _, summary := range nodes {
		out[summary.NodeHash] = summary
	}
	return out
}

func TestListNodes_IntelFieldForUnassessedNodeIsExplicitlyEmpty(t *testing.T) {
	fixture := buildIntelNodeFixture(t)
	byHash := listNodeHashesByHash(t, fixture.cp, NodeFilters{})

	unassessed, ok := byHash[fixture.absent.Hex()]
	if !ok {
		t.Fatalf("unassessed node %s missing from the list", fixture.absent.Hex())
	}
	if unassessed.Intel.State != IntelStateUnassessed {
		t.Errorf("state = %q, want %q", unassessed.Intel.State, IntelStateUnassessed)
	}
	if unassessed.Intel.PurityScore != nil {
		t.Errorf("purity score = %v, want an explicit null", *unassessed.Intel.PurityScore)
	}
	if unassessed.Intel.PurityBand != "unknown" || unassessed.Intel.Confidence != "none" ||
		unassessed.Intel.Verdict != "pending" || unassessed.Intel.IPType != "unknown" {
		t.Errorf("unassessed defaults = %+v", unassessed.Intel)
	}
	if unassessed.Intel.Native != nil {
		t.Errorf("native = %v, want nil", *unassessed.Intel.Native)
	}
	if len(unassessed.Intel.Flags) != 0 || len(unassessed.Intel.Checks) != 0 {
		t.Errorf("flags/checks = %v/%v, want empty", unassessed.Intel.Flags, unassessed.Intel.Checks)
	}
	if unassessed.Intel.AssessedAt != "" || unassessed.Intel.AssessedAtNs != 0 {
		t.Errorf("assessed_at = %q/%d, want empty", unassessed.Intel.AssessedAt, unassessed.Intel.AssessedAtNs)
	}
	if unassessed.Intel.ASN != 0 || unassessed.Intel.Country != "" || unassessed.Intel.City != "" {
		t.Errorf("asn/country/city = %d/%q/%q, want empty", unassessed.Intel.ASN, unassessed.Intel.Country, unassessed.Intel.City)
	}
}

func TestListNodes_IntelFieldForAssessedNode(t *testing.T) {
	fixture := buildIntelNodeFixture(t)
	byHash := listNodeHashesByHash(t, fixture.cp, NodeFilters{})

	assessed, ok := byHash[fixture.clean.Hex()]
	if !ok {
		t.Fatalf("assessed node %s missing from the list", fixture.clean.Hex())
	}
	intelView := assessed.Intel
	if intelView.State != IntelStateValid {
		t.Fatalf("state = %q, want %q", intelView.State, IntelStateValid)
	}
	if intelView.PurityScore == nil || *intelView.PurityScore != 95 {
		t.Fatalf("purity score = %v, want 95", intelView.PurityScore)
	}
	if intelView.PurityBand != "excellent" || intelView.Confidence != "high" ||
		intelView.Verdict != "favorable" || intelView.IPType != "datacenter" {
		t.Errorf("assessment names = %+v", intelView)
	}
	if intelView.Native == nil || !*intelView.Native {
		t.Errorf("native = %v, want true", intelView.Native)
	}
	if intelView.ASN != 13335 || intelView.ASOrg != "Cloudflare" || intelView.Country != "JP" || intelView.City != "Tokyo" {
		t.Errorf("asn facts = %+v", intelView)
	}
	if len(intelView.Flags) != 1 || intelView.Flags[0] != "proxy" {
		t.Errorf("flags = %v, want [proxy]", intelView.Flags)
	}
	if intelView.Checks["chatgpt"] != "available" {
		t.Errorf("checks = %v, want chatgpt=available", intelView.Checks)
	}
	wantAssessedAt := fixture.now.Add(-time.Minute).UnixNano()
	if intelView.AssessedAtNs != wantAssessedAt || intelView.AssessedAt == "" {
		t.Errorf("assessed_at = %q/%d, want %d", intelView.AssessedAt, intelView.AssessedAtNs, wantAssessedAt)
	}
}

func TestListNodes_IntelStateIsStaleAfterValidUntil(t *testing.T) {
	fixture := buildIntelNodeFixture(t)
	now := time.Now().UTC().UnixNano()
	fixture.svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.12"), intel.AssessmentLite{
		Score:      88,
		Band:       intel.BandFair,
		Verdict:    intel.VerdictCaution,
		IPType:     intel.IPTypeBusiness,
		Confidence: intel.ConfidenceMedium,
		State:      intel.StateValid,
		Native:     -1,
		ValidUntil: now - int64(time.Minute),
		ComputedAt: now - int64(2*time.Hour),
	})

	byHash := listNodeHashesByHash(t, fixture.cp, NodeFilters{})
	stale := byHash[fixture.absent.Hex()]
	if stale.Intel.State != IntelStateStale {
		t.Fatalf("state = %q, want %q", stale.Intel.State, IntelStateStale)
	}
	if stale.Intel.PurityScore == nil || *stale.Intel.PurityScore != 88 {
		t.Errorf("score = %v, want 88 (the stored value, reported stale)", stale.Intel.PurityScore)
	}
}

func TestListNodes_IntelFilters(t *testing.T) {
	fixture := buildIntelNodeFixture(t)

	tests := []struct {
		name    string
		filters NodeFilters
		want    []string
	}{
		{
			name:    "purity_min keeps only the clean node",
			filters: NodeFilters{PurityMin: intelTestIntPtr(90)},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name:    "purity_max keeps only the poor node",
			filters: NodeFilters{PurityMax: intelTestIntPtr(60)},
			want:    []string{fixture.poor.Hex()},
		},
		{
			name:    "purity range",
			filters: NodeFilters{PurityMin: intelTestIntPtr(0), PurityMax: intelTestIntPtr(100)},
			want:    []string{fixture.clean.Hex(), fixture.poor.Hex()},
		},
		{
			name:    "verdict allow list",
			filters: NodeFilters{Verdicts: []string{"high_risk"}},
			want:    []string{fixture.poor.Hex()},
		},
		{
			name:    "confidence_min medium",
			filters: NodeFilters{ConfidenceMin: intelTestStrPtr("medium")},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name:    "native true",
			filters: NodeFilters{Native: intelTestBoolPtr(true)},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name:    "native false",
			filters: NodeFilters{Native: intelTestBoolPtr(false)},
			want:    []string{fixture.poor.Hex()},
		},
		{
			name:    "asn",
			filters: NodeFilters{ASN: intelTestIntPtr(64500)},
			want:    []string{fixture.poor.Hex()},
		},
		{
			name:    "country is case insensitive",
			filters: NodeFilters{Country: intelTestStrPtr("jp")},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name:    "check outcome",
			filters: NodeFilters{Checks: []NodeCheckFilter{{ID: "chatgpt", Outcome: "available"}}},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name: "check outcome on another node",
			filters: NodeFilters{Checks: []NodeCheckFilter{
				{ID: "chatgpt", Outcome: "available"},
				{ID: "netflix", Outcome: "available"},
			}},
			want: nil,
		},
		{
			name:    "ip_type reads the projection",
			filters: NodeFilters{IPType: intelTestStrPtr("residential")},
			want:    []string{fixture.poor.Hex()},
		},
		{
			name:    "purity_band reads the projection",
			filters: NodeFilters{PurityBand: intelTestStrPtr("excellent")},
			want:    []string{fixture.clean.Hex()},
		},
		{
			name:    "purity_band review maps to the verdict",
			filters: NodeFilters{PurityBand: intelTestStrPtr("review")},
			want:    nil,
		},
		{
			name: "no filter returns every node",
			want: []string{fixture.clean.Hex(), fixture.poor.Hex(), fixture.absent.Hex()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			byHash := listNodeHashesByHash(t, fixture.cp, tt.filters)
			if len(byHash) != len(tt.want) {
				t.Fatalf("matched %d nodes, want %d (%v)", len(byHash), len(tt.want), byHash)
			}
			for _, want := range tt.want {
				if _, ok := byHash[want]; !ok {
					t.Fatalf("expected %s in %v", want, byHash)
				}
			}
		})
	}
}

func TestListNodes_UnassessedNodeNeverSatisfiesAnIntelFilter(t *testing.T) {
	fixture := buildIntelNodeFixture(t)

	for name, filters := range map[string]NodeFilters{
		"purity_min":     {PurityMin: intelTestIntPtr(0)},
		"verdict":        {Verdicts: []string{"pending"}},
		"confidence_min": {ConfidenceMin: intelTestStrPtr("low")},
		"native":         {Native: intelTestBoolPtr(false)},
		"asn":            {ASN: intelTestIntPtr(1)},
		"country":        {Country: intelTestStrPtr("JP")},
		"check":          {Checks: []NodeCheckFilter{{ID: "chatgpt", Outcome: "available"}}},
	} {
		byHash := listNodeHashesByHash(t, fixture.cp, filters)
		if _, ok := byHash[fixture.absent.Hex()]; ok {
			t.Errorf("%s: unassessed node must not match (matched %v)", name, byHash)
		}
	}
}

func TestPreviewFilterReport_ExcludedByCounters(t *testing.T) {
	fixture := buildIntelNodeFixture(t)

	report, err := fixture.cp.PreviewFilterReport(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters: []string{".*"},
		},
		// exclude_high_risk stays off so the poor node is caught by the purity
		// rule instead of by the (default on) high-risk rule.
		QualityPolicy: &model.QualityPolicy{
			MinPurity:       intelTestIntPtr(80),
			ExcludeHighRisk: intelTestBoolPtr(false),
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilterReport: %v", err)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].NodeHash != fixture.clean.Hex() {
		t.Fatalf("kept nodes = %+v, want only %s (%v)", report.Nodes, fixture.clean.Hex(), report.ExcludedBy)
	}
	if report.ExcludedBy["QUALITY_MIN_PURITY"] != 1 {
		t.Errorf("QUALITY_MIN_PURITY = %d, want 1 (%v)", report.ExcludedBy["QUALITY_MIN_PURITY"], report.ExcludedBy)
	}
	if report.ExcludedBy["QUALITY_UNKNOWN"] != 1 {
		t.Errorf("QUALITY_UNKNOWN = %d, want 1 (%v)", report.ExcludedBy["QUALITY_UNKNOWN"], report.ExcludedBy)
	}
	if _, ok := report.ExcludedBy["regex"]; ok {
		t.Errorf("regex counter must be absent: %v", report.ExcludedBy)
	}
}

func TestPreviewFilterReport_RegexAndRegionCounters(t *testing.T) {
	fixture := buildIntelNodeFixture(t)

	report, err := fixture.cp.PreviewFilterReport(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters:  []string{"clean", "absent"},
			RegionFilters: []string{"!jp"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilterReport: %v", err)
	}
	if report.ExcludedBy[PreviewExcludedRegex] != 1 {
		t.Errorf("regex = %d, want 1 (%v)", report.ExcludedBy[PreviewExcludedRegex], report.ExcludedBy)
	}
	// Both regex matches have no observed region, and an unknown region never
	// satisfies a configured region filter.
	if report.ExcludedBy[PreviewExcludedRegion] != 2 {
		t.Errorf("region = %d, want 2 (%v)", report.ExcludedBy[PreviewExcludedRegion], report.ExcludedBy)
	}
	if len(report.Nodes) != 0 {
		t.Errorf("kept nodes = %+v, want none", report.Nodes)
	}
	// An empty policy excludes nobody, so only the two filter counters exist.
	if len(report.ExcludedBy) != 2 {
		t.Errorf("excluded_by = %v, want only the regex and region counters", report.ExcludedBy)
	}
}

func TestPreviewFilterReport_PlatformPolicyIsUsedWhenRequestHasNone(t *testing.T) {
	fixture := buildIntelNodeFixture(t)

	plat := platform.NewPlatformWithTagFilter("plat-intel", "plat", node.TagFilter{}, nil)
	plat.QualityPolicy = model.QualityPolicy{MinPurity: intelTestIntPtr(80), ExcludeHighRisk: intelTestBoolPtr(false)}
	fixture.cp.Pool.RegisterPlatform(plat)
	fixture.cp.Pool.RebuildPlatform(plat)

	report, err := fixture.cp.PreviewFilterReport(PreviewFilterRequest{PlatformID: &plat.ID})
	if err != nil {
		t.Fatalf("PreviewFilterReport: %v", err)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].NodeHash != fixture.clean.Hex() {
		t.Fatalf("kept nodes = %+v, want only %s", report.Nodes, fixture.clean.Hex())
	}
	if report.ExcludedBy["QUALITY_MIN_PURITY"] != 1 || report.ExcludedBy["QUALITY_UNKNOWN"] != 1 {
		t.Errorf("excluded_by = %v, want one QUALITY_MIN_PURITY and one QUALITY_UNKNOWN", report.ExcludedBy)
	}
}
