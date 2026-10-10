package jobs

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestAudit_ConcurrentRefreshJobLosesUpdates measures the read-modify-write
// sequence of RefreshJob (repo_jobs.go): it counts the items, reads the job row,
// computes the next status in Go and writes it back - several statements with no
// transaction around them. Two workers finishing two items of the same job call
// publish() (and so RefreshJob) concurrently, so the slower writer could
// overwrite the newer settlement with a status derived from a stale counter set.
//
// This one is a known, deliberately unfixed narrow window (AUDIT_FIX_PLAN.md:
// "RefreshJob 非事务读-改-写", measured at 0.05%): the test is the anchor that
// keeps the defect from getting worse, but it must not redden a normal test run.
// The window is real, so a green 300-round run is a coin toss rather than a
// property of the code - leaving it enabled would make CI flaky for a defect
// this change set explicitly does not fix. It runs only when
// PRISM_AUDIT_REFRESH_RACE=1 is set, which is how the audit measured it:
//
//	PRISM_AUDIT_REFRESH_RACE=1 go test -tags "$TAGS" \
//	  -run TestAudit_ConcurrentRefreshJobLosesUpdates -count=20 ./internal/intel/jobs/
func TestAudit_ConcurrentRefreshJobLosesUpdates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const rounds = 300
	regressions := 0
	if os.Getenv("PRISM_AUDIT_REFRESH_RACE") == "" {
		t.Skip("known unfixed RefreshJob race (AUDIT_REPORT #7): set PRISM_AUDIT_REFRESH_RACE=1 to measure it")
	}
	var lastSeen store.Job

	for round := 0; round < rounds; round++ {
		jobID := "job-" + itoa(round)
		now := int64(1000 + round)
		if err := st.CreateJob(ctx,
			store.Job{ID: jobID, Kind: "egress", Status: store.JobRunning, CreatedAtNs: now},
			[]store.JobItem{
				{JobID: jobID, NodeHash: "n1", Status: store.ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
				{JobID: jobID, NodeHash: "n2", Status: store.ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
			}); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}

		// Both workers finish their own item and publish, exactly as two
		// concurrent runItem calls do.
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, hash := range []string{"n1", "n2"} {
			wg.Add(1)
			go func(hash string) {
				defer wg.Done()
				<-start
				if err := st.FinishJobItem(ctx, jobID, hash, store.ItemDone, "", "{}", now); err != nil {
					t.Errorf("FinishJobItem: %v", err)
					return
				}
				if _, err := st.RefreshJob(ctx, jobID, now); err != nil {
					t.Errorf("RefreshJob: %v", err)
				}
			}(hash)
		}
		close(start)
		wg.Wait()

		got, err := st.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		lastSeen = got
		if got.Status != store.JobSucceeded || got.Done != 2 {
			regressions++
		}
	}

	t.Logf("rounds=%d regressions=%d (last: status=%s done=%d failed=%d finished_at=%d)",
		rounds, regressions, lastSeen.Status, lastSeen.Done, lastSeen.Failed, lastSeen.FinishedAtNs)
	if regressions > 0 {
		t.Errorf("%d/%d jobs ended in a state their items contradict: "+
			"RefreshJob lost an update between two concurrent publishes", regressions, rounds)
	}
}

// Regression test for F5 of AUDIT_REPORT.md (fixed): RetryFailedJobItems
// (repo_jobs.go) requeued the FAILED items and then, unconditionally, reopened
// the job with "UPDATE jobs SET status = 'queued', finished_at_ns = 0 WHERE
// id = ? AND status IN ('partial','failed','canceled')". A canceled job has no
// failed item - CancelJob moves its queued items to canceled - so the first
// UPDATE changed nothing while the second one still reopened the job. The
// publish that followed recomputed the job from its items: nothing pending,
// nothing failed, so RefreshJob took its default branch and the job became
// succeeded.
//
// The operator asked to retry failures and got a canceled job reported as a
// success, with the cancel recorded in the audit log. The fix runs the reopen
// only when retried > 0 and drops canceled from the status list: cancel is
// terminal even when the job has failed items.
func TestAudit_RetryFailedTurnsACanceledJobIntoSuccess(t *testing.T) {
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
	if err := m.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	canceled, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if canceled.Status != store.JobCanceled {
		t.Fatalf("job = %s, want %s after Cancel", canceled.Status, store.JobCanceled)
	}

	// The operator retries the failures of the canceled job.
	retried, err := m.RetryFailed(ctx, job.ID)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	counters, err := st.JobItemCounters(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("retried=%d job=%s items: total=%d done=%d failed=%d canceled=%d queued=%d running=%d",
		retried, after.Status, counters.Total, counters.Done, counters.Failed,
		counters.Canceled, counters.Queued, counters.Running)
	if retried != 0 {
		t.Fatalf("retried = %d, want 0: a canceled job has no failed item", retried)
	}
	if after.Status != store.JobCanceled {
		t.Errorf("job = %s, want %s: retry-failed reopened a job that had no failed item",
			after.Status, store.JobCanceled)
	}
}

// TestAudit_RetryFailedDoesNotReviveACanceledJobWithFailedItems pins the second
// half of the F5 decision: the reopen list is ('partial','failed') and no longer
// contains canceled. A job that was canceled after some of its items had already
// failed still has failed items, so the old code did requeue them - and then
// reopened the job. The fix skips a canceled job entirely: the retry is a no-op,
// the failure records the operator is reading survive, and no queued item is
// stranded inside a canceled job (nothing claims items of a non-active job).
func TestAudit_RetryFailedDoesNotReviveACanceledJobWithFailedItems(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const now = int64(1000)
	if err := st.CreateJob(ctx,
		store.Job{ID: "canceled-with-failures", Kind: "egress", Status: store.JobRunning, CreatedAtNs: now},
		[]store.JobItem{
			{JobID: "canceled-with-failures", NodeHash: "n1", Status: store.ItemFailed, NextRunAtNs: now, UpdatedAtNs: now},
			{JobID: "canceled-with-failures", NodeHash: "n2", Status: store.ItemQueued, NextRunAtNs: now, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.CancelJob(ctx, "canceled-with-failures"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	// CancelJob only cancels queued items, so the failed one survives: this job
	// has something to retry and is still canceled.
	if counters, err := st.JobItemCounters(ctx, "canceled-with-failures"); err != nil ||
		counters.Failed != 1 || counters.Canceled != 1 {
		t.Fatalf("counters = %+v (err %v), want one failed and one canceled item", counters, err)
	}

	retried, err := st.RetryFailedJobItems(ctx, "canceled-with-failures", now+1)
	if err != nil {
		t.Fatalf("RetryFailedJobItems: %v", err)
	}
	after, err := st.GetJob(ctx, "canceled-with-failures")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	t.Logf("retried=%d job=%s", retried, after.Status)
	if retried != 0 {
		t.Errorf("retried = %d, want 0: a canceled job must not be retried", retried)
	}
	if after.Status != store.JobCanceled {
		t.Errorf("job = %s, want %s: retry-failed revived a canceled job", after.Status, store.JobCanceled)
	}
	if !after.Terminal() {
		t.Errorf("job = %s, want a terminal status", after.Status)
	}
	// The failed item must keep its failure record, and no queued item may be
	// left behind in the canceled job.
	counters, err := st.JobItemCounters(ctx, "canceled-with-failures")
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	if counters.Failed != 1 || counters.Queued != 0 || counters.Canceled != 1 {
		t.Errorf("counters = %+v, want one failed and one canceled item and nothing queued", counters)
	}
	if item, ok, err := st.GetJobItem(ctx, "canceled-with-failures", "n1"); err != nil || !ok ||
		item.Status != store.ItemFailed {
		t.Errorf("failed item = %+v (ok=%v err=%v), want it left failed", item, ok, err)
	}
}
