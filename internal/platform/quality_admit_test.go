package platform

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
)

// fakeSnapshot is the read-only intel projection used by the admission tests.
type fakeSnapshot struct {
	assessments map[netip.Addr]intel.AssessmentLite
	checks      map[string]map[string]intel.OutcomeLite
}

func newFakeSnapshot() *fakeSnapshot {
	return &fakeSnapshot{
		assessments: map[netip.Addr]intel.AssessmentLite{},
		checks:      map[string]map[string]intel.OutcomeLite{},
	}
}

func (f *fakeSnapshot) set(ip string, lite intel.AssessmentLite) *fakeSnapshot {
	f.assessments[netip.MustParseAddr(ip)] = lite
	return f
}

func (f *fakeSnapshot) setCheck(nodeHash, checkID string, outcome intel.OutcomeLite) *fakeSnapshot {
	entry, ok := f.checks[nodeHash]
	if !ok {
		entry = map[string]intel.OutcomeLite{}
		f.checks[nodeHash] = entry
	}
	entry[checkID] = outcome
	return f
}

func (f *fakeSnapshot) Assessment(ip netip.Addr) (intel.AssessmentLite, bool) {
	lite, ok := f.assessments[ip.Unmap()]
	return lite, ok
}

func (f *fakeSnapshot) CheckOutcome(nodeHash, checkID string) (intel.OutcomeLite, bool) {
	entry, ok := f.checks[nodeHash]
	if !ok {
		return intel.OutcomeLite{}, false
	}
	lite, ok := entry[checkID]
	return lite, ok
}

var admitNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// lite builds a projected assessment that passes every rule by default.
func lite() intel.AssessmentLite {
	return intel.AssessmentLite{
		Score:      90,
		Band:       intel.BandCode("clean"),
		Verdict:    intel.VerdictCode("favorable"),
		IPType:     intel.IPTypeCode("residential"),
		Confidence: intel.ConfidenceCode("high"),
		Native:     1,
		Flags:      0,
		ValidUntil: admitNow.Add(24 * time.Hour).UnixNano(),
		ComputedAt: admitNow.Add(-time.Hour).UnixNano(),
	}
}

func admitEntry(t *testing.T) *node.NodeEntry {
	t.Helper()
	entry := node.NewNodeEntry(makeHash(`{"type":"ss"}`), nil, admitNow, 16)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	entry.LastEgressUpdate.Store(admitNow.Add(-time.Minute).UnixNano())
	return entry
}

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }

// TestAdmitQuality_Rules covers every rule of WP10 §2 with a passing and a
// rejecting case.
func TestAdmitQuality_Rules(t *testing.T) {
	cases := []struct {
		name     string
		policy   model.QualityPolicy
		lite     *intel.AssessmentLite
		noEgress bool
		checks   func(*fakeSnapshot, *node.NodeEntry)
		egressAt *time.Time
		wantOK   bool
		want     string
	}{
		{
			name:   "empty_policy_always_passes",
			policy: model.QualityPolicy{},
			lite:   nil,
			wantOK: true,
		},
		{
			name:     "missing_egress_ip_fails_closed",
			policy:   model.QualityPolicy{MinPurity: intPtr(50)},
			lite:     nil,
			noEgress: true,
			wantOK:   false,
			want:     "QUALITY_EGRESS_UNKNOWN",
		},
		{
			name:   "unknown_assessment_rejected_by_default",
			policy: model.QualityPolicy{MinPurity: intPtr(50)},
			lite:   nil,
			wantOK: false,
			want:   "QUALITY_UNKNOWN",
		},
		{
			name:   "unknown_assessment_allowed_by_unknown_action",
			policy: model.QualityPolicy{MinPurity: intPtr(50), UnknownAction: "allow"},
			lite:   nil,
			wantOK: true,
		},
		{
			name:   "stale_assessment_is_unknown",
			policy: model.QualityPolicy{MinPurity: intPtr(50)},
			lite: func() *intel.AssessmentLite {
				stale := lite()
				stale.ValidUntil = admitNow.Add(-time.Minute).UnixNano()
				return &stale
			}(),
			wantOK: false,
			want:   "QUALITY_UNKNOWN",
		},
		{
			name:   "pending_verdict_is_unknown",
			policy: model.QualityPolicy{MinPurity: intPtr(50)},
			lite: func() *intel.AssessmentLite {
				pending := lite()
				pending.Verdict = intel.VerdictCode("pending")
				pending.Score = -1
				return &pending
			}(),
			wantOK: false,
			want:   "QUALITY_UNKNOWN",
		},
		{
			name:   "tor_flag_rejected",
			policy: model.QualityPolicy{MinPurity: intPtr(50)},
			lite: func() *intel.AssessmentLite {
				tor := lite()
				tor.Flags = uint16(intel.FlagTor)
				return &tor
			}(),
			wantOK: false,
			want:   "QUALITY_TOR",
		},
		{
			name:   "tor_flag_allowed_when_exclude_tor_is_false",
			policy: model.QualityPolicy{MinPurity: intPtr(50), ExcludeTor: boolPtr(false)},
			lite: func() *intel.AssessmentLite {
				tor := lite()
				tor.Flags = uint16(intel.FlagTor)
				return &tor
			}(),
			wantOK: true,
		},
		{
			name:   "high_risk_rejected",
			policy: model.QualityPolicy{MinPurity: intPtr(50)},
			lite: func() *intel.AssessmentLite {
				high := lite()
				high.Verdict = intel.VerdictCode("high_risk")
				return &high
			}(),
			wantOK: false,
			want:   "QUALITY_HIGH_RISK",
		},
		{
			name:   "high_risk_allowed_when_exclude_high_risk_is_false",
			policy: model.QualityPolicy{MinPurity: intPtr(50), ExcludeHighRisk: boolPtr(false)},
			lite: func() *intel.AssessmentLite {
				high := lite()
				high.Verdict = intel.VerdictCode("high_risk")
				return &high
			}(),
			wantOK: true,
		},
		{
			name:   "verdict_not_in_allow_list",
			policy: model.QualityPolicy{AllowedVerdicts: []string{"favorable", "caution"}},
			lite: func() *intel.AssessmentLite {
				review := lite()
				review.Verdict = intel.VerdictCode("review")
				return &review
			}(),
			wantOK: false,
			want:   "QUALITY_VERDICT",
		},
		{
			name:   "verdict_in_allow_list",
			policy: model.QualityPolicy{AllowedVerdicts: []string{"favorable"}},
			lite:   litePtr(),
			wantOK: true,
		},
		{
			name:   "min_purity_below_threshold",
			policy: model.QualityPolicy{MinPurity: intPtr(80)},
			lite: func() *intel.AssessmentLite {
				low := lite()
				low.Score = 72
				return &low
			}(),
			wantOK: false,
			want:   "QUALITY_MIN_PURITY",
		},
		{
			name:   "min_purity_above_threshold",
			policy: model.QualityPolicy{MinPurity: intPtr(80)},
			lite: func() *intel.AssessmentLite {
				high := lite()
				high.Score = 85
				return &high
			}(),
			wantOK: true,
		},
		{
			name:   "min_purity_rejects_null_score",
			policy: model.QualityPolicy{MinPurity: intPtr(80)},
			lite: func() *intel.AssessmentLite {
				unknown := lite()
				unknown.Score = -1
				return &unknown
			}(),
			wantOK: false,
			want:   "QUALITY_MIN_PURITY",
		},
		{
			name:   "ip_type_not_allowed",
			policy: model.QualityPolicy{IPTypes: []string{"residential", "mobile"}},
			lite: func() *intel.AssessmentLite {
				dc := lite()
				dc.IPType = intel.IPTypeCode("datacenter")
				return &dc
			}(),
			wantOK: false,
			want:   "QUALITY_IP_TYPE",
		},
		{
			name:   "ip_type_allowed",
			policy: model.QualityPolicy{IPTypes: []string{"residential"}},
			lite:   litePtr(),
			wantOK: true,
		},
		{
			name:   "confidence_below_minimum",
			policy: model.QualityPolicy{MinConfidence: "medium"},
			lite: func() *intel.AssessmentLite {
				low := lite()
				low.Confidence = intel.ConfidenceCode("low")
				return &low
			}(),
			wantOK: false,
			want:   "QUALITY_CONFIDENCE",
		},
		{
			name:   "confidence_without_evidence_is_rejected",
			policy: model.QualityPolicy{MinConfidence: "low"},
			lite: func() *intel.AssessmentLite {
				none := lite()
				none.Confidence = intel.ConfidenceCode("none")
				return &none
			}(),
			wantOK: false,
			want:   "QUALITY_CONFIDENCE",
		},
		{
			name:   "confidence_at_minimum_passes",
			policy: model.QualityPolicy{MinConfidence: "high"},
			lite:   litePtr(),
			wantOK: true,
		},
		{
			name:   "require_native_rejects_non_native",
			policy: model.QualityPolicy{RequireNative: true},
			lite: func() *intel.AssessmentLite {
				non := lite()
				non.Native = 0
				return &non
			}(),
			wantOK: false,
			want:   "QUALITY_NATIVE",
		},
		{
			name:   "require_native_rejects_missing_native",
			policy: model.QualityPolicy{RequireNative: true},
			lite: func() *intel.AssessmentLite {
				missing := lite()
				missing.Native = -1
				return &missing
			}(),
			wantOK: false,
			want:   "QUALITY_NATIVE",
		},
		{
			name:   "require_native_passes_for_native",
			policy: model.QualityPolicy{RequireNative: true},
			lite:   litePtr(),
			wantOK: true,
		},
		{
			name:   "required_check_missing",
			policy: model.QualityPolicy{RequiredChecks: map[string]string{"chatgpt": "available"}},
			lite:   litePtr(),
			wantOK: false,
			want:   "QUALITY_CHECK:chatgpt",
		},
		{
			name:   "required_check_wrong_outcome",
			policy: model.QualityPolicy{RequiredChecks: map[string]string{"chatgpt": "available"}},
			lite:   litePtr(),
			checks: func(snap *fakeSnapshot, entry *node.NodeEntry) {
				snap.setCheck(entry.Hash.Hex(), "chatgpt", intel.OutcomeLite{
					Outcome:    intel.OutcomeCode("blocked"),
					ValidUntil: admitNow.Add(time.Hour).UnixNano(),
				})
			},
			wantOK: false,
			want:   "QUALITY_CHECK:chatgpt",
		},
		{
			name:   "required_check_expired",
			policy: model.QualityPolicy{RequiredChecks: map[string]string{"chatgpt": "available"}},
			lite:   litePtr(),
			checks: func(snap *fakeSnapshot, entry *node.NodeEntry) {
				snap.setCheck(entry.Hash.Hex(), "chatgpt", intel.OutcomeLite{
					Outcome:    intel.OutcomeCode("available"),
					ValidUntil: admitNow.Add(-time.Minute).UnixNano(),
				})
			},
			wantOK: false,
			want:   "QUALITY_CHECK:chatgpt",
		},
		{
			name:   "required_check_satisfied",
			policy: model.QualityPolicy{RequiredChecks: map[string]string{"chatgpt": "available"}},
			lite:   litePtr(),
			checks: func(snap *fakeSnapshot, entry *node.NodeEntry) {
				snap.setCheck(entry.Hash.Hex(), "chatgpt", intel.OutcomeLite{
					Outcome:    intel.OutcomeCode("available"),
					ValidUntil: admitNow.Add(time.Hour).UnixNano(),
				})
			},
			wantOK: true,
		},
		{
			name:   "assessment_too_old",
			policy: model.QualityPolicy{MaxAssessmentAge: "24h"},
			lite: func() *intel.AssessmentLite {
				old := lite()
				old.ComputedAt = admitNow.Add(-48 * time.Hour).UnixNano()
				return &old
			}(),
			wantOK: false,
			want:   "QUALITY_STALE",
		},
		{
			name:   "assessment_within_max_age",
			policy: model.QualityPolicy{MaxAssessmentAge: "24h"},
			lite:   litePtr(),
			wantOK: true,
		},
		{
			name:   "egress_observation_too_old",
			policy: model.QualityPolicy{MaxEgressAge: "10m"},
			lite:   litePtr(),
			egressAt: func() *time.Time {
				t := admitNow.Add(-time.Hour)
				return &t
			}(),
			wantOK: false,
			want:   "QUALITY_EGRESS_STALE",
		},
		{
			name:     "egress_observation_within_max_age",
			policy:   model.QualityPolicy{MaxEgressAge: "10m"},
			lite:     litePtr(),
			egressAt: nil,
			wantOK:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := admitEntry(t)
			if tc.noEgress {
				entry.SetEgressIP(netip.Addr{})
			}
			if tc.egressAt != nil {
				entry.LastEgressUpdate.Store(tc.egressAt.UnixNano())
			}
			snap := newFakeSnapshot()
			if tc.lite != nil {
				snap.set("1.2.3.4", *tc.lite)
			}
			if tc.checks != nil {
				tc.checks(snap, entry)
			}

			ok, reason := AdmitQuality(tc.policy, entry, snap, admitNow)
			if ok != tc.wantOK {
				t.Fatalf("admitted = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if !tc.wantOK && reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
			if tc.wantOK && reason != "" {
				t.Fatalf("reason = %q, want empty", reason)
			}
		})
	}
}

func litePtr() *intel.AssessmentLite {
	v := lite()
	return &v
}

// TestAdmitQuality_EmptyPolicySkipsSnapshot covers deviation X5: without a
// configured policy the admission path must not even look at the snapshot.
func TestAdmitQuality_EmptyPolicySkipsSnapshot(t *testing.T) {
	entry := admitEntry(t)
	entry.SetEgressIP(netip.Addr{})
	if ok, reason := AdmitQuality(model.QualityPolicy{}, entry, nil, admitNow); !ok || reason != "" {
		t.Fatalf("empty policy must always pass, got ok=%v reason=%q", ok, reason)
	}
}

// TestPlatform_FullRebuild_QualityAdmission proves the policy flows through the
// routable view: a node failing one rule disappears from the view. The
// assessment is anchored to the real clock because evaluateNode reads the wall
// clock.
func TestPlatform_FullRebuild_QualityAdmission(t *testing.T) {
	now := time.Now().UTC()
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	entry.LastEgressUpdate.Store(now.Add(-time.Minute).UnixNano())

	minPurity := 80
	policy := model.QualityPolicy{MinPurity: &minPurity}

	rejecting := newFakeSnapshot().set("1.2.3.4", projectionAt(now, 72))
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	p.SetQualitySnapshot(rejecting)
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, nil)
	if p.View().Size() != 0 {
		t.Fatal("node below min_purity must not be routable")
	}

	admitting := newFakeSnapshot().set("1.2.3.4", projectionAt(now, 90))
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy)
	p2.SetQualitySnapshot(admitting)
	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, nil)
	if p2.View().Size() != 1 {
		t.Fatal("node above min_purity must be routable")
	}
}

// projectionAt builds a projected assessment anchored to the caller's clock.
func projectionAt(now time.Time, score int) intel.AssessmentLite {
	lite := lite()
	lite.Score = int8(score)
	lite.ValidUntil = now.Add(24 * time.Hour).UnixNano()
	lite.ComputedAt = now.Add(-time.Hour).UnixNano()
	return lite
}

// TestPlatform_FullRebuild_QualityPolicyWithoutSnapshotFailsClosed documents the
// fail-closed rule: a non-empty policy that cannot be verified admits nothing.
func TestPlatform_FullRebuild_QualityPolicyWithoutSnapshotFailsClosed(t *testing.T) {
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	minPurity := 10
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, model.QualityPolicy{MinPurity: &minPurity})
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, nil)
	if p.View().Size() != 0 {
		t.Fatal("a non-empty policy without a snapshot reader must reject every node")
	}
}

func TestExplainQuality(t *testing.T) {
	entry := admitEntry(t)
	minPurity := 80
	policy := model.QualityPolicy{
		MinPurity:       &minPurity,
		AllowedVerdicts: []string{"favorable", "caution", "review"},
	}
	snap := newFakeSnapshot().set("1.2.3.4", func() intel.AssessmentLite {
		lite := lite()
		lite.Score = 72
		return lite
	}())

	decisions := ExplainQuality(policy, entry, snap, admitNow)
	byRule := map[string]QualityDecision{}
	for _, d := range decisions {
		byRule[d.Rule] = d
	}
	if d, ok := byRule["quality.min_purity"]; !ok || d.Passed {
		t.Fatalf("quality.min_purity must fail, got %+v", decisions)
	} else if d.Detail != "score=72<80" {
		t.Fatalf("detail = %q, want %q", d.Detail, "score=72<80")
	}
	if d, ok := byRule["quality.allowed_verdicts"]; !ok || !d.Passed {
		t.Fatalf("quality.allowed_verdicts must pass, got %+v", decisions)
	}
	if decisions[0].Rule != "quality.egress" {
		t.Fatalf("first rule = %q, want quality.egress", decisions[0].Rule)
	}
	if !strings.Contains(byRule["quality.exclude_tor"].Detail, "tor") {
		t.Fatalf("tor rule detail = %q", byRule["quality.exclude_tor"].Detail)
	}
}

func TestExplainQuality_UnknownAndEmpty(t *testing.T) {
	entry := admitEntry(t)
	minPurity := 10
	policy := model.QualityPolicy{MinPurity: &minPurity}

	empty := ExplainQuality(model.QualityPolicy{}, entry, newFakeSnapshot(), admitNow)
	if len(empty) != 1 || !empty[0].Passed {
		t.Fatalf("empty policy explain = %+v", empty)
	}

	unknown := ExplainQuality(policy, entry, newFakeSnapshot(), admitNow)
	if len(unknown) != 2 || unknown[1].Rule != "quality.unknown" || unknown[1].Passed {
		t.Fatalf("unknown explain = %+v", unknown)
	}

	allowed := ExplainQuality(model.QualityPolicy{MinPurity: &minPurity, UnknownAction: "allow"}, entry, newFakeSnapshot(), admitNow)
	if len(allowed) != 2 || !allowed[1].Passed {
		t.Fatalf("allowed explain = %+v", allowed)
	}
}
