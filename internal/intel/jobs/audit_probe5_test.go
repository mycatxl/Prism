package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestAudit_RetryFailedDoesNotResurrectAJobWhoseItemsAreGone probes the second
// escape from a terminal job status.
//
// DeleteJobItemsFinishedBefore (repo_jobs.go:684) removes the items of jobs that
// finished more than seven days ago, but it never touches the jobs row: the job
// keeps status=partial with failed=1. The job row itself is kept for thirty days
// (DeleteJobsFinishedBefore), so for a three-week window the API still lists the
// job with a failed counter and the UI still offers "retry failed items".
//
// RetryFailedJobItems finds no failed item and returns 0, which is right. But
// Manager.RetryFailed (manager.go:305-312) publishes unconditionally, and
// RefreshJob (repo_jobs.go:616-643) recomputes the job from an item set that is
// now EMPTY: no pending, no failed, so it takes its default branch and writes
// succeeded. A job the operator saw as partial turns into a success because its
// items were pruned.
func TestAudit_RetryFailedDoesNotResurrectAJobWhoseItemsAreGone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	m := newTestManager(t, st, clock, &scriptedRunner{},
		ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
			return []string{"node-1", "node-2"}, nil
		}), nil)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Settle the job as partial: one failed item, no pending one.
	if _, err := st.FailPendingJobItems(ctx, job.ID, CodeJobTimeout, clock.Now().UnixNano()); err != nil {
		t.Fatalf("FailPendingJobItems: %v", err)
	}
	partial, err := st.RefreshJob(ctx, job.ID, clock.Now().UnixNano())
	if err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}
	if partial.Status != store.JobPartial {
		t.Fatalf("job = %s, want %s before the probe", partial.Status, store.JobPartial)
	}

	// Three weeks later the retention pass has pruned the items. The job row
	// survives, so the API and the UI still show it as partial with a failure.
	clock.advance(21 * 24 * time.Hour)
	if _, err := st.DeleteJobItemsFinishedBefore(ctx, clock.Now().UnixNano()); err != nil {
		t.Fatalf("DeleteJobItemsFinishedBefore: %v", err)
	}
	pruned, err := st.JobItemCounters(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	if pruned.Total != 0 {
		t.Fatalf("items were not pruned: total=%d", pruned.Total)
	}

	// The operator clicks "retry failed items" on the job the UI still lists.
	retried, err := m.RetryFailed(ctx, job.ID)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	t.Logf("retried=%d job=%s (finished_at=%d)", retried, after.Status, after.FinishedAtNs)
	if retried != 0 {
		t.Fatalf("retried = %d, want 0: the job has no item left to retry", retried)
	}
	if after.Status != store.JobPartial {
		t.Errorf("job = %s, want %s: retrying a job with no failed item rewrote its terminal status",
			after.Status, store.JobPartial)
	}
}
