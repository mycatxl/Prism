package service

import (
	"context"
	"encoding/json"
	"prism/internal/intel/store"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestPurityLatestFailureRejectsPreviouslyStoredScore(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip := entry.GetEgressIP()
	ctx := context.Background()
	risk := 20
	ev := quality.Evidence{IP: ip.String(), Provider: "ippure", FraudScore: &risk, ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)}
	body, _ := json.Marshal(ev)
	row := store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk, ObservedAtNs: f.now.UnixNano(), ValidUntilNs: ev.ValidUntil.UnixNano(), NormalizedJSON: string(body)}
	if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Assess(ctx, ip); err != nil {
		t.Fatal(err)
	}
	row.Status = store.StatusError
	row.ErrorCode = "PROVIDER_LIMIT"
	row.NormalizedJSON = ""
	row.ObservedAtNs++
	if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
		t.Fatal(err)
	}
	summary, err := f.cp.QualityIP(ctx, ip.String())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Assessment.PurityScore != nil || summary.Assessment.Facts["purity"].Cause != "IPPURE_RATE_LIMITED" {
		t.Error("quality API reused superseded success")
	}
	detail, err := f.cp.IntelIPReport(ctx, ip.String())
	if err != nil {
		t.Fatal(err)
	}
	if detail.Assessment.PurityScore.Valid {
		t.Error("intel API reused superseded success")
	}
}
