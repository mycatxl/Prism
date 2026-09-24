package main

import (
	"context"
	"net/netip"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/testutil"
)

// --- fixtures --------------------------------------------------------------

// newScopeFilterEntry builds the entry shape nodeFilter sees on the pool hot
// path: an outbound-ready node with a closed circuit, i.e. one IsHealthy
// reports true for.
func newScopeFilterEntry(t *testing.T, raw string) *node.NodeEntry {
	t.Helper()
	entry := node.NewNodeEntry(node.HashFromRawOptions([]byte(raw)), []byte(raw), time.Unix(0, 0), 0)
	markScopeFilterEntryHealthy(entry)
	return entry
}

func markScopeFilterEntryHealthy(entry *node.NodeEntry) {
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.CircuitOpenSince.Store(0)
}

// setScopeFilterAssessment projects one assessment of the WP10 §4 shape into
// the in-memory snapshot the purity keys read.
func setScopeFilterAssessment(snap *intel.Snapshot, ip, band, verdict, ipType string) {
	snap.SetAssessment(netip.MustParseAddr(ip), intel.AssessmentLite{
		Score:      90,
		Band:       intel.BandCode(band),
		Verdict:    intel.VerdictCode(verdict),
		IPType:     intel.IPTypeCode(ipType),
		Confidence: intel.ConfidenceCode("high"),
		State:      intel.StateValid,
		ValidUntil: time.Now().Add(time.Hour).UnixNano(),
	})
}

// --- per-key matching ------------------------------------------------------

// TestNodeFilter_ProtocolAndEngine pins the two pre-WP10 keys: the extension
// must not change what they match.
func TestNodeFilter_ProtocolAndEngine(t *testing.T) {
	entry := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)

	for _, tc := range []struct {
		name   string
		raw    map[string]string
		want   bool
		reason string
	}{
		{"protocol matches the outbound type", map[string]string{"protocol": "noop"}, true, "the noop fixture outbound reports Type()=noop"},
		{"protocol is trimmed and lowercased", map[string]string{"protocol": " NOOP "}, true, "values are normalized"},
		{"protocol mismatch", map[string]string{"protocol": "vless"}, false, ""},
		{"engine matches the kernel", map[string]string{"engine": "singbox"}, true, ""},
		{"engine mismatch", map[string]string{"engine": "mihomo"}, false, "sing-box is the only kernel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newNodeFilter(tc.raw, nil, nil).matches(entry); got != tc.want {
				t.Errorf("matches() = %v, want %v%s", got, tc.want, tc.reason)
			}
		})
	}
}

// TestNodeFilter_Region covers the three region sources: the explicit probe
// region, the GeoIP fallback and the unknown-region case.
func TestNodeFilter_Region(t *testing.T) {
	probeEntry := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)
	probeEntry.SetEgressIP(netip.MustParseAddr("203.0.113.4"))
	probeEntry.SetEgressRegion("JP")

	geoEntry := newScopeFilterEntry(t, `{"type":"ss","server":"2.2.2.2","port":443}`)
	geoEntry.SetEgressIP(netip.MustParseAddr("203.0.113.5"))

	unknownEntry := newScopeFilterEntry(t, `{"type":"ss","server":"3.3.3.3","port":443}`)

	geoLookup := func(ip netip.Addr) string {
		switch ip {
		case netip.MustParseAddr("203.0.113.4"), netip.MustParseAddr("203.0.113.5"):
			return "us"
		default:
			return ""
		}
	}

	tests := []struct {
		name   string
		value  string
		lookup func(netip.Addr) string
		entry  *node.NodeEntry
		want   bool
	}{
		{"explicit probe region matches, normalized", " jp ", geoLookup, probeEntry, true},
		{"explicit probe region wins over the GeoIP lookup", "us", geoLookup, probeEntry, false},
		{"GeoIP fallback matches when no probe region was stored", "us", geoLookup, geoEntry, true},
		{"GeoIP fallback mismatch", "jp", geoLookup, geoEntry, false},
		{"an unknown region never matches a region filter", "us", geoLookup, unknownEntry, false},
		{"without GeoIP only the explicit probe region matches", "us", nil, geoEntry, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filter := newNodeFilter(map[string]string{"region": tc.value}, tc.lookup, nil)
			if got := filter.matches(tc.entry); got != tc.want {
				t.Errorf("matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNodeFilter_PurityKeys covers ip_type, purity_band and verdict, including
// the fail-closed rule for a node without an assessment.
func TestNodeFilter_PurityKeys(t *testing.T) {
	entry := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))

	unassessed := newScopeFilterEntry(t, `{"type":"ss","server":"2.2.2.2","port":443}`)
	unassessed.SetEgressIP(netip.MustParseAddr("203.0.113.99"))

	snap := intel.NewSnapshot()
	setScopeFilterAssessment(snap, "203.0.113.10", "excellent", "favorable", "residential")

	for _, tc := range []struct {
		name  string
		raw   map[string]string
		entry *node.NodeEntry
		want  bool
	}{
		{"ip_type matches the projected name", map[string]string{"ip_type": "residential"}, entry, true},
		{"ip_type is normalized", map[string]string{"ip_type": " RESIDENTIAL "}, entry, true},
		{"ip_type mismatch", map[string]string{"ip_type": "datacenter"}, entry, false},
		{"unknown ip_type matches nothing", map[string]string{"ip_type": "nonsense"}, entry, false},
		{"purity_band matches", map[string]string{"purity_band": "excellent"}, entry, true},
		{"purity_band mismatch", map[string]string{"purity_band": "poor"}, entry, false},
		{"verdict matches", map[string]string{"verdict": "favorable"}, entry, true},
		{"verdict mismatch", map[string]string{"verdict": "high_risk"}, entry, false},
		{"verdict takes a comma-separated list", map[string]string{"verdict": "high_risk, FAVORABLE"}, entry, true},
		{"verdict list without a hit", map[string]string{"verdict": "high_risk,review"}, entry, false},
		{"unknown verdict matches nothing", map[string]string{"verdict": "unknown_verdict"}, entry, false},
		{"an unassessed node fails ip_type", map[string]string{"ip_type": "residential"}, unassessed, false},
		{"an unassessed node fails purity_band", map[string]string{"purity_band": "excellent"}, unassessed, false},
		{"an unassessed node fails verdict", map[string]string{"verdict": "favorable"}, unassessed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newNodeFilter(tc.raw, nil, snap).matches(tc.entry); got != tc.want {
				t.Errorf("matches() = %v, want %v", got, tc.want)
			}
		})
	}

	// Without the projection (intel subsystem not wired, or no snapshot) a
	// purity key matches nothing: the same fail-closed rule.
	if newNodeFilter(map[string]string{"ip_type": "residential"}, nil, nil).matches(entry) {
		t.Error("ip_type matched without an assessment projection, want no match")
	}
}

// TestNodeFilter_PurityBandReviewAlias pins the legacy "review" value of
// purity_band: like the node list it selects the review/conflicting verdicts.
func TestNodeFilter_PurityBandReviewAlias(t *testing.T) {
	review := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)
	review.SetEgressIP(netip.MustParseAddr("203.0.113.20"))
	conflicting := newScopeFilterEntry(t, `{"type":"ss","server":"2.2.2.2","port":443}`)
	conflicting.SetEgressIP(netip.MustParseAddr("203.0.113.21"))
	clean := newScopeFilterEntry(t, `{"type":"ss","server":"3.3.3.3","port":443}`)
	clean.SetEgressIP(netip.MustParseAddr("203.0.113.22"))

	snap := intel.NewSnapshot()
	setScopeFilterAssessment(snap, "203.0.113.20", "mixed", "review", "datacenter")
	setScopeFilterAssessment(snap, "203.0.113.21", "mixed", "conflicting", "datacenter")
	setScopeFilterAssessment(snap, "203.0.113.22", "clean", "favorable", "residential")

	filter := newNodeFilter(map[string]string{"purity_band": "review"}, nil, snap)
	if !filter.matches(review) {
		t.Error("purity_band=review must match verdict=review")
	}
	if !filter.matches(conflicting) {
		t.Error("purity_band=review must match verdict=conflicting")
	}
	if filter.matches(clean) {
		t.Error("purity_band=review must not match verdict=favorable")
	}

	// The alias is not a band name: a real band still matches by name.
	if !newNodeFilter(map[string]string{"purity_band": "clean"}, nil, snap).matches(clean) {
		t.Error("purity_band=clean must match the projected band")
	}
}

// TestNodeFilter_Healthy covers both values of the health switch.
func TestNodeFilter_Healthy(t *testing.T) {
	healthy := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)

	circuitOpen := newScopeFilterEntry(t, `{"type":"ss","server":"2.2.2.2","port":443}`)
	circuitOpen.CircuitOpenSince.Store(time.Now().UnixNano())

	noOutbound := newScopeFilterEntry(t, `{"type":"ss","server":"3.3.3.3","port":443}`)
	noOutbound.Outbound.Store(nil)

	on := newNodeFilter(map[string]string{"healthy": "true"}, nil, nil)
	if !on.matches(healthy) {
		t.Error("healthy=true must match a healthy node")
	}
	if on.matches(circuitOpen) {
		t.Error("healthy=true must not match a circuit-open node")
	}
	if on.matches(noOutbound) {
		t.Error("healthy=true must not match a node without an outbound")
	}

	// The literal "true" is normalized like every other value.
	if !newNodeFilter(map[string]string{"healthy": " TRUE "}, nil, nil).matches(healthy) {
		t.Error("healthy=TRUE must be normalized to true")
	}

	// Every other value, including an empty one, adds no constraint.
	for _, value := range []string{"", "false", "1", "yes"} {
		filter := newNodeFilter(map[string]string{"healthy": value}, nil, nil)
		for _, entry := range []*node.NodeEntry{healthy, circuitOpen, noOutbound} {
			if !filter.matches(entry) {
				t.Errorf("healthy=%q must not filter, but it rejected a node", value)
			}
		}
	}
}

// TestNodeFilter_CombinedConditions checks that every key of one filter has to
// match the same node.
func TestNodeFilter_CombinedConditions(t *testing.T) {
	entry := newScopeFilterEntry(t, `{"type":"ss","server":"1.1.1.1","port":443}`)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.30"))
	entry.SetEgressRegion("US")

	snap := intel.NewSnapshot()
	setScopeFilterAssessment(snap, "203.0.113.30", "excellent", "favorable", "residential")

	all := map[string]string{
		"protocol":    "noop",
		"engine":      "singbox",
		"region":      "us",
		"healthy":     "true",
		"ip_type":     "residential",
		"purity_band": "excellent",
		"verdict":     "favorable",
	}
	if !newNodeFilter(all, nil, snap).matches(entry) {
		t.Fatal("the full filter must match a node that satisfies every key")
	}

	for key, value := range map[string]string{
		"protocol":    "vless",
		"engine":      "mihomo",
		"region":      "jp",
		"ip_type":     "datacenter",
		"purity_band": "poor",
		"verdict":     "high_risk",
	} {
		failing := make(map[string]string, len(all))
		for k, v := range all {
			failing[k] = v
		}
		failing[key] = value
		if newNodeFilter(failing, nil, snap).matches(entry) {
			t.Errorf("a node must be rejected when %s=%q does not match", key, value)
		}
	}

	// An unhealthy node fails a combination that asks for healthy=true even
	// when every other key matches.
	entry.CircuitOpenSince.Store(time.Now().UnixNano())
	if newNodeFilter(all, nil, snap).matches(entry) {
		t.Error("the combination must reject an unhealthy node")
	}
}

// TestIntelScopeLookupAccessorsDegradeWhenServicesAreMissing pins the nil
// paths: an app without the intel or GeoIP services compiles a filter that
// matches no purity key and uses only the explicit probe region.
func TestIntelScopeLookupAccessorsDegradeWhenServicesAreMissing(t *testing.T) {
	app := &prismApp{}
	if app.intelScopeGeoLookup() != nil {
		t.Error("intelScopeGeoLookup without a GeoIP service must be nil")
	}
	if app.intelScopeSnapshot() != nil {
		t.Error("intelScopeSnapshot without an intel service must be nil")
	}

	var nilApp *prismApp
	if nilApp.intelScopeGeoLookup() != nil || nilApp.intelScopeSnapshot() != nil {
		t.Error("the accessors must be nil-receiver safe")
	}
}

// --- scope expansion -------------------------------------------------------

// TestResolveIntelScope_ExpandsWp10FilterKeys drives the whole expansion:
// a real pool plus the intel projection, filtered by the §4 keys. It pins both
// the wiring of newNodeFilter and the union semantics of the scope (explicit
// node_hashes are never filtered).
func TestResolveIntelScope_ExpandsWp10FilterKeys(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
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

	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())

	rawA := `{"type":"ss","server":"1.1.1.1","port":443}`
	rawB := `{"type":"ss","server":"2.2.2.2","port":443}`
	rawC := `{"type":"ss","server":"3.3.3.3","port":443}`
	rawD := `{"type":"ss","server":"4.4.4.4","port":443}`
	hashA := node.HashFromRawOptions([]byte(rawA))
	hashB := node.HashFromRawOptions([]byte(rawB))
	hashC := node.HashFromRawOptions([]byte(rawC))
	hashD := node.HashFromRawOptions([]byte(rawD))
	for hash, raw := range map[node.Hash]string{hashA: rawA, hashB: rawB, hashC: rawC, hashD: rawD} {
		pool.AddNodeFromSub(hash, []byte(raw), "sub-scope-filter")
	}

	entryA, ok := pool.GetEntry(hashA)
	if !ok {
		t.Fatalf("node A %s missing after add", hashA.Hex())
	}
	markScopeFilterEntryHealthy(entryA)
	entryA.SetEgressIP(netip.MustParseAddr("203.0.113.40"))
	entryA.SetEgressRegion("us")

	entryB, ok := pool.GetEntry(hashB)
	if !ok {
		t.Fatalf("node B %s missing after add", hashB.Hex())
	}
	markScopeFilterEntryHealthy(entryB)
	entryB.CircuitOpenSince.Store(time.Now().UnixNano()) // unhealthy
	entryB.SetEgressIP(netip.MustParseAddr("203.0.113.41"))
	entryB.SetEgressRegion("jp")

	entryC, ok := pool.GetEntry(hashC)
	if !ok {
		t.Fatalf("node C %s missing after add", hashC.Hex())
	}
	markScopeFilterEntryHealthy(entryC)
	entryC.SetEgressIP(netip.MustParseAddr("203.0.113.42"))
	entryC.SetEgressRegion("us")
	// Node C deliberately stays unassessed.

	entryD, ok := pool.GetEntry(hashD)
	if !ok {
		t.Fatalf("node D %s missing after add", hashD.Hex())
	}
	markScopeFilterEntryHealthy(entryD)
	entryD.SetEgressIP(netip.MustParseAddr("203.0.113.43"))
	entryD.SetEgressRegion("de")

	setScopeFilterAssessment(svc.Snapshot(), "203.0.113.40", "excellent", "favorable", "residential")
	setScopeFilterAssessment(svc.Snapshot(), "203.0.113.41", "poor", "high_risk", "datacenter")
	setScopeFilterAssessment(svc.Snapshot(), "203.0.113.43", "mixed", "conflicting", "datacenter")

	app := &prismApp{intelSvc: svc, topoRuntime: &topologyRuntime{pool: pool}}

	wantHashes := func(hashes ...node.Hash) []string {
		out := make([]string, 0, len(hashes))
		for _, hash := range hashes {
			out = append(out, hash.String())
		}
		sort.Strings(out)
		return out
	}

	for _, tc := range []struct {
		name  string
		scope jobs.Scope
		want  []string
	}{
		{
			"region plus healthy",
			jobs.Scope{Filter: map[string]string{"region": "us", "healthy": "true"}},
			wantHashes(hashA, hashC),
		},
		{
			"region without healthy keeps the unhealthy node",
			jobs.Scope{Filter: map[string]string{"region": "jp"}},
			wantHashes(hashB),
		},
		{
			"ip_type reads the projection",
			jobs.Scope{Filter: map[string]string{"ip_type": "residential"}},
			wantHashes(hashA),
		},
		{
			"purity_band does not imply healthy",
			jobs.Scope{Filter: map[string]string{"purity_band": "poor"}},
			wantHashes(hashB),
		},
		{
			"verdict takes a comma-separated list",
			jobs.Scope{Filter: map[string]string{"verdict": "high_risk,review"}},
			wantHashes(hashB),
		},
		{
			"purity_band review alias selects the conflicting verdict",
			jobs.Scope{Filter: map[string]string{"purity_band": "review"}},
			wantHashes(hashD),
		},
		{
			"a combination narrows to the single matching node",
			jobs.Scope{Filter: map[string]string{
				"region": "us", "healthy": "true", "ip_type": "residential",
				"purity_band": "excellent", "verdict": "favorable", "protocol": "noop",
			}},
			wantHashes(hashA),
		},
		{
			"a filter that matches nothing resolves to an empty scope",
			jobs.Scope{Filter: map[string]string{"healthy": "true", "verdict": "high_risk"}},
			[]string{},
		},
		{
			"node_hashes stay a union with the filter",
			jobs.Scope{
				NodeHashes: []string{hashB.String()},
				Filter:     map[string]string{"region": "us", "healthy": "true"},
			},
			wantHashes(hashA, hashB, hashC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := app.resolveIntelScope(context.Background(), tc.scope)
			if err != nil {
				t.Fatalf("resolveIntelScope: %v", err)
			}
			if len(got) == 0 {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolveIntelScope() = %v, want %v", got, tc.want)
			}
		})
	}
}
