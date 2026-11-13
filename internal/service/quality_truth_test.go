package service

import (
	"context"
	"encoding/json"
	"prism/internal/config"
	"prism/internal/export"
	"prism/internal/intel"
	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/platform"
	"prism/internal/quality"
	"sync/atomic"
	"testing"
	"time"
)

func TestPurityOneScoreAndHotBandAcrossConsumers(t *testing.T) {
	f := buildIntelNodeFixture(t)
	ctx := context.Background()
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip := entry.GetEgressIP()
	riskA, riskB := 80, 10
	input := []quality.Evidence{
		{IP: ip.String(), Provider: "proxycheck", RiskScore: &riskA, ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)},
		{IP: ip.String(), Provider: "ippure", FraudScore: &riskB, RiskScore: &riskB, ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)},
	}
	for _, ev := range input {
		body, _ := json.Marshal(ev)
		if err := f.svc.Store().UpsertEvidence(ctx, store.Evidence{IP: ev.IP, Provider: ev.Provider, Status: store.StatusOk, ObservedAtNs: ev.ObservedAt.UnixNano(), ValidUntilNs: ev.ValidUntil.UnixNano(), NormalizedJSON: string(body)}); err != nil {
			t.Fatal(err)
		}
	}
	result := assess.Assess(ip, input, f.now, nil)
	if result.PurityScore == nil || *result.PurityScore != 90 {
		t.Fatalf("IPPure fraudScore 10 must produce 90: %+v", result)
	}
	row := intel.AssessmentRow(result, f.now)
	if err := f.svc.Store().UpsertAssessment(ctx, row); err != nil {
		t.Fatal(err)
	}
	f.svc.Snapshot().SetAssessmentFromRow(row)
	f.cp.RuntimeCfg = &atomic.Pointer[config.RuntimeConfig]{}
	cfg := config.NewDefaultRuntimeConfig()
	f.cp.RuntimeCfg.Store(cfg)
	f.svc.Snapshot().Bands = f.cp.qualityBands
	for _, wantBand := range []string{"clean", "excellent"} {
		if wantBand == "excellent" {
			cfg2 := *cfg
			cfg2.PurityBands = config.PurityBands{Excellent: 60, Clean: 50, Fair: 40, Mixed: 30}
			f.cp.RuntimeCfg.Store(&cfg2)
		}
		view := f.cp.nodeIntel(entry, f.now)
		if view.PurityScore == nil || *view.PurityScore != 90 || view.PurityBand != wantBand {
			t.Fatal("node", view.PurityScore, view.PurityBand)
		}
		q := f.cp.NodeQualityForEgress(ip)
		if *q.Assessment.PurityScore != 90 || q.Assessment.PurityBand != wantBand {
			t.Fatal("quality projection disagrees")
		}
		qdb, err := f.cp.QualityIP(ctx, ip.String())
		if err != nil {
			t.Fatal(err)
		}
		if *qdb.Assessment.PurityScore != 90 || qdb.Assessment.PurityBand != wantBand {
			t.Fatal("DB quality disagrees")
		}
		report, err := f.cp.IntelIPReport(ctx, ip.String())
		if err != nil {
			t.Fatal(err)
		}
		if report.Assessment.PurityBand != wantBand || intel.BandName(report.Projection.Band) != wantBand {
			t.Fatal("intel report disagrees")
		}
		min, max := 90, 90
		if !f.cp.nodeIntelMatchesFilters(view, NodeFilters{PurityMin: &min, PurityMax: &max, PurityBand: &wantBand}) {
			t.Fatal("filters disagree")
		}
		ok, reason := platform.MatchNodeCriteriaForPreview(platform.NodeCriteria{PurityBands: []string{wantBand}, QualityPolicy: model.QualityPolicy{MinPurity: &min}}, entry, nil, nil, nil, f.svc.Snapshot(), f.now)
		if !ok {
			t.Fatal("platform admission disagrees", reason)
		}
		min = 91
		if ok, _ := platform.AdmitQuality(model.QualityPolicy{MinPurity: &min}, entry, f.svc.Snapshot(), f.now); ok {
			t.Fatal("platform ignored the explicit threshold")
		}
		item, ok := f.cp.ExportItem(f.cp.nodeEntryToSummary(f.clean, entry))
		if !ok {
			t.Fatal("missing export")
		}
		body, _, _, err := export.Export([]export.Item{item}, export.FormatJSON, export.Options{IncludeIntel: true})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if err = json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Items) != 1 || out.Items[0]["purity_score"] != "90" || out.Items[0]["purity_band"] != wantBand {
			t.Fatal("serialized export disagrees")
		}
	}
}

func TestEveryUnavailableQualityFactHasCauseAndClearingAction(t *testing.T) {
	for _, state := range []string{"unassessed", "pending", "unsupported", "stale", "valid"} {
		for _, measurement := range []string{"never_scheduled", "queued", "running", "measured_failed", "complete"} {
			for _, kind := range []string{"unknown", "conflicting", "residential"} {
				n := NodeIntel{State: state, MeasurementState: measurement, IPType: kind, Checks: map[string]string{"test": "error"}}
				n.explainFacts()
				for _, key := range []string{"purity", "network_type", "egress_ipv4", "egress_ipv6", "colo", "asn", "as_org", "country", "city", "native", "proxy", "vpn", "tor", "hosting", "compromised", "scraper", "anonymous", "confidence", "assessed_at", "verdict", "checks"} {
					if _, ok := n.Facts[key]; !ok {
						t.Fatalf("coverage contract omitted %s", key)
					}
				}
				for key, f := range n.Facts {
					if f.State != "known" && (f.Cause == "" || f.Action == "" || f.SourceClass == "") {
						t.Fatalf("%s/%s/%s/%s lacks explanation: %+v", state, measurement, kind, key, f)
					}
				}
				n.EgressIPv4 = "203.0.113.10"
				n.explainFacts()
				for key, f := range n.Facts {
					if f.State != "known" && (f.Cause == "" || f.Action == "") {
						t.Fatalf("%s: %+v", key, f)
					}
				}
			}
		}
	}
	n := NodeIntel{EgressIPv4: "203.0.113.10", IPType: "conflicting"}
	n.explainFacts()
	if n.Facts["network_type"].Cause != "PROVIDER_TYPE_DISAGREEMENT" {
		t.Fatal("lost conflict cause")
	}
	n.IPType = "residential"
	n.explainFacts()
	if n.Facts["network_type"].State != "known" {
		t.Fatal("resolved conflict retained")
	}
	score := 90
	n.State = IntelStateStale
	n.PurityScore = &score
	n.explainFacts()
	for _, key := range []string{"purity", "network_type"} {
		f := n.Facts[key]
		if f.State != "stale" || f.Cause != "EVIDENCE_EXPIRED" || f.Action == "" {
			t.Fatalf("historical %s falsely current: %+v", key, f)
		}
	}
	rows := []NodeSummary{{}}
	(&ControlPlaneService{}).FillNodeQualityEvidence(context.Background(), rows)
	if f := rows[0].Intel.Facts["latency"]; f.Cause == "" || f.Action == "" {
		t.Fatal("unwired enrichment hid latency gap")
	}
}
