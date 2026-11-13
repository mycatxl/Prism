package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/platform"
	"prism/internal/quality"
)

func TestPurityLatestWritePublishesFailureBeforeReturning(t *testing.T) {
	for code, reason := range map[string]string{
		"PROVIDER_LIMIT": "IPPURE_RATE_LIMITED", "PROVIDER_TIMEOUT": "IPPURE_TIMEOUT",
		"PROVIDER_AUTH": "IPPURE_AUTH_FAILED", "PROVIDER_RESPONSE": "IPPURE_INVALID_RESPONSE",
		"PROVIDER_UNAVAILABLE": "IPPURE_PROVIDER_UNAVAILABLE", "EGRESS_MISMATCH": "IPPURE_EGRESS_MISMATCH",
		"PROVIDER_NETWORK": "NO_VALID_IPPURE_EVIDENCE",
	} {
		t.Run(code, func(t *testing.T) {
			f := buildIntelNodeFixture(t)
			entry, _ := f.cp.Pool.GetEntry(f.clean)
			ip, ctx, score := entry.GetEgressIP(), context.Background(), 20
			ev := quality.Evidence{IP: ip.String(), Provider: "ippure", FraudScore: &score, ObservedAt: f.now, ValidUntil: f.now.Add(time.Hour)}
			body, _ := json.Marshal(ev)
			success := store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk, ObservedAtNs: f.now.UnixNano(), ValidUntilNs: ev.ValidUntil.UnixNano(), NormalizedJSON: string(body)}
			if err := f.svc.Store().UpsertEvidence(ctx, success); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.Assess(ctx, ip); err != nil {
				t.Fatal(err)
			}
			if ok, _ := platform.AdmitQuality(model.QualityPolicy{}, entry, f.svc.Snapshot(), time.Now()); !ok {
				t.Fatal("real score did not satisfy Default measurement floor")
			}
			failure := success
			failure.Status, failure.ErrorCode, failure.NormalizedJSON = store.StatusError, code, ""
			failure.ObservedAtNs++
			if err := f.svc.Store().UpsertEvidence(ctx, failure); err != nil {
				t.Fatal(err)
			}
			// No explicit Assess or API read may be needed to revoke admission.
			stored, ok, err := f.svc.Store().GetAssessment(ctx, ip.String())
			if err != nil || !ok {
				t.Fatal("assessment missing after evidence write")
			}
			fact := intel.AssessmentLiteFromRow(stored).FactValues()["purity"]
			if stored.PurityScore.Valid || fact.Cause != reason || fact.ErrorCode != code {
				t.Errorf("persisted assessment reused success: score=%v cause=%s code=%s", stored.PurityScore.Valid, fact.Cause, fact.ErrorCode)
			}
			live, ok := f.svc.Snapshot().Assessment(ip)
			if !ok || live.ScoreValue() != nil || live.FactValues()["purity"].Cause != reason {
				t.Error("route snapshot reused success")
			}
			if ok, _ := platform.AdmitQuality(model.QualityPolicy{}, entry, f.svc.Snapshot(), time.Now()); ok {
				t.Error("Default admitted failed latest measurement")
			}
			// A delayed older observation must not replace the latest failure.
			if err := f.svc.Store().UpsertEvidence(ctx, success); err != nil {
				t.Fatal(err)
			}
			latest, _, err := f.svc.Store().GetEvidence(ctx, ip.String(), "ippure")
			if err != nil || latest.Status != store.StatusError || latest.ErrorCode != code {
				t.Error("older success overwrote latest failure")
			}
			if err := f.svc.ReloadSnapshot(ctx); err != nil {
				t.Fatal(err)
			}
			live, _ = f.svc.Snapshot().Assessment(ip)
			if live.ScoreValue() != nil || live.FactValues()["purity"].Cause != reason {
				t.Error("reload resurrected superseded success")
			}
		})
	}
}

func TestPurityEvidenceCommitCannotLeaveScoreBeforePublication(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip, ctx := entry.GetEgressIP(), context.Background()
	row := store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk,
		ObservedAtNs: f.now.UnixNano(), ValidUntilNs: f.now.Add(time.Hour).UnixNano(), NormalizedJSON: `{"fraud_score":20}`}
	if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
		t.Fatal(err)
	}
	// Simulate stopping after the durable evidence commit, before the service's
	// publication callback. Restart must not load a superseded current profile.
	f.svc.Store().SetEvidenceCommitted(nil)
	row.Status, row.ErrorCode, row.NormalizedJSON = store.StatusError, "PROVIDER_TIMEOUT", ""
	row.ObservedAtNs++
	if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
		t.Fatal(err)
	}
	stored, _, err := f.svc.Store().GetAssessment(ctx, ip.String())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PurityScore.Valid {
		t.Error("evidence commit left a durable superseded score")
	}
	if err := f.svc.Start(); err != nil {
		t.Fatal(err)
	}
	live, ok := f.svc.Snapshot().Assessment(ip)
	if !ok || live.ScoreValue() != nil || live.FactValues()["purity"].Cause != "IPPURE_TIMEOUT" {
		t.Error("startup did not recover the latest failure")
	}
}

func TestPurityConcurrentEvidenceAndAssessmentKeepLatestFailure(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip, ctx := entry.GetEgressIP(), context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			row := store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk,
				ObservedAtNs: f.now.Add(-time.Second).UnixNano() + int64(i), ValidUntilNs: f.now.Add(time.Hour).UnixNano(), NormalizedJSON: `{"fraud_score":20}`}
			if i == 11 {
				row.Status, row.ErrorCode, row.NormalizedJSON = store.StatusError, "PROVIDER_LIMIT", ""
			}
			if err := f.svc.Store().UpsertEvidence(ctx, row); err != nil {
				t.Error(err)
			}
			if _, err := f.svc.Assess(ctx, ip); err != nil {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	stored, _, err := f.svc.Store().GetAssessment(ctx, ip.String())
	if err != nil {
		t.Fatal(err)
	}
	live, ok := f.svc.Snapshot().Assessment(ip)
	if stored.PurityScore.Valid || !ok || live.ScoreValue() != nil || live.FactValues()["purity"].Cause != "IPPURE_RATE_LIMITED" {
		t.Fatal("concurrent write/assessment resurrected an older success")
	}
}
