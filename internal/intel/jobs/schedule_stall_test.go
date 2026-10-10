package jobs

import (
	"context"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// parkedRunner parks every item forever, which is what a job behind a provider
// gate that never reopens looks like to the scheduler.
type parkedRunner struct{}

func (parkedRunner) RunStep(context.Context, Kind, Step, string, string) StepResult {
	return StepResult{DeferUntilNs: 1}
}

func newScheduleManager(t *testing.T, st *store.Store, clock Clock, runner StepRunner, maxRunning int) *Manager {
	t.Helper()
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	return newTestManager(t, st, clock, runner, scope, func() Config {
		return Config{Enabled: true, NodeWorkers: 1, MaxRunningJobs: maxRunning}
	})
}

// createRunningJob creates a job, marks it running and refreshes the scheduler so
// the progress tracker sees it.
func createRunningJob(t *testing.T, m *Manager, st *store.Store, clock *fakeClock, priority int) store.Job {
	t.Helper()
	ctx := context.Background()
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), priority)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	return job
}

// TestManager_StalledJobYieldsItsSlotToQueuedWork covers the scheduling half of
// the 2026-10-02 incident: a running job whose items only ever get parked held
// one of the max_running_jobs slots, and every later job waited behind it.
func TestManager_StalledJobYieldsItsSlotToQueuedWork(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	m := newScheduleManager(t, st, clock, parkedRunner{}, 1)

	stalled := createRunningJob(t, m, st, clock, PriorityManual)
	// One item runs and is parked, so the job is genuinely running with no
	// settled item - the state the incident left jobs in.
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)
	parked, _, _ := st.GetJobItem(ctx, stalled.ID, "node-1")
	if parked.Status != store.ItemQueued {
		t.Fatalf("parked item = %+v, want queued", parked)
	}

	queued, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create queued job: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	ids := m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != stalled.ID {
		t.Fatalf("active = %v, want the still-progressing job to keep its slot", ids)
	}

	// Past jobStallYield the stalled job stops owning the slot, but only because
	// another job actually wants it.
	clock.advance(jobStallYield)
	m.refreshActiveJobs(m.Config())
	ids = m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != queued.ID {
		t.Fatalf("active = %v, want the queued job to take the slot from the stalled one", ids)
	}
	got, _ := st.GetJob(ctx, stalled.ID)
	if got.Status != store.JobRunning {
		t.Fatalf("yielding must be a sort order, not a state change: job = %+v", got)
	}
	claimed, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	if claimed.JobID != queued.ID {
		t.Fatalf("claimed an item of job %s, want the queued job %s to hold the slot", claimed.JobID, queued.ID)
	}
}

// TestManager_StalledJobKeepsRunningWithoutCompetition pins the other half of the
// rule: yielding must not strand a job when nothing else wants the slot.
func TestManager_StalledJobKeepsRunningWithoutCompetition(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	m := newScheduleManager(t, st, clock, parkedRunner{}, 1)

	job := createRunningJob(t, m, st, clock, PriorityManual)
	clock.advance(jobStallTimeout + time.Hour)
	m.refreshActiveJobs(m.Config())

	ids := m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("active = %v, want the only job to keep running", ids)
	}
	if _, ok, _ := m.claimOne(ctx); !ok {
		t.Fatal("a stalled job with no competition must stay claimable")
	}
	settled, _ := st.GetJob(ctx, job.ID)
	if settled.Status != store.JobRunning {
		t.Fatalf("job = %+v, want it still running: nothing was waiting for the slot", settled)
	}
}

// TestManager_AbandonedJobSettlesPartialAndFreesTheSlot covers the terminal half:
// a job that settles nothing for jobStallTimeout while another job waits is
// failed item by item and settles as partial, so its slot is released for good
// and the collected evidence of the items that did run survives.
func TestManager_AbandonedJobSettlesPartialAndFreesTheSlot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	m := newScheduleManager(t, st, clock, parkedRunner{}, 1)

	abandoned := createRunningJob(t, m, st, clock, PriorityManual)
	if _, ok, err := m.claimOne(ctx); err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	// A queued job is what makes the slot wanted.
	waiter, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.advance(jobStallYield)
	m.refreshActiveJobs(m.Config())
	// Still inside jobStallTimeout: the job yields but is not given up on.
	settled, _ := st.GetJob(ctx, abandoned.ID)
	if settled.Status != store.JobRunning {
		t.Fatalf("job = %+v, want it still running before %s", settled, jobStallTimeout)
	}

	clock.advance(jobStallTimeout)
	m.refreshActiveJobs(m.Config())

	settled, _ = st.GetJob(ctx, abandoned.ID)
	if settled.Status != store.JobPartial || settled.Failed != 1 {
		t.Fatalf("job = %+v, want partial with one failed item", settled)
	}
	if !settled.Terminal() {
		t.Fatalf("job = %+v, want a terminal status", settled)
	}
	item, _, _ := st.GetJobItem(ctx, abandoned.ID, "node-1")
	if item.Status != store.ItemFailed || item.ErrorCode != CodeJobTimeout {
		t.Fatalf("item = %+v, want failed with %s", item, CodeJobTimeout)
	}

	// The freed slot goes to the waiting job on the next pass.
	m.refreshActiveJobs(m.Config())
	ids := m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != waiter.ID {
		t.Fatalf("active = %v, want the waiting job to take the freed slot", ids)
	}
}

// TestManager_JobTimeoutItemIsNotRevived pins the guard that keeps a worker from
// writing an item back to life after the sweep settled its job.
func TestManager_JobTimeoutItemIsNotRevived(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	m := newScheduleManager(t, st, clock, parkedRunner{}, 1)

	job := createRunningJob(t, m, st, clock, PriorityManual)
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}

	// The sweep settles the job while the worker still holds the item.
	if _, err := st.FailPendingJobItems(ctx, job.ID, CodeJobTimeout, clock.Now().UnixNano()); err != nil {
		t.Fatalf("FailPendingJobItems: %v", err)
	}
	if _, err := st.RefreshJob(ctx, job.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}
	settled, _ := st.GetJob(ctx, job.ID)
	if settled.Status != store.JobPartial {
		t.Fatalf("job = %+v, want partial", settled)
	}

	m.runItem(ctx, item)

	after, _, _ := st.GetJobItem(ctx, job.ID, "node-1")
	if after.Status != store.ItemFailed || after.ErrorCode != CodeJobTimeout {
		t.Fatalf("item = %+v, want it left as the sweep settled it", after)
	}
	jobAfter, _ := st.GetJob(ctx, job.ID)
	if jobAfter.Status != store.JobPartial || jobAfter.FinishedAtNs != settled.FinishedAtNs {
		t.Fatalf("job = %+v, want it to stay settled", jobAfter)
	}
}

// TestManager_RunningJobThatProgressesKeepsItsSlot guards the yield rule against
// false positives: a job that keeps settling items must never be sorted away.
func TestManager_RunningJobThatProgressesKeepsItsSlot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1", "node-2", "node-3"}, nil
	})
	m := newTestManager(t, st, clock, &scriptedRunner{}, scope, func() Config {
		return Config{Enabled: true, NodeWorkers: 1, MaxRunningJobs: 1}
	})

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

	queued, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create queued job: %v", err)
	}
	// Long enough to pass the yield threshold, but the job settled an item in
	// between, which restarts the stall clock.
	clock.advance(jobStallYield - time.Minute)
	m.refreshActiveJobs(m.Config())
	item, ok, err = m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)

	clock.advance(time.Minute)
	m.refreshActiveJobs(m.Config())
	ids := m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("active = %v, want the progressing job to keep its slot over %s", ids, queued.ID)
	}
}
