package intel

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestInvalidStoredScoreCannotAcquireFavorableBand(t *testing.T) {
	for _, score := range []sql.NullInt64{{}, {Int64: -256, Valid: true}, {Int64: -1, Valid: true}, {Int64: 101, Valid: true}, {Int64: 256, Valid: true}} {
		row := store.Assessment{PurityScore: score, PurityBand: "excellent", Verdict: "favorable"}
		lite := AssessmentLiteFromRow(row)
		if lite.ScoreValue() != nil || lite.Band != BandUnknown || lite.Verdict == VerdictFavorable {
			t.Fatalf("invalid score promoted: %+v", lite)
		}
	}
	zero := AssessmentLiteFromRow(store.Assessment{Profile: assess.Profile, State: "valid", PurityScore: sql.NullInt64{Int64: 0, Valid: true}, PurityBand: "excellent", Verdict: "favorable"})
	if zero.ScoreValue() == nil || *zero.ScoreValue() != 0 || zero.Band != BandPoor {
		t.Fatal("real zero lost")
	}
}

func TestStartupRecomputesLegacyMissingSignalScore(t *testing.T) {
	enabled := false
	svc := newAutoEnqueueService(t, &enabled)
	defer svc.Stop()
	now := time.Now()
	ctx := context.Background()
	ip := "203.0.113.10"
	no := false
	ev := quality.Evidence{IP: ip, Provider: "ipapi_is_node", Signals: quality.Signals{Proxy: &no}, ObservedAt: now, ValidUntil: now.Add(time.Hour)}
	encoded, _ := json.Marshal(ev)
	if err := svc.Store().UpsertEvidence(ctx, store.Evidence{IP: ip, Provider: ev.Provider, Status: store.StatusOk, NormalizedJSON: string(encoded), ObservedAtNs: now.UnixNano(), ValidUntilNs: ev.ValidUntil.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store().UpsertAssessment(ctx, store.Assessment{IP: ip, Profile: "prism-purity-v2", State: "valid", Verdict: "favorable", PurityScore: sql.NullInt64{Int64: 100, Valid: true}, PurityBand: "excellent", ValidUntilNs: ev.ValidUntil.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	row, ok, err := svc.Store().GetAssessment(ctx, ip)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.Profile != assess.Profile || row.PurityScore.Valid || row.Verdict == "favorable" || row.PurityBand != "unknown" {
		t.Fatal("legacy fabricated score survived startup")
	}
	lite, ok := svc.Snapshot().Assessment(netip.MustParseAddr(ip))
	if !ok || lite.ScoreValue() != nil || lite.Verdict == VerdictFavorable {
		t.Fatal("legacy result published")
	}
}
