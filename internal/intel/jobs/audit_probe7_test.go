package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestAudit_CanceledJobRejectsALateDefer is the second half of the F1/F4
// cancellation contract, added after the first review pass.
//
// The store guard only looks at the ITEM status (status IN ('queued','running')),
// so a defer that lands while the row is still running used to be accepted even
// though the job had just been canceled: the item went back to queued inside a
// terminal job, where claimOne (it only claims inside the active set) and
// RetryFailedJobItems (it refuses canceled jobs) can never reach it.
//
// CancelJob now settles every unsettled item - the running ones included - in
// the same transaction that cancels the job. The item is therefore already
// canceled when the late write arrives, and the guard refuses it. The probe
// reproduces the ordering by canceling between the gate read and the write.
func TestAudit_CanceledJobRejectsALateDefer(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	inner := &scriptedRunner{}
	m := newTestManager(t, st, clock, inner, scope, nil)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}

	// The operator cancels, then the worker's late defer lands.
	if err := m.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// DeferJobItem must now be refused: the row was settled by CancelJob.
	if err := st.DeferJobItem(ctx, job.ID, item.NodeHash, 0, clock.Now().UnixNano(), "{}", clock.Now().UnixNano()); !errors.Is(err, store.ErrJobItemSettled) {
		t.Errorf("late DeferJobItem = %v, want ErrJobItemSettled", err)
	}
	// And the manager path stays silent, leaving the canceled item alone.
	m.deferItem(ctx, item, 0, clock.Now().UnixNano(), "", nil)

	got, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}
	counters, err := st.JobItemCounters(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("item=%s counters: queued=%d running=%d canceled=%d", got.Status, counters.Queued, counters.Running, counters.Canceled)
	if got.Status != store.ItemCanceled {
		t.Errorf("item = %s, want %s", got.Status, store.ItemCanceled)
	}
	if counters.Queued > 0 || counters.Running > 0 {
		t.Errorf("canceled job holds %d queued / %d running item(s)", counters.Queued, counters.Running)
	}
}
