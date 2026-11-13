package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"prism/internal/export"
	"prism/internal/intel"
	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/quality"
	"prism/internal/service"
	"prism/internal/subscription"
)

func TestPurityAgreesAcrossHTTPDetailListRangeSortAndExport(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)
	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "fixture", "", true, false)
	cp.SubMgr.Register(sub)
	now := time.Now().UTC()
	var target node.Hash
	for _, tc := range []struct {
		raw, ip             string
		riskA, riskB, score int
	}{
		{`{"type":"direct","fixture":"single-source"}`, "203.0.113.10", 80, 10, 90},
		{`{"type":"direct","fixture":"comparison"}`, "203.0.113.11", 30, 30, 70},
	} {
		addNodeForNodeListTest(t, cp, sub, tc.raw, tc.ip)
		if tc.score == 90 {
			target = node.HashFromRawOptions([]byte(tc.raw))
		}
		ip := netip.MustParseAddr(tc.ip)
		ev := []quality.Evidence{
			{IP: tc.ip, Provider: "proxycheck", RiskScore: &tc.riskA, ObservedAt: now, ValidUntil: now.Add(time.Hour)},
			{IP: tc.ip, Provider: "ippure", FraudScore: &tc.riskB, RiskScore: &tc.riskB, ObservedAt: now, ValidUntil: now.Add(time.Hour)},
		}
		a := assess.Assess(ip, ev, now, nil)
		if a.PurityScore == nil || *a.PurityScore != tc.score {
			t.Fatal("fixture formula differs")
		}
		row := intel.AssessmentRow(a, now)
		if err := svc.Store().UpsertAssessment(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		svc.Snapshot().SetAssessmentFromRow(row)
		for _, e := range ev {
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Store().UpsertEvidence(context.Background(), store.Evidence{IP: tc.ip, Provider: e.Provider, Profile: "fixture", Status: store.StatusOk, ObservedAtNs: now.UnixNano(), ValidUntilNs: now.Add(time.Hour).UnixNano(), NormalizedJSON: string(b)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(item map[string]any) {
		t.Helper()
		view := intelObject(t, item)
		if view["purity_score"] != float64(90) || view["purity_band"] != "clean" {
			t.Fatal("node surface differs from single-source purity")
		}
		q := item["quality"].(map[string]any)["assessment"].(map[string]any)
		if q["purity_score"] != view["purity_score"] || q["purity_band"] != view["purity_band"] {
			t.Fatal("quality and intel disagree for the same node")
		}
	}
	for _, query := range []string{"", "?purity_min=90&purity_max=90", "?sort_by=purity_score&sort_order=asc", "?sort_by=purity_score&sort_order=desc"} {
		rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes"+query, nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("list status %d", rec.Code)
		}
		body := decodeJSONMap(t, rec)
		items := nodeItems(t, body)
		check(items[target.Hex()])
		if query == "?purity_min=90&purity_max=90" && len(items) != 1 {
			t.Fatal("range filter differs")
		}
		ordered := body["items"].([]any)
		if query == "?sort_by=purity_score&sort_order=asc" && ordered[1].(map[string]any)["node_hash"] != target.Hex() {
			t.Fatal("ascending sort used another score")
		}
		if query == "?sort_by=purity_score&sort_order=desc" && ordered[0].(map[string]any)["node_hash"] != target.Hex() {
			t.Fatal("descending sort used another score")
		}
	}
	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+target.Hex(), nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status %d", rec.Code)
	}
	check(decodeJSONMap(t, rec))
	q, err := cp.QualityIP(context.Background(), "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if *q.Assessment.PurityScore != 90 || q.Assessment.PurityBand != "clean" {
		t.Fatal("quality IP differs")
	}
	var summary service.NodeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	item, ok := cp.ExportItem(summary)
	if !ok {
		t.Fatal("export omitted measured node")
	}
	body, _, _, err := export.Export([]export.Item{item}, export.FormatJSON, export.Options{IncludeIntel: true})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Items) != 1 || output.Items[0]["purity_score"] != "90" || output.Items[0]["purity_band"] != "clean" {
		if len(output.Items) == 0 {
			t.Fatal("export omitted fixture")
		}
		t.Fatalf("export purity=%v band=%v", output.Items[0]["purity_score"], output.Items[0]["purity_band"])
	}
}
