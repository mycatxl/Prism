package api

import (
	"context"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/service"
	"prism/internal/subscription"
)

// WP10 §4 API surface: the `intel` node field, its filters, the new sort keys
// and the preview-filter excluded_by counters. Everything is served through
// httptest, no test touches the network.

func wireIntelForTest(t *testing.T, cp *service.ControlPlaneService) *intel.Service {
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
	cp.Intel = svc
	return svc
}

func nodeItems(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items = %T, want []any", body["items"])
	}
	out := make(map[string]map[string]any, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item = %T, want map", item)
		}
		hash, _ := entry["node_hash"].(string)
		out[hash] = entry
	}
	return out
}

func intelObject(t *testing.T, item map[string]any) map[string]any {
	t.Helper()
	value, ok := item["intel"].(map[string]any)
	if !ok {
		t.Fatalf("intel = %v (%T), want an object", item["intel"], item["intel"])
	}
	return value
}

func TestHandleListNodes_IntelFieldShape(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)

	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "sub-a", "https://example.com/a", true, false)
	cp.SubMgr.Register(sub)

	const assessedRaw = `{"type":"ss","server":"1.1.1.1","port":443}`
	const unassessedRaw = `{"type":"ss","server":"2.2.2.2","port":443}`
	assessedHash := node.HashFromRawOptions([]byte(assessedRaw))
	unassessedHash := node.HashFromRawOptions([]byte(unassessedRaw))
	addNodeForNodeListTest(t, cp, sub, assessedRaw, "203.0.113.10")
	addNodeForNodeListTest(t, cp, sub, unassessedRaw, "203.0.113.11")

	now := time.Now().UTC()
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score:      91,
		Band:       intel.BandClean,
		Verdict:    intel.VerdictCaution,
		IPType:     intel.IPTypeDatacenter,
		Confidence: intel.ConfidenceHigh,
		State:      intel.StateValid,
		Native:     1,
		Flags:      intel.FlagProxy,
		ASN:        13335,
		ASOrg:      "Cloudflare",
		Country:    "JP",
		City:       "Tokyo",
		ValidUntil: now.Add(time.Hour).UnixNano(),
		ComputedAt: now.Add(-time.Minute).UnixNano(),
	})
	svc.Snapshot().SetCheck(assessedHash.Hex(), "chatgpt", intel.OutcomeLite{
		Outcome:    intel.OutcomeAvailable,
		ValidUntil: now.Add(time.Hour).UnixNano(),
		ObservedAt: now.UnixNano(),
	})
	if err := svc.Store().UpsertNodeEgress(context.Background(), store.NodeEgress{
		NodeHash: assessedHash.Hex(),
		IPv4:     "203.0.113.10",
		IPv6:     "2001:db8::1",
		Colo:     "NRT",
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	items := nodeItems(t, decodeJSONMap(t, rec))

	assessed, ok := items[assessedHash.Hex()]
	if !ok {
		t.Fatalf("assessed node missing: %v", items)
	}
	view := intelObject(t, assessed)
	if view["state"] != "valid" || view["purity_score"] != float64(91) || view["purity_band"] != "clean" {
		t.Errorf("assessment = %v", view)
	}
	if view["confidence"] != "high" || view["verdict"] != "caution" || view["ip_type"] != "datacenter" {
		t.Errorf("assessment names = %v", view)
	}
	if view["native"] != true {
		t.Errorf("native = %v, want true", view["native"])
	}
	if view["asn"] != float64(13335) || view["as_org"] != "Cloudflare" || view["country"] != "JP" || view["city"] != "Tokyo" {
		t.Errorf("asn facts = %v", view)
	}
	flags, _ := view["flags"].([]any)
	if len(flags) != 1 || flags[0] != "proxy" {
		t.Errorf("flags = %v, want [proxy]", view["flags"])
	}
	checks, _ := view["checks"].(map[string]any)
	if checks["chatgpt"] != "available" {
		t.Errorf("checks = %v", view["checks"])
	}
	if view["assessed_at"] == "" {
		t.Errorf("assessed_at = %v, want an RFC3339 timestamp", view["assessed_at"])
	}
	if view["egress_ipv4"] != "203.0.113.10" || view["egress_ipv6"] != "2001:db8::1" || view["colo"] != "NRT" {
		t.Errorf("node_egress facts = %v", view)
	}

	unassessed, ok := items[unassessedHash.Hex()]
	if !ok {
		t.Fatalf("unassessed node missing: %v", items)
	}
	empty := intelObject(t, unassessed)
	if empty["state"] != "unassessed" {
		t.Errorf("state = %v, want unassessed", empty["state"])
	}
	if empty["purity_score"] != nil {
		t.Errorf("purity_score = %v, want null", empty["purity_score"])
	}
	if empty["native"] != nil {
		t.Errorf("native = %v, want null", empty["native"])
	}
	if empty["purity_band"] != "unknown" || empty["confidence"] != "none" || empty["verdict"] != "pending" {
		t.Errorf("unassessed defaults = %v", empty)
	}
	if empty["assessed_at"] != "" {
		t.Errorf("assessed_at = %v, want empty", empty["assessed_at"])
	}
	if flags, _ := empty["flags"].([]any); len(flags) != 0 {
		t.Errorf("flags = %v, want []", empty["flags"])
	}
	if checks, _ := empty["checks"].(map[string]any); len(checks) != 0 {
		t.Errorf("checks = %v, want {}", empty["checks"])
	}
}

func TestHandleGetNode_IncludesIntelField(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelForTest(t, cp)

	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "sub-a", "https://example.com/a", true, false)
	cp.SubMgr.Register(sub)

	const raw = `{"type":"ss","server":"1.1.1.1","port":443}`
	hash := node.HashFromRawOptions([]byte(raw))
	addNodeForNodeListTest(t, cp, sub, raw, "203.0.113.10")

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+hash.Hex(), nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	view := intelObject(t, decodeJSONMap(t, rec))
	if view["state"] != "unassessed" {
		t.Errorf("state = %v, want unassessed", view["state"])
	}
}

func TestHandleListNodes_IntelFilterQueryParameters(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)

	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "sub-a", "https://example.com/a", true, false)
	cp.SubMgr.Register(sub)

	const cleanRaw = `{"type":"ss","server":"1.1.1.1","port":443}`
	const poorRaw = `{"type":"ss","server":"2.2.2.2","port":443}`
	cleanHash := node.HashFromRawOptions([]byte(cleanRaw))
	poorHash := node.HashFromRawOptions([]byte(poorRaw))
	addNodeForNodeListTest(t, cp, sub, cleanRaw, "203.0.113.10")
	addNodeForNodeListTest(t, cp, sub, poorRaw, "203.0.113.11")

	now := time.Now().UTC()
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score: 95, Band: intel.BandExcellent, Verdict: intel.VerdictFavorable, IPType: intel.IPTypeDatacenter,
		Confidence: intel.ConfidenceHigh, State: intel.StateValid, Native: 1, ASN: 13335, Country: "JP",
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.Add(-time.Minute).UnixNano(),
	})
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.11"), intel.AssessmentLite{
		Score: 40, Band: intel.BandPoor, Verdict: intel.VerdictHighRisk, IPType: intel.IPTypeResidential,
		Confidence: intel.ConfidenceLow, State: intel.StateValid, Native: 0, ASN: 64500, Country: "US",
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.Add(-2 * time.Hour).UnixNano(),
	})
	svc.Snapshot().SetCheck(cleanHash.Hex(), "chatgpt", intel.OutcomeLite{
		Outcome: intel.OutcomeAvailable, ValidUntil: now.Add(time.Hour).UnixNano(), ObservedAt: now.UnixNano(),
	})

	for _, tt := range []struct {
		query string
		want  string
	}{
		{"purity_min=90", cleanHash.Hex()},
		{"purity_max=50", poorHash.Hex()},
		{"verdict=high_risk", poorHash.Hex()},
		{"verdict=high_risk,caution", poorHash.Hex()},
		{"confidence_min=high", cleanHash.Hex()},
		{"native=true", cleanHash.Hex()},
		{"native=false", poorHash.Hex()},
		{"asn=13335", cleanHash.Hex()},
		{"country=jp", cleanHash.Hex()},
		{"check=chatgpt:available", cleanHash.Hex()},
		{"ip_type=residential", poorHash.Hex()},
		{"purity_band=excellent", cleanHash.Hex()},
	} {
		t.Run(tt.query, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?"+tt.query, nil, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			items := nodeItems(t, decodeJSONMap(t, rec))
			if len(items) != 1 {
				t.Fatalf("%s matched %d nodes, want 1", tt.query, len(items))
			}
			if _, ok := items[tt.want]; !ok {
				t.Fatalf("%s matched %v, want %s", tt.query, items, tt.want)
			}
		})
	}
}

func TestHandleListNodes_RejectsInvalidIntelFilters(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelForTest(t, cp)

	for _, query := range []string{
		"purity_min=101",
		"purity_min=abc",
		"purity_max=-1",
		"verdict=unknown_verdict",
		"confidence_min=none",
		"native=maybe",
		"asn=0",
		"country=japan",
		"check=chatgpt",
		"check=chatgpt:nonsense",
		strings.Repeat("check=x:available&", MaxNodeCheckFilters+1) + "check=x:available",
	} {
		t.Run(query, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?"+query, nil, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
			}
			assertErrorCode(t, rec, "INVALID_ARGUMENT")
		})
	}
}

func TestHandleListNodes_SortsByIntelKeys(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)

	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "sub-a", "https://example.com/a", true, false)
	cp.SubMgr.Register(sub)

	const cleanRaw = `{"type":"ss","server":"1.1.1.1","port":443}`
	const poorRaw = `{"type":"ss","server":"2.2.2.2","port":443}`
	const absentRaw = `{"type":"ss","server":"3.3.3.3","port":443}`
	cleanHash := node.HashFromRawOptions([]byte(cleanRaw))
	poorHash := node.HashFromRawOptions([]byte(poorRaw))
	absentHash := node.HashFromRawOptions([]byte(absentRaw))
	addNodeForNodeListTest(t, cp, sub, cleanRaw, "203.0.113.10")
	addNodeForNodeListTest(t, cp, sub, poorRaw, "203.0.113.11")
	addNodeForNodeListTest(t, cp, sub, absentRaw, "203.0.113.12")

	now := time.Now().UTC()
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score: 95, Band: intel.BandExcellent, Verdict: intel.VerdictFavorable, IPType: intel.IPTypeDatacenter,
		Confidence: intel.ConfidenceHigh, State: intel.StateValid, Native: 1,
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.Add(-2 * time.Hour).UnixNano(),
	})
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.11"), intel.AssessmentLite{
		Score: 40, Band: intel.BandPoor, Verdict: intel.VerdictHighRisk, IPType: intel.IPTypeResidential,
		Confidence: intel.ConfidenceLow, State: intel.StateValid, Native: 0,
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.Add(-time.Minute).UnixNano(),
	})

	// The unassessed node has no score and no assessment timestamp, so it is
	// always last; the two assessed nodes swap with the sort order.
	for _, tt := range []struct {
		query string
		want  []string
	}{
		{"sort_by=purity_score&sort_order=desc", []string{cleanHash.Hex(), poorHash.Hex(), absentHash.Hex()}},
		{"sort_by=purity_score&sort_order=asc", []string{poorHash.Hex(), cleanHash.Hex(), absentHash.Hex()}},
		{"sort_by=assessed_at&sort_order=desc", []string{poorHash.Hex(), cleanHash.Hex(), absentHash.Hex()}},
		{"sort_by=assessed_at&sort_order=asc", []string{cleanHash.Hex(), poorHash.Hex(), absentHash.Hex()}},
	} {
		t.Run(tt.query, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?"+tt.query, nil, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			items, ok := decodeJSONMap(t, rec)["items"].([]any)
			if !ok {
				t.Fatalf("items missing: %s", rec.Body.String())
			}
			if len(items) != len(tt.want) {
				t.Fatalf("items = %d, want %d", len(items), len(tt.want))
			}
			for i, entry := range items {
				hash, _ := entry.(map[string]any)["node_hash"].(string)
				if hash != tt.want[i] {
					t.Fatalf("%s: order = %v, want %v", tt.query, items, tt.want)
				}
			}
		})
	}
}

func TestHandlePreviewFilter_ReportsExcludedBy(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)

	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "sub-a", "https://example.com/a", true, false)
	cp.SubMgr.Register(sub)

	const cleanRaw = `{"type":"ss","server":"1.1.1.1","port":443}`
	const poorRaw = `{"type":"ss","server":"2.2.2.2","port":443}`
	addNodeForNodeListTest(t, cp, sub, cleanRaw, "203.0.113.10")
	addNodeForNodeListTest(t, cp, sub, poorRaw, "203.0.113.11")

	now := time.Now().UTC()
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.10"), intel.AssessmentLite{
		Score: 95, Band: intel.BandExcellent, Verdict: intel.VerdictFavorable, IPType: intel.IPTypeDatacenter,
		Confidence: intel.ConfidenceHigh, State: intel.StateValid, Native: 1,
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.UnixNano(),
	})
	svc.Snapshot().SetAssessment(netip.MustParseAddr("203.0.113.11"), intel.AssessmentLite{
		Score: 40, Band: intel.BandPoor, Verdict: intel.VerdictHighRisk, IPType: intel.IPTypeResidential,
		Confidence: intel.ConfidenceLow, State: intel.StateValid, Native: 0,
		ValidUntil: now.Add(time.Hour).UnixNano(), ComputedAt: now.UnixNano(),
	})

	minPurity := 80
	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms/preview-filter", map[string]any{
		"platform_spec":  map[string]any{"regex_filters": []string{".*"}, "region_filters": []string{}},
		"quality_policy": map[string]any{"min_purity": minPurity, "exclude_high_risk": false},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	excludedBy, ok := body["excluded_by"].(map[string]any)
	if !ok {
		t.Fatalf("excluded_by = %v (%T), want an object", body["excluded_by"], body["excluded_by"])
	}
	if excludedBy["QUALITY_MIN_PURITY"] != float64(1) {
		t.Errorf("excluded_by = %v, want one QUALITY_MIN_PURITY", excludedBy)
	}
	if body["total"] != float64(1) {
		t.Errorf("total = %v, want 1", body["total"])
	}
	items := nodeItems(t, body)
	if len(items) != 1 {
		t.Errorf("items = %v, want the clean node only", items)
	}
	// The pagination envelope is unchanged.
	if _, ok := body["limit"]; !ok {
		t.Errorf("limit missing from the page envelope: %v", body)
	}
	if _, ok := body["offset"]; !ok {
		t.Errorf("offset missing from the page envelope: %v", body)
	}
	if _, ok := body["nodes"]; ok {
		t.Errorf("legacy nodes field must not be returned: %v", body)
	}
}

func TestHandleUpdateExportProfile_StoresIntelFilters(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelForTest(t, cp)

	create := doJSONRequest(t, srv, http.MethodPost, "/api/v1/export-profiles", map[string]any{
		"name":   "intel filter",
		"format": "json",
		"filter": map[string]any{
			"purity_min":     80,
			"purity_max":     100,
			"verdict":        "favorable,caution",
			"confidence_min": "medium",
			"native":         true,
			"asn":            13335,
			"country":        "jp",
			"checks":         []string{"chatgpt:available"},
		},
	}, true)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", create.Code, create.Body.String())
	}
	filter, ok := decodeJSONMap(t, create)["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter = %v, want an object", decodeJSONMap(t, create)["filter"])
	}
	if filter["purity_min"] != float64(80) || filter["native"] != true || filter["country"] != "jp" {
		t.Errorf("stored filter = %v", filter)
	}
	if checks, _ := filter["checks"].([]any); len(checks) != 1 || checks[0] != "chatgpt:available" {
		t.Errorf("stored checks = %v", filter["checks"])
	}

	invalid := doJSONRequest(t, srv, http.MethodPost, "/api/v1/export-profiles", map[string]any{
		"name":   "invalid filter",
		"format": "json",
		"filter": map[string]any{"purity_min": 101},
	}, true)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter status = %d, want 400, body=%s", invalid.Code, invalid.Body.String())
	}
	assertErrorCode(t, invalid, "INVALID_ARGUMENT")
}
