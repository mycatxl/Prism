package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestCanceledJobSettlesTheInFlightItem probes the asymmetry the settlement
// guard left behind between deferItem and finishItem.
//
// CancelJob only moves the QUEUED items to canceled (repo_jobs.go:213-215), so
// an item that is inside a step when the operator cancels stays running. The
// worker is supposed to settle it as canceled: deferItem does exactly that
// (manager.go:760-762) and so does runItem's pre-loop check (manager.go:620-623),
// but finishItem - the path every normal step result takes (done, skipped,
// failed) - returns without writing anything (manager.go:731-734).
//
// The item therefore stays running inside a terminal job. Nothing can reach it:
// the scheduler lists JOBS (ListActiveJobs filters status IN ('queued','running')),
// claimOne only claims inside the active set, RetryFailedJobItems refuses
// canceled jobs and the timeout sweep only walks active jobs. It keeps
// inflating the job's pending counter until the seven-day item retention pass
// deletes it.
func TestCanceledJobSettlesTheInFlightItem(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	steps := PendingSteps(KindEgress, -1)
	last := steps[len(steps)-1]

	// The step returns a plain success, so the loop tail calls
	// finishItem(ItemDone) - the path this probe is about.
	inner := &scriptedRunner{}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	m := newTestManager(t, st, clock, inner, scope, nil)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}

	canceled := false
	runner := &gatedRunner{onStep: last, inner: inner, between: func() {
		if err := m.Cancel(ctx, job.ID); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		canceled = true
	}}
	m.runner = runner

	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)
	if !canceled {
		t.Fatal("the probe never reached the last step, so the window was not exercised")
	}

	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	got, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}
	counters, err := st.JobItemCounters(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("job=%s item=%s counters: queued=%d running=%d canceled=%d done=%d",
		after.Status, got.Status, counters.Queued, counters.Running, counters.Canceled, counters.Done)

	if after.Status != store.JobCanceled {
		t.Errorf("job = %s, want %s", after.Status, store.JobCanceled)
	}
	// The operator asked for this job to stop, so the item that was in flight
	// must be recorded as canceled - not left running, and not marked done.
	if got.Status != store.ItemCanceled {
		t.Errorf("item = %s, want %s: finishItem left an in-flight item of a canceled job unsettled",
			got.Status, store.ItemCanceled)
	}
	if counters.Queued > 0 || counters.Running > 0 {
		t.Errorf("canceled job holds %d queued and %d running item(s): no path can ever reach them",
			counters.Queued, counters.Running)
	}
}
