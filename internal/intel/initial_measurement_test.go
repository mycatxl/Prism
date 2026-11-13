package intel

import (
	"context"
	"prism/internal/intel/jobs"
	"testing"
)

func TestInitialMeasurementIsFullBoundedAndSurvivesHistoryPruning(t *testing.T) {
	enabled := true
	s := newAutoEnqueueService(t, &enabled)
	ctx := context.Background()
	h := testNodeHash(t, "first-full")
	if n, err := s.EnsureInitialMeasurements(ctx, []string{h}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := s.EnsureInitialMeasurements(ctx, []string{h}); err != nil || n != 0 {
		t.Fatal("duplicate initial job", n, err)
	}
	db := s.Store().DB()
	if _, err := db.Exec(`UPDATE job_items SET status='failed',attempts=5,error_code='EGRESS_PROBE_FAILED',updated_at_ns=updated_at_ns+1 WHERE node_hash=?`, h); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM job_items WHERE node_hash=?`, h); err != nil {
		t.Fatal(err)
	}
	states, err := s.Store().LatestMeasurementStates(ctx, []string{h})
	if err != nil || states[h].State != "measured_failed" || states[h].Attempts != 5 {
		t.Fatal(states, err)
	}
	if n, err := s.EnsureInitialMeasurements(ctx, []string{h}); err != nil || n != 0 {
		t.Fatal("history pruning reset retry bound", n, err)
	}
}

func TestChecksOnlyHistoryStillGetsFullMeasurement(t *testing.T) {
	enabled := true
	s := newAutoEnqueueService(t, &enabled)
	ctx := context.Background()
	h := testNodeHash(t, "checks-only")
	_, err := s.CreateJob(ctx, jobs.Request{Kind: jobs.KindChecks, Scope: jobs.Scope{NodeHashes: []string{h}}}, "fixture", 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.EnsureInitialMeasurements(ctx, []string{h}); err != nil || n != 1 {
		t.Fatal("checks-only history incorrectly satisfied full measurement", n, err)
	}
}
