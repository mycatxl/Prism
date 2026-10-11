package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// newDeferredItem drives one item through a failing step and returns the item
// row as the store left it. The runner fails every step with stepError, and the
// item still has attempts left, so runItem takes the deferrable-error branch
// (manager.go:664) - the branch that hands deferItem the step's own error code.
func newDeferredItem(t *testing.T, stepError string) store.JobItem {
	t.Helper()
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	st := newTestStore(t)

	handlers := make(map[Step]StepResult)
	for _, step := range Plan(KindEgress) {
		handlers[step] = StepResult{ErrorCode: stepError, Summary: map[string]any{"step": int(step)}}
	}
	runner := &scriptedRunner{handlers: handlers}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	m := newTestManager(t, st, clock, runner, scope, nil)
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
	m.runItem(ctx, item)

	after, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}
	return after
}

// TestDeferItemParksTheItemInsteadOfFailingIt pins the part of deferItem that
// does not depend on #35: a deferrable step error keeps the item in the queue
// with a later next_run_at and no lease, rather than settling it. It must hold
// before and after the store starts persisting error_code.
func TestDeferItemParksTheItemInsteadOfFailingIt(t *testing.T) {
	item := newDeferredItem(t, "EGRESS_PROBE_FAILED")

	if item.Status != store.ItemQueued {
		t.Fatalf("status = %s, want %s: a deferrable step error must not settle the item",
			item.Status, store.ItemQueued)
	}
	// One attempt was consumed by the claim, so the backoff is backoff(0).
	wantNextRunAt := item.UpdatedAtNs + int64(backoff(0))
	if item.NextRunAtNs != wantNextRunAt {
		t.Errorf("next_run_at_ns = %d (%s after the write), want %d (+%s)",
			item.NextRunAtNs, time.Duration(item.NextRunAtNs-item.UpdatedAtNs), wantNextRunAt, backoff(0))
	}
	if item.LeaseOwner != "" || item.LeaseUntilNs != 0 {
		t.Errorf("lease = %q/%d, want it cleared so another worker can claim the item",
			item.LeaseOwner, item.LeaseUntilNs)
	}
}

// TestDeferItemKeepsTheReasonItWasParked pins the diagnostic half of deferItem:
// runItem:664 hands it the failing step's own code, and the parked row must
// carry it. store.DeferJobItem used to write status, step_index, next_run_at_ns,
// result_json and the lease but never error_code, so an item that reappeared
// later could not say why it had gone away - for the ITEM_TIMEOUT path
// (runItem:650) the timeout itself was invisible.
func TestDeferItemKeepsTheReasonItWasParked(t *testing.T) {
	const stepError = "EGRESS_PROBE_FAILED"
	item := newDeferredItem(t, stepError)

	if item.Status != store.ItemQueued {
		t.Fatalf("status = %s, want %s", item.Status, store.ItemQueued)
	}
	if item.ErrorCode != stepError {
		t.Errorf("error_code = %q after the defer, want %q: the parked item must keep the reason "+
			"runItem passed to deferItem", item.ErrorCode, stepError)
	}
}

// TestDeferItemClearsTheCodeWhenTheParkHasNoReason is not here on purpose: the
// two call sites that park an item with "" (a closed provider gate, a logged
// breakpoint failure) are driven by provider state and a failing breakpoint
// write, not by the step result, so the clearing rule is pinned where it is
// implemented - TestDeferJobItemClearsTheCodeWhenNoReasonIsGiven in
// internal/intel/store/jobs_test.go.
