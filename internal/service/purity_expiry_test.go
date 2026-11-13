package service

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/store"
)

func TestPurityExpiredMalformedEvidenceKeepsExpiryAcrossConsumers(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip, ctx := entry.GetEgressIP(), context.Background()
	for _, payload := range []string{"{", `{"ip":"invalid","fraud_score":101}`, `{"observed_at":"bad"}`} {
		row := store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk, ObservedAtNs: f.now.Add(-2 * time.Hour).UnixNano(), ValidUntilNs: f.now.Add(-time.Hour).UnixNano(), NormalizedJSON: payload}
		if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
			t.Fatal(err)
		}
		stored, err := f.svc.Assess(ctx, ip)
		if err != nil {
			t.Fatal(err)
		}
		if stored.PurityScore.Valid || intel.AssessmentLiteFromRow(stored).FactValues()["purity"].Cause != "EVIDENCE_EXPIRED" {
			t.Error("persisted assessment masked expiry")
		}
		summary, err := f.cp.QualityIP(ctx, ip.String())
		if err != nil {
			t.Fatal(err)
		}
		if summary.Assessment.PurityScore != nil || summary.Assessment.Facts["purity"].Cause != "EVIDENCE_EXPIRED" {
			t.Errorf("quality cause=%s", summary.Assessment.Facts["purity"].Cause)
		}
		detail, err := f.cp.IntelIPReport(ctx, ip.String())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Projection.ScoreValue() != nil || detail.Projection.FactValues()["purity"].Cause != "EVIDENCE_EXPIRED" {
			t.Error("intel masked expiry")
		}
	}
}
