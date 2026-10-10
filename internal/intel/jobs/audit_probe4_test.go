package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestAudit_CanceledJobDoesNotStrandAnInFlightItem probes the gap the F1 and F4
// guards leave together.
//
// CancelJob only moves the QUEUED items to canceled (repo_jobs.go:213-215); the
// running ones are left for the worker, which is supposed to notice through the
// jobGate check at the top of its step loop and finish the item as canceled. A
// step that returns a deferrable result never reaches that check again: the loop
// body calls deferItem and returns. DeferJobItem's guard (status IN
// ('queued','running')) therefore still accepts the write, and the item goes back
// to queued inside a job that is already terminal - the stranded state F1 exists
// to prevent. RetryFailedJobItems used to be an escape hatch (it reopened a
// canceled job), but F5 now returns early for canceled jobs, so nothing can ever
// claim or retry this item again.
func TestAudit_CanceledJobDoesNotStrandAnInFlightItem(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	steps := PendingSteps(KindEgress, -1)
	last := steps[len(steps)-1]

	// The last step parks the item: this is the deferral path, taken after the
	// loop's gate check already passed.
	inner := &scriptedRunner{handlers: map[Step]StepResult{
		last: {DeferUntilNs: clock.Now().UnixNano() + int64(30*time.Second)},
	}}
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

	// The operator cancels while the item is inside its last step.
	canceled := false
	runner := &auditRunner{onStep: last, inner: inner, between: func() {
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
	t.Logf("job=%s item=%s (error=%s) counters: queued=%d running=%d canceled=%d failed=%d",
		after.Status, got.Status, got.ErrorCode,
		counters.Queued, counters.Running, counters.Canceled, counters.Failed)

	if !after.Terminal() {
		t.Fatalf("job = %s, want a terminal status", after.Status)
	}
	// A terminal job must not hold an item that no worker can ever claim: the
	// scheduler lists jobs, not items, so a queued item in a terminal job is
	// unreachable.
	if counters.Queued > 0 || counters.Running > 0 {
		t.Errorf("terminal job %s holds %d queued and %d running item(s): "+
			"neither ListActiveJobs nor RetryFailedJobItems can reach them",
			after.Status, counters.Queued, counters.Running)
	}

	// And the retry path must not be the only way out: it now refuses canceled
	// jobs on purpose.
	retried, err := m.RetryFailed(ctx, job.ID)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if retried != 0 {
		t.Fatalf("retried = %d, want 0 for a canceled job", retried)
	}
	stillStranded, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}
	if stillStranded.Status == store.ItemQueued || stillStranded.Status == store.ItemRunning {
		t.Errorf("after retry the item is still %s in a %s job: it can never be claimed",
			stillStranded.Status, after.Status)
	}
}
