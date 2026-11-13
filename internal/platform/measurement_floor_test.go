package platform

import (
	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestMeasurementFloorCannotBeWaivedByEmptyPolicy(t *testing.T) {
	entry := admitEntry(t)
	for _, policy := range []model.QualityPolicy{{}, {UnknownAction: "allow"}} {
		if ok, _ := AdmitQuality(policy, entry, newFakeSnapshot(), admitNow); ok {
			t.Fatal("never-measured node sold")
		}
		for _, state := range []uint8{intel.StatePending, intel.StateUnsupported} {
			a := lite()
			a.State = state
			snap := newFakeSnapshot().set("1.2.3.4", a)
			if ok, _ := AdmitQuality(policy, entry, snap, admitNow); ok {
				t.Fatal("invalid measurement sold")
			}
		}
		for _, mode := range []string{"nil", "expired"} {
			a := lite()
			a.State = intel.StateValid
			if mode == "nil" {
				a.Score = -1
			} else {
				a.ValidUntil = admitNow.Add(-time.Second).UnixNano()
			}
			if ok, _ := AdmitQuality(policy, entry, newFakeSnapshot().set("1.2.3.4", a), admitNow); ok {
				t.Fatal("missing/stale score sold")
			}
		}
	}
	for _, score := range []int8{0, 12, 100} {
		a := lite()
		a.State = intel.StateValid
		a.Score = score
		a.Verdict = intel.VerdictHighRisk
		a.Flags = intel.FlagTor
		snap := newFakeSnapshot().set("1.2.3.4", a)
		if ok, reason := AdmitQuality(model.QualityPolicy{}, entry, snap, admitNow); !ok {
			t.Fatal("empty policy changed impure classification", score, reason)
		}
	}
}

func TestEmptyPolicyDoesNotRejectMeasuredNetworkConflict(t *testing.T) {
	score := 0
	summary := quality.Summary{State: "conflicting", Assessment: &quality.Assessment{State: "valid", PurityScore: &score, NetworkType: "conflicting", Verdict: "high_risk"}}
	if !EvaluateQuality(model.QualityPolicy{}, summary, admitNow, admitNow) {
		t.Fatal("Default added a network-conflict restriction to a valid measured score")
	}
	summary.State = "stale"
	if EvaluateQuality(model.QualityPolicy{}, summary, admitNow, admitNow) {
		t.Fatal("stale conflict accepted")
	}
}

func TestEmptyPlatformPolicyCannotSellNeverMeasuredNode(t *testing.T) {
	entry := makeFullyRoutableEntry(makeHash(`{"type":"shadowsocks","fixture":"floor"}`), "source")
	plat := NewPlatform("floor", "floor", nil, nil)
	snap := newFakeSnapshot()
	plat.SetQualitySnapshot(snap)
	rebuild := func() {
		plat.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) { fn(entry.Hash, entry) }, alwaysLookup, nil, nil)
	}
	rebuild()
	if plat.View().Size() != 0 {
		t.Fatal("policy {} sold a never-measured node")
	}
	measured := projectionAt(time.Now(), 0)
	measured.State = intel.StateValid
	measured.Verdict = intel.VerdictHighRisk
	snap.assessments[entry.GetEgressIP()] = measured
	rebuild()
	if plat.View().Size() != 1 {
		t.Fatal("measurement floor secretly became a low-purity ban")
	}
}
