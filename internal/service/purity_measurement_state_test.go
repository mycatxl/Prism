package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
)

func TestPurityActiveMeasurementDoesNotDisplayPreviousScore(t *testing.T) {
	for _, state := range []string{"queued", "running"} {
		t.Run(state, func(t *testing.T) {
			f := buildIntelNodeFixture(t)
			entry, _ := f.cp.Pool.GetEntry(f.clean)
			ip, ctx := entry.GetEgressIP(), context.Background()
			if err := f.svc.Store().UpsertEvidence(ctx, store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk, ObservedAtNs: f.now.UnixNano(), ValidUntilNs: f.now.Add(time.Hour).UnixNano(), NormalizedJSON: `{"fraud_score":20}`}); err != nil {
				t.Fatal(err)
			}
			_, err := f.svc.Store().DB().ExecContext(ctx, `INSERT INTO node_measurements VALUES(?, 'refresh', ?, 0, 0, '', ?, 1)`, f.clean.Hex(), state, f.now.Add(time.Millisecond).UnixNano())
			if err != nil {
				t.Fatal(err)
			}
			rows := []NodeSummary{f.cp.nodeEntryToSummary(f.clean, entry)}
			f.cp.FillNodeIntelEgress(ctx, rows)
			f.cp.FillNodeQualityEvidence(ctx, rows)
			if rows[0].Intel.PurityScore != nil || rows[0].Intel.Facts["purity"].Cause != "MEASUREMENT_"+strings.ToUpper(state) {
				t.Fatal("active measurement still displays previous score")
			}
		})
	}
}

func TestPurityJobLifecycleClosesRouteUntilMeasurementCompletes(t *testing.T) {
	f := buildIntelNodeFixture(t)
	entry, _ := f.cp.Pool.GetEntry(f.clean)
	ip, ctx := entry.GetEgressIP(), context.Background()
	st := f.svc.Store()
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: f.clean.Hex(), IPv4: ip.String()}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertEvidence(ctx, store.Evidence{IP: ip.String(), Provider: "ippure", Status: store.StatusOk, ObservedAtNs: f.now.UnixNano(), ValidUntilNs: f.now.Add(time.Hour).UnixNano(), NormalizedJSON: `{"fraud_score":20}`}); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		if ok, _ := platform.AdmitQuality(model.QualityPolicy{}, entry, f.svc.Snapshot(), time.Now()); ok != want {
			t.Fatalf("route admission=%v want=%v", ok, want)
		}
	}
	check(true)
	if err := st.CreateJob(ctx, store.Job{ID: "refresh", Kind: "full", CreatedAtNs: f.now.UnixNano()}, []store.JobItem{{NodeHash: f.clean.Hex(), Status: store.ItemQueued, UpdatedAtNs: f.now.UnixNano()}}); err != nil {
		t.Fatal(err)
	}
	check(false)
	report, err := f.cp.IntelNodeReport(ctx, f.clean.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if report.IPv4.PurityScore.Valid || report.V4Projected.FactValues()["purity"].Cause != "MEASUREMENT_QUEUED" {
		t.Fatal("node report exposed queued score")
	}
	if key := f.cp.NodeListSortKey(f.clean, entry, "purity_score"); key.Present {
		t.Fatal("queued score remained sortable as rated")
	}
	reason, state := "MEASUREMENT_QUEUED", "unrated"
	matched := 0
	_, err = f.cp.RangeFilteredNodesContext(ctx, NodeFilters{PurityReason: &reason, PurityState: &state}, func(_ node.Hash, n NodeSummary) bool { matched++; return true })
	if err != nil || matched != 1 {
		t.Fatal("queued reason filter did not match exactly one node")
	}
	if err := f.svc.ReloadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := st.ClaimJobItems(ctx, store.ClaimOptions{Limit: 1, NowNs: time.Now().UnixNano(), LeaseUntilNs: time.Now().Add(time.Minute).UnixNano(), Owner: "test"}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := st.FinishJobItem(ctx, "refresh", f.clean.Hex(), store.ItemDone, "", "{}", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := st.CreateJob(ctx, store.Job{ID: "failed-refresh", Kind: "full", CreatedAtNs: time.Now().UnixNano()}, []store.JobItem{{NodeHash: f.clean.Hex(), Status: store.ItemQueued, UpdatedAtNs: time.Now().UnixNano()}}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJobItem(ctx, "failed-refresh", f.clean.Hex(), store.ItemFailed, "TRACE_TIMEOUT", "{}", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	check(false)
	live, _ := f.svc.Snapshot().NodeAssessment(f.clean.Hex(), ip)
	if fact := live.FactValues()["purity"]; fact.Cause != "MEASUREMENT_FAILED" || fact.ErrorCode != "TRACE_TIMEOUT" {
		t.Fatal("terminal measurement failure lost cause")
	}
}
