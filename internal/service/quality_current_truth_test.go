package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/quality"
)

func TestQualityStateExpiryAgreesAcrossSurfaces(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"valid", "pending", "unsupported"} {
		for _, kind := range []string{"residential", "conflicting"} {
			for _, until := range []int64{0, now.Add(-time.Second).UnixNano(), now.Add(time.Hour).UnixNano()} {
				row := store.Assessment{State: state, IPType: kind, ValidUntilNs: until}
				got := qualityStateFromRows(nil, &row, now)
				want := qualityStateFromLite(intel.AssessmentLiteFromRow(row), now)
				if got != want {
					t.Errorf("%s/%s/until=%d: DB=%s projection=%s", state, kind, until, got, want)
				}
			}
		}
	}
}

func TestNetworkConflictResolutionReachesEveryProjection(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip := entry.GetEgressIP()
	risk := 30
	evidence := []quality.Evidence{
		{IP: ip.String(), Provider: "proxycheck_node", RiskScore: &risk, IPType: "residential", ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)},
		{IP: ip.String(), Provider: "ip_api", IPType: "datacenter", ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)},
	}
	for _, want := range []string{"conflicting", "residential"} {
		if want == "residential" {
			evidence[1].IPType = want
			evidence[1].ObservedAt = f.now.Add(time.Second)
		}
		a := assess.Assess(ip, evidence, f.now.Add(time.Minute), nil)
		row := intel.AssessmentRow(a, f.now)
		if err := f.svc.Store().UpsertAssessment(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		f.svc.Snapshot().SetAssessmentFromRow(row)
		view := f.cp.nodeIntel(entry, f.now)
		q, err := f.cp.QualityIP(context.Background(), ip.String())
		if err != nil {
			t.Fatal(err)
		}
		if view.IPType != want || q.Assessment.NetworkType != want || f.cp.NodeQualityForEgress(ip).Assessment.NetworkType != want {
			t.Fatal("network classification diverged")
		}
		fact := view.Facts["network_type"]
		if want == "conflicting" && (fact.Cause != "PROVIDER_TYPE_DISAGREEMENT" || fact.Action == "") {
			t.Fatal("conflict lacks cause/action")
		}
		if want == "residential" && (fact.State != "known" || fact.Cause != "") {
			t.Fatal("resolved conflict retained its cause")
		}
	}
}

func TestUnknownFactsKeepExactFailureCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		view  NodeIntel
		cause string
	}{
		{"trace failure", NodeIntel{MeasurementState: "measured_failed", MeasurementError: "TRACE_TIMEOUT"}, "MEASUREMENT_FAILED"},
		{"all sources failed", NodeIntel{EgressIPv4: "203.0.113.10", State: "unsupported", Reasons: []string{"IPPURE_PROVIDER_UNAVAILABLE"}}, "IPPURE_PROVIDER_UNAVAILABLE"},
		{"no scoring sources", NodeIntel{EgressIPv4: "203.0.113.10", State: "unsupported", Reasons: []string{"IPPURE_NOT_RUN"}}, "IPPURE_NOT_RUN"},
		{"no valid evidence", NodeIntel{EgressIPv4: "203.0.113.10", State: "pending", Reasons: []string{"IPPURE_NO_FRAUD_SCORE"}}, "IPPURE_NO_FRAUD_SCORE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.view.explainFacts()
			f := tc.view.Facts["purity"]
			if f.Cause != tc.cause || f.Action == "" {
				t.Fatalf("cause=%q action=%q, want %s", f.Cause, f.Action, tc.cause)
			}
			if tc.name == "trace failure" {
				for _, key := range []string{"egress_ipv4", "egress_ipv6", "colo"} {
					if tc.view.Facts[key].Cause != "TRACE_TIMEOUT" {
						t.Fatalf("%s lost trace failure", key)
					}
				}
			}
		})
	}
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	nodes := []NodeSummary{{NodeHash: f.clean.Hex(), EgressIP: entry.GetEgressIP().String(), Intel: f.cp.nodeIntel(entry, f.now)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.cp.FillNodeQualityEvidence(ctx, nodes)
	if fact := nodes[0].Intel.Facts["proxy"]; fact.Cause != "EVIDENCE_READ_FAILED" || fact.Action == "" {
		t.Fatalf("storage failure disguised as missing provider field: %+v", fact)
	}
	f.cp.FillNodeIntelEgress(ctx, nodes)
	if fact := nodes[0].Intel.Facts["proxy"]; fact.Cause != "EVIDENCE_READ_FAILED" || fact.Action == "" {
		t.Fatal("lost evidence read failure")
	}
	n := NodeIntel{EgressIPv4: "203.0.113.10", State: "valid", Facts: map[string]quality.Fact{
		"network_type": {Sources: []string{"ippure:unobserved:EGRESS_MISMATCH", "proxycheck_node:unobserved:PROVIDER_RESPONSE"}},
	}}
	n.explainFacts()
	fact := n.Facts["network_type"]
	if fact.Cause != "NO_CURRENT_FIELD_EVIDENCE" || fact.Action == "" || len(fact.Sources) != 2 {
		t.Fatal("provider failures collapsed into unreported field")
	}
	n.measurementReadFailed = true
	n.explainFacts()
	if n.Facts["purity"].Cause != "MEASUREMENT_STATE_READ_FAILED" {
		t.Fatal("lost measurement-state read failure")
	}
}

func TestSignalDisplayUsesTheSameCurrentVendorEvidenceAsAssessment(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip := entry.GetEgressIP()
	for _, tc := range []struct {
		provider string
		value    bool
		at       time.Time
	}{
		{"proxycheck", true, f.now.Add(-time.Minute)},
		{"proxycheck_node", false, f.now},
		{"ipapi_is", true, f.now.Add(time.Hour)},
	} {
		e := quality.Evidence{IP: ip.String(), Provider: tc.provider, ObservedAt: tc.at, ValidUntil: f.now.Add(2 * time.Hour), Signals: quality.Signals{Proxy: &tc.value}}
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		row := store.Evidence{IP: ip.String(), Provider: tc.provider, Profile: "fixture", Status: store.StatusOk, ObservedAtNs: tc.at.UnixNano(), ValidUntilNs: e.ValidUntil.UnixNano(), NormalizedJSON: string(body)}
		if err := f.svc.Store().UpsertEvidence(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	nodes := []NodeSummary{{NodeHash: f.clean.Hex(), EgressIP: ip.String(), Intel: f.cp.nodeIntel(entry, f.now)}}
	f.cp.FillNodeQualityEvidence(context.Background(), nodes)
	if v := nodes[0].Intel.Signals.Proxy; v == nil || *v {
		t.Fatal("old vendor alias or future observation overrode the current correction")
	}
	for _, observed := range []int64{0, f.now.Add(time.Hour).UnixNano()} {
		row := store.Evidence{IP: ip.String(), Provider: "torproject", Status: store.StatusOk, ObservedAtNs: observed, ValidUntilNs: f.now.Add(2 * time.Hour).UnixNano(), NormalizedJSON: `{"tor_roles":["relay"]}`}
		if qualityStateFromEvidence(row, f.now) == "valid" || len(qualityTorRoles([]store.Evidence{row}, f.now)) != 0 {
			t.Fatal("untimed/future evidence displayed as current")
		}
		if qualityEvidenceIssue(row, f.now) != "EVIDENCE_TIME_INVALID" {
			t.Fatal("invalid observation lost its exact cause")
		}
	}
}
