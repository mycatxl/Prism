package jobs

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// Regression tests for F4/F5/F6 of AUDIT_REPORT.md (all fixed).
//
// F4: CancelJob (repo_jobs.go) ran "UPDATE jobs SET status = 'canceled' WHERE
// id = ?" with no status guard, so a cancel rewrote a job that had already
// reached a terminal status. The API exposes cancel on any job id, so a double
// click or a retried request turned a succeeded job into canceled and rewrote
// what the operator sees. The fix adds status IN ('queued','running') and
// reports store.ErrJobNotActive (mapped to CONFLICT / 409) when the row was
// already terminal - Cancel is no longer a silent no-op there.
//
// F5: RetryFailedJobItems reset finished_at_ns for every job in
// ('partial','failed','canceled') whether or not it had a failed item, so
// "retry failures" on a job with nothing to retry reopened it. The fix only
// reopens when retried > 0 and narrows the status list to ('partial','failed').
//
// F6: runOnce consumed the provider budget before it claimed the queue, so an
// idle poll burned one unit of the daily quota and one QPS interval on no work
// (used=3 after three empty rounds). The fix claims first, consumes only when
// rows were claimed, and hands the rows back through
// store.ReleaseProviderItems when the gate it then hits is shut. The tests below
// are the regression tests for that fix.

// TestAudit_CanceledJobIsNotOverwrittenByALaterCancel pins the missing status
// predicate: cancelling a job that already succeeded must leave it succeeded.
// With the fix the store refuses the write (ErrJobNotActive), and the job row
// keeps both its status and its finished_at_ns.
func TestAudit_CanceledJobIsNotOverwrittenByALaterCancel(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	m := newTestManager(t, st, clock, &scriptedRunner{}, nil, nil)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Drive the job to a successful terminal state.
	drain(t, m, clock, 4)
	settled, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if settled.Status != store.JobSucceeded {
		t.Fatalf("job = %s, want %s before the regression check", settled.Status, store.JobSucceeded)
	}
	finishedAt := settled.FinishedAtNs

	// The store boundary is the authority: it must refuse the write.
	if err := st.CancelJob(ctx, job.ID); !errors.Is(err, store.ErrJobNotActive) {
		t.Fatalf("CancelJob on a succeeded job = %v, want ErrJobNotActive", err)
	}
	// The manager path must surface the same refusal instead of swallowing it.
	if err := m.Cancel(ctx, job.ID); !errors.Is(err, store.ErrJobNotActive) {
		t.Fatalf("Cancel on a succeeded job = %v, want ErrJobNotActive", err)
	}

	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	t.Logf("job after cancelling a succeeded job: %s (finished_at %d -> %d)",
		after.Status, finishedAt, after.FinishedAtNs)
	if after.Status != store.JobSucceeded {
		t.Errorf("job = %s, want %s: a terminal job was rewritten by a later cancel",
			after.Status, store.JobSucceeded)
	}
	if after.FinishedAtNs != finishedAt {
		t.Errorf("finished_at = %d, want %d unchanged", after.FinishedAtNs, finishedAt)
	}

	// Cancelling an active job still works, and an unknown id is still NOT_FOUND.
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"node-1"}, nil
	})
	activeManager := newTestManager(t, st, clock, &scriptedRunner{}, scope, nil)
	active, err := activeManager.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.CancelJob(ctx, active.ID); err != nil {
		t.Fatalf("CancelJob on an active job: %v", err)
	}
	canceled, _ := st.GetJob(ctx, active.ID)
	if canceled.Status != store.JobCanceled {
		t.Errorf("active job = %s, want %s", canceled.Status, store.JobCanceled)
	}
	if err := st.CancelJob(ctx, "missing"); !errors.Is(err, store.ErrJobNotFound) {
		t.Errorf("CancelJob(missing) = %v, want ErrJobNotFound", err)
	}
}

// TestAudit_RetryFailedKeepsJobFinishedAt pins the finished_at_ns reset of
// RetryFailedJobItems: a succeeded job has no failed item, so retrying must be a
// no-op. Before the fix the second UPDATE still ran and reopened the job.
func TestAudit_RetryFailedKeepsJobFinishedAt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	m := newTestManager(t, st, clock, &scriptedRunner{}, nil, nil)
	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	drain(t, m, clock, 4)

	settled, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	finishedAt := settled.FinishedAtNs
	if settled.Status != store.JobSucceeded || finishedAt == 0 {
		t.Fatalf("job = %+v, want a settled succeeded job", settled)
	}

	// A succeeded job has no failed item, so the retry must touch nothing. The
	// store call is checked on its own because RetryFailed publishes afterwards
	// and RefreshJob re-stamps finished_at_ns on every publish of a settled job
	// (pre-existing behaviour, unrelated to this defect).
	retried, err := st.RetryFailedJobItems(ctx, job.ID, clock.Now().UnixNano())
	if err != nil {
		t.Fatalf("RetryFailedJobItems: %v", err)
	}
	if retried != 0 {
		t.Fatalf("retried = %d, want 0: the job had no failed item", retried)
	}
	after, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	t.Logf("retried=%d job=%s finished_at=%d (was %d)", retried, after.Status, after.FinishedAtNs, finishedAt)
	if after.Status != store.JobSucceeded {
		t.Errorf("job = %s, want %s: retry-failed reopened a job with no failed item",
			after.Status, store.JobSucceeded)
	}
	if after.FinishedAtNs != finishedAt {
		t.Errorf("finished_at = %d, want %d: retry-failed reset the settlement time",
			after.FinishedAtNs, finishedAt)
	}

	// The manager path adds publish/RefreshJob on top; the job must still be the
	// same succeeded job, with a non-zero settlement time.
	if retried, err := m.RetryFailed(ctx, job.ID); err != nil || retried != 0 {
		t.Fatalf("RetryFailed = %d (err %v), want 0 and no error", retried, err)
	}
	published, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if published.Status != store.JobSucceeded || published.FinishedAtNs == 0 {
		t.Errorf("job after publish = %+v, want succeeded with a settlement time", published)
	}
}

// TestAudit_ProviderBudgetIsNotConsumedWithoutWork is the regression test for
// F6 of AUDIT_REPORT.md (fixed): runOnce ordered ConsumeProviderBudget before
// ClaimProviderItems, so when the queue was empty the consumed slot was lost and
// an idle provider kept spending its daily budget with its QPS spacing applied
// to no work. The worker now claims first and only consumes the budget when the
// claim returned rows.
func TestAudit_ProviderBudgetIsNotConsumedWithoutWork(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	lookups := 0
	pool := newProviderPool(
		newTestManager(t, st, clock, &scriptedRunner{}, nil, nil),
		func() []ProviderQueueSpec {
			return []ProviderQueueSpec{{
				ProviderID: "ipapi_is",
				Enabled:    true,
				DailyLimit: 5,
				QPS:        1,
				BatchSize:  10,
			}}
		},
		OnlineLookupFunc(func(context.Context, string, []netip.Addr) ProviderOutcome {
			lookups++
			return ProviderOutcome{}
		}),
		nil,
	)

	// No queue row exists, so every round has nothing to do. The clock advances
	// one second per round, which is exactly the providerIdleSleep the worker
	// loop uses after an empty claim.
	for i := 0; i < 3; i++ {
		pool.runOnce("ipapi_is", ProviderQueueSpec{ProviderID: "ipapi_is", Enabled: true, DailyLimit: 5, QPS: 1, BatchSize: 10})
		clock.advance(time.Second)
	}
	state, ok, err := st.GetProviderState(ctx, "ipapi_is")
	if err != nil {
		t.Fatalf("GetProviderState: %v", err)
	}
	t.Logf("provider state after 3 empty rounds: found=%v used=%d next_request_at=%d lookups=%d",
		ok, state.Used, state.NextRequestAtNs, lookups)
	if state.Used != 0 {
		t.Errorf("used = %d, want 0: three empty rounds consumed daily budget without resolving any IP",
			state.Used)
	}
}

// TestAudit_ProviderBudgetGateRollsBackTheClaim covers the rollback path the F6
// fix introduced, which is its real risk: claiming before consuming the budget
// means the worker can lease rows and only then find the gate shut. Nothing was
// sent, so nothing may be spent and nothing may be lost - the rows must return
// to queued with their lease cleared, no lookup may happen, the day counter must
// not move, and once the gate opens the same rows must be claimed again.
func TestAudit_ProviderBudgetGateRollsBackTheClaim(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC))

	now := clock.Now().UnixNano()
	ips := []string{"198.51.100.1", "198.51.100.2"}
	for i, ip := range ips {
		if _, err := st.EnqueueProviderItem(ctx, store.QueueItem{
			Provider: "ipapi_is", IP: ip, Priority: 1, NextRunAtNs: now, EnqueuedAtNs: now + int64(i),
		}, store.EnqueueIfMissing); err != nil {
			t.Fatalf("EnqueueProviderItem: %v", err)
		}
	}

	// Close the gate the way a spent day does: the single unit of the day is
	// already used, so the consume of the round fails with ErrBudgetExhausted.
	seeded, err := st.ConsumeProviderBudget(ctx, store.BudgetRequest{
		Provider: "ipapi_is", Day: store.DayString(clock.Now()), DailyLimit: 1, QPS: 1, NowNs: now,
	})
	if err != nil || seeded.Used != 1 {
		t.Fatalf("seed consume = %+v (err %v), want used=1", seeded, err)
	}

	lookups := 0
	spec := ProviderQueueSpec{ProviderID: "ipapi_is", Enabled: true, DailyLimit: 1, QPS: 1, BatchSize: 10}
	pool := newProviderPool(
		newTestManager(t, st, clock, &scriptedRunner{}, nil, nil),
		func() []ProviderQueueSpec { return []ProviderQueueSpec{spec} },
		OnlineLookupFunc(func(context.Context, string, []netip.Addr) ProviderOutcome {
			lookups++
			return ProviderOutcome{Reported: 1}
		}),
		nil,
	)

	wait := pool.runOnce("ipapi_is", spec)
	t.Logf("closed gate: wait=%s lookups=%d", wait, lookups)
	if lookups != 0 {
		t.Errorf("lookups = %d, want 0: a shut gate must not reach the vendor", lookups)
	}
	if wait <= 0 || wait > providerSleepMax {
		t.Errorf("wait = %s, want a bounded positive sleep", wait)
	}

	// The claimed rows are back in the queue with their lease cleared.
	for _, ip := range ips {
		item, ok := queueItem(t, st, "ipapi_is", ip)
		if !ok {
			t.Fatalf("queue row %s disappeared", ip)
		}
		if item.Status != store.QueueQueued {
			t.Errorf("row %s status = %q, want %q: the claim was not rolled back",
				ip, item.Status, store.QueueQueued)
		}
		if item.LeaseOwner != "" || item.LeaseUntilNs != 0 {
			t.Errorf("row %s kept its lease: %+v", ip, item)
		}
		if item.Attempts != 0 {
			t.Errorf("row %s attempts = %d, want 0: a shut gate is not a failure", ip, item.Attempts)
		}
	}

	// The gate was shut, so the day counter must not have moved.
	after, _, err := st.GetProviderState(ctx, "ipapi_is")
	if err != nil {
		t.Fatalf("GetProviderState: %v", err)
	}
	if after.Used != 1 {
		t.Errorf("used = %d, want 1 unchanged: a shut gate consumed budget for work it never did", after.Used)
	}

	// The next UTC day opens the gate and the very same rows are claimed again.
	clock.advance(24 * time.Hour)
	if wait := pool.runOnce("ipapi_is", spec); wait <= 0 {
		t.Fatalf("runOnce after the gate opened = %s", wait)
	}
	if lookups != 1 {
		t.Errorf("lookups = %d, want 1: the released rows must be claimable again", lookups)
	}
	counts, err := st.ProviderQueueCounts(ctx, "ipapi_is")
	if err != nil {
		t.Fatalf("ProviderQueueCounts: %v", err)
	}
	if counts.Done != 2 || counts.Queued != 0 || counts.Running != 0 {
		t.Errorf("counts = %+v, want both rows resolved on the retry", counts)
	}
}
