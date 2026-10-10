package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// Regression tests for F1 of AUDIT_REPORT.md (fixed): a job settled by the
// timeout sweep must stay settled, and an item the sweep already failed must not
// be written back to life by the worker that was still inside a step.
//
// The defect was the window between the per-step jobGate check and the trailing
// finishItem/deferItem: the loop tail had no guard and FinishJobItem/
// DeferJobItem had no status predicate, so the write revived the item and
// publish -> RefreshJob moved the settled job out of its terminal status. The
// fix is two-layered: the three store writes require status IN
// ('queued','running') and return store.ErrJobItemSettled otherwise, and
// finishItem/deferItem return silently (without publishing) on that sentinel.
//
// auditRunner runs a callback in the middle of the pipeline: it lets a test act
// (let the timeout sweep settle the job) between two steps, which is the window
// the runItem guard has to cover.
type auditRunner struct {
	onStep  Step
	between func()
	inner   StepRunner
}

func (r *auditRunner) RunStep(ctx context.Context, kind Kind, step Step, jobID, nodeHash string) StepResult {
	if step == r.onStep && r.between != nil {
		r.between()
		r.between = nil
	}
	return r.inner.RunStep(ctx, kind, step, jobID, nodeHash)
}

// TestAudit_SettledJobIsNotRevivedByTheLastStep pins the window between the
// per-step jobGate check and the trailing finishItem: the sweep fails the item
// and publishes (settling the job as partial) while its item is inside the LAST
// step, then the loop exits and calls finishItem unconditionally. Before the fix
// FinishJobItem's UPDATE had no status predicate, so the item went terminal
// again (done, error_code cleared) and publish -> RefreshJob recomputed the job
// from its items, moving it from partial to succeeded - a timeout reported as a
// success. The job must stay partial and the item must keep the sweep's verdict.
func TestAudit_SettledJobIsNotRevivedByTheLastStep(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	// KindEgress has two steps (see the step plans), so a runner that fires on
	// the last step lets the sweep land after the gate check that preceded it.
	inner := &scriptedRunner{}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	job, err := newTestManager(t, st, clock, inner, scope, nil).Create(ctx,
		Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}

	steps := PendingSteps(KindEgress, -1)
	if len(steps) == 0 {
		t.Fatal("KindEgress has no steps")
	}
	last := steps[len(steps)-1]

	// The sweep action, exactly what settleAbandonedJobs does: fail every
	// pending item, then publish (RefreshJob) so the job settles as partial.
	settled := false
	runner := &auditRunner{onStep: last, inner: inner, between: func() {
		if _, err := st.FailPendingJobItems(ctx, job.ID, CodeJobTimeout, clock.Now().UnixNano()); err != nil {
			t.Fatalf("FailPendingJobItems: %v", err)
		}
		if _, err := st.RefreshJob(ctx, job.ID, clock.Now().UnixNano()); err != nil {
			t.Fatalf("RefreshJob: %v", err)
		}
		settled = true
	}}
	m := newTestManager(t, st, clock, runner, scope, nil)

	// claimOne only claims inside the active-job set, so the scheduler has to
	// have seen the job once - exactly as the worker loop does each tick.
	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)
	if !settled {
		t.Fatal("the probe never reached the last step, so the window was not exercised")
	}

	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	gotItem, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}

	t.Logf("job status after the settled item finished: %s (finished_at=%d)", after.Status, after.FinishedAtNs)
	t.Logf("item status after the settled item finished: %s (error=%s)", gotItem.Status, gotItem.ErrorCode)

	if after.Status != store.JobPartial {
		t.Errorf("job = %s, want %s: a settled job was moved out of its terminal state", after.Status, store.JobPartial)
	}
	if gotItem.Status != store.ItemFailed {
		t.Errorf("item = %s, want %s: the settled item was rewritten by the trailing finishItem", gotItem.Status, store.ItemFailed)
	}
	if gotItem.ErrorCode != CodeJobTimeout {
		t.Errorf("item error = %q, want %q", gotItem.ErrorCode, CodeJobTimeout)
	}
}

// TestAudit_SettledJobIsNotReopenedByALateDefer is the worse variant of the same
// window: the step that was running while the sweep settled the job returns a
// deferrable result (a provider gate, an item timeout, a transient error), so
// the trailing deferItem put the item back to queued and publish recomputed the
// job. Before the fix the item became a queued item of a terminal job - stranded
// forever, because ListActiveJobs only claims items of queued/running jobs and
// RetryFailedJobItems only requeues failed items. The item must stay failed.
func TestAudit_SettledJobIsNotReopenedByALateDefer(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	steps := PendingSteps(KindEgress, -1)
	last := steps[len(steps)-1]

	// The last step returns a parked result, which is the deferral path.
	inner := &scriptedRunner{handlers: map[Step]StepResult{
		last: {DeferUntilNs: clock.Now().UnixNano() + int64(30*time.Second)},
	}}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	job, err := newTestManager(t, st, clock, inner, scope, nil).Create(ctx,
		Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}

	settled := false
	runner := &auditRunner{onStep: last, inner: inner, between: func() {
		if _, err := st.FailPendingJobItems(ctx, job.ID, CodeJobTimeout, clock.Now().UnixNano()); err != nil {
			t.Fatalf("FailPendingJobItems: %v", err)
		}
		// The sweep settles the job the way settleAbandonedJobs does.
		if _, err := st.RefreshJob(ctx, job.ID, clock.Now().UnixNano()); err != nil {
			t.Fatalf("RefreshJob: %v", err)
		}
		settled = true
	}}
	m := newTestManager(t, st, clock, runner, scope, nil)
	m.refreshActiveJobs(m.Config())

	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)
	if !settled {
		t.Fatal("the probe never reached the last step, so the window was not exercised")
	}

	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	gotItem, _, err := st.GetJobItem(ctx, job.ID, item.NodeHash)
	if err != nil {
		t.Fatalf("GetJobItem: %v", err)
	}
	t.Logf("job status after the late defer: %s (finished_at=%d)", after.Status, after.FinishedAtNs)
	t.Logf("item status after the late defer: %s (error=%s)", gotItem.Status, gotItem.ErrorCode)

	if gotItem.Status != store.ItemFailed {
		t.Errorf("item = %s, want %s: a late defer reopened a settled item", gotItem.Status, store.ItemFailed)
	}
	if !after.Terminal() {
		t.Errorf("job = %s, want a terminal status: a late defer reopened a settled job", after.Status)
	}
}

// TestAudit_SettledJobDoesNotReturnToTheActiveSet is the consequence check: after
// the sweep settles a job, refreshActiveJobs must not list it as active again.
// The sweep itself is reproduced here with the same two store calls it makes.
func TestAudit_SettledJobDoesNotReturnToTheActiveSet(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	inner := &scriptedRunner{}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	cfg := func() Config { return Config{Enabled: true, NodeWorkers: 1, MaxRunningJobs: 1} }

	m := newTestManager(t, st, clock, inner, scope, cfg)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}

	steps := PendingSteps(KindEgress, -1)
	last := steps[len(steps)-1]
	runner := &auditRunner{onStep: last, inner: inner, between: func() {
		if _, err := st.FailPendingJobItems(ctx, job.ID, CodeJobTimeout, clock.Now().UnixNano()); err != nil {
			t.Fatalf("FailPendingJobItems: %v", err)
		}
		// settleAbandonedJobs publishes right after the sweep, which is what
		// settles the job as partial.
		if _, err := st.RefreshJob(ctx, job.ID, clock.Now().UnixNano()); err != nil {
			t.Fatalf("RefreshJob: %v", err)
		}
	}}
	m.runner = runner

	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)

	// The sweep's own publish settles the job; the worker's publish follows. Now
	// the scheduler must agree that the job is done.
	m.refreshActiveJobs(m.Config())
	for _, id := range m.currentActiveJobIDs() {
		if id == job.ID {
			t.Errorf("settled job %s is back in the active set: it would hold a max_running_jobs slot again", job.ID)
		}
	}
	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !after.Terminal() {
		t.Errorf("job = %s, want a terminal status", after.Status)
	}
}
