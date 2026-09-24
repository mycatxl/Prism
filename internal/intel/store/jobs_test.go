package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProviderBudget_DayRolloverAndLimits(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	day1 := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)

	req := BudgetRequest{Provider: "proxycheck", Day: DayString(day1), DailyLimit: 2, QPS: 1, NowNs: day1.UnixNano()}
	state, err := st.ConsumeProviderBudget(ctx, req)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if state.Used != 1 {
		t.Fatalf("used = %d, want 1", state.Used)
	}
	if state.NextRequestAtNs != day1.UnixNano()+int64(time.Second) {
		t.Fatalf("next_request_at = %d, want now+1s", state.NextRequestAtNs)
	}

	// The QPS gate is closed until next_request_at_ns.
	req.NowNs = day1.UnixNano() + int64(time.Second) - 1
	state, err = st.ConsumeProviderBudget(ctx, req)
	if !errors.Is(err, ErrProviderNotReady) {
		t.Fatalf("err = %v, want ErrProviderNotReady", err)
	}
	if state.NextRequestAtNs == 0 {
		t.Fatal("blocked consume must return the scheduling state")
	}

	// Second request after the gate opens.
	req.NowNs = day1.UnixNano() + int64(2*time.Second)
	if _, err := st.ConsumeProviderBudget(ctx, req); err != nil {
		t.Fatalf("second consume: %v", err)
	}
	// Daily limit reached.
	req.NowNs = day1.UnixNano() + int64(3*time.Second)
	if _, err := st.ConsumeProviderBudget(ctx, req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}

	// Next UTC day resets the counter (injected clock, no sleeping).
	req.Day = DayString(day2)
	req.NowNs = day2.UnixNano()
	state, err = st.ConsumeProviderBudget(ctx, req)
	if err != nil {
		t.Fatalf("next-day consume: %v", err)
	}
	if state.Used != 1 || state.Day != DayString(day2) {
		t.Fatalf("state = %+v, want used=1 on %s", state, DayString(day2))
	}
}

func TestProviderBudget_BlockedPausedAndCredentialRotation(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	req := BudgetRequest{Provider: "abuseipdb", Day: DayString(now), NowNs: now.UnixNano(), CredentialID: "key-1"}

	if _, err := st.ConsumeProviderBudget(ctx, req); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := st.MarkProviderBlocked(ctx, "abuseipdb", now.Add(time.Hour).UnixNano(), "PROVIDER_LIMIT"); err != nil {
		t.Fatalf("MarkProviderBlocked: %v", err)
	}
	state, err := st.ConsumeProviderBudget(ctx, req)
	if !errors.Is(err, ErrProviderBlocked) {
		t.Fatalf("err = %v, want ErrProviderBlocked", err)
	}
	if state.ErrorCode != "PROVIDER_LIMIT" {
		t.Fatalf("error_code = %q", state.ErrorCode)
	}

	// 401 => paused.
	if err := st.MarkProviderPaused(ctx, "abuseipdb", "PROVIDER_AUTH", "key-1"); err != nil {
		t.Fatalf("MarkProviderPaused: %v", err)
	}
	req.NowNs = now.Add(2 * time.Hour).UnixNano()
	if _, err := st.ConsumeProviderBudget(ctx, req); !errors.Is(err, ErrProviderPaused) {
		t.Fatalf("err = %v, want ErrProviderPaused", err)
	}

	// A new key lifts the pause automatically.
	req.CredentialID = "key-2"
	if _, err := st.ConsumeProviderBudget(ctx, req); err != nil {
		t.Fatalf("consume after key rotation: %v", err)
	}
	saved, ok, err := st.GetProviderState(ctx, "abuseipdb")
	if err != nil || !ok {
		t.Fatalf("GetProviderState: ok=%v err=%v", ok, err)
	}
	if saved.Paused || saved.CredentialID != "key-2" {
		t.Fatalf("state = %+v, want unpaused with key-2", saved)
	}

	// The explicit resume action also clears a pause.
	if err := st.MarkProviderPaused(ctx, "abuseipdb", "PROVIDER_AUTH", "key-2"); err != nil {
		t.Fatalf("MarkProviderPaused: %v", err)
	}
	if err := st.ResumeProvider(ctx, "abuseipdb"); err != nil {
		t.Fatalf("ResumeProvider: %v", err)
	}
	saved, _, _ = st.GetProviderState(ctx, "abuseipdb")
	if saved.Paused || saved.ErrorCode != "" || saved.BlockedUntilNs != 0 {
		t.Fatalf("state after resume = %+v", saved)
	}

	states, err := st.ListProviderStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("ListProviderStates = %d (err %v)", len(states), err)
	}
}

func TestProviderQueue_EnqueueModesAndClaims(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	inserted, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "proxycheck", IP: "198.51.100.1", Priority: 10, JobID: "job-1",
		NextRunAtNs: 100, EnqueuedAtNs: 100,
	}, EnqueueIfMissing)
	if err != nil || !inserted {
		t.Fatalf("enqueue = %v (err %v)", inserted, err)
	}

	// A second enqueue for the same (provider, ip) is de-duplicated but raises
	// the priority.
	inserted, err = st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "proxycheck", IP: "198.51.100.1", Priority: 50, JobID: "job-2",
		NextRunAtNs: 100, EnqueuedAtNs: 100,
	}, EnqueueIfMissing)
	if err != nil || inserted {
		t.Fatalf("second enqueue = %v (err %v)", inserted, err)
	}
	items, err := st.ClaimProviderItems(ctx, "proxycheck", 100, 200, 10, "worker-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("claim = %d (err %v)", len(items), err)
	}
	if items[0].Priority != 50 || items[0].JobID != "job-2" || items[0].Status != QueueRunning {
		t.Fatalf("item = %+v", items[0])
	}
	if items[0].LeaseOwner != "worker-1" || items[0].LeaseUntilNs != 200 {
		t.Fatalf("lease = %+v", items[0])
	}

	// A running item with a live lease is not claimable again.
	again, err := st.ClaimProviderItems(ctx, "proxycheck", 150, 300, 10, "worker-2")
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim = %d (err %v)", len(again), err)
	}

	// After resolution the row is done and EnqueueRefresh can reopen it.
	if err := st.ResolveProviderItem(ctx, "proxycheck", "198.51.100.1"); err != nil {
		t.Fatalf("ResolveProviderItem: %v", err)
	}
	counts, err := st.ProviderQueueCounts(ctx, "proxycheck")
	if err != nil || counts.Done != 1 {
		t.Fatalf("counts = %+v (err %v)", counts, err)
	}
	if inserted, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "proxycheck", IP: "198.51.100.1", Priority: 10, JobID: "job-3",
		NextRunAtNs: 400, EnqueuedAtNs: 400,
	}, EnqueueRefresh); err != nil || inserted {
		t.Fatalf("refresh enqueue = %v (err %v)", inserted, err)
	}
	items, err = st.ClaimProviderItems(ctx, "proxycheck", 400, 500, 10, "worker-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("post-refresh claim = %d (err %v)", len(items), err)
	}
	if items[0].Attempts != 0 || items[0].ErrorCode != "" {
		t.Fatalf("refreshed item = %+v", items[0])
	}
}

func TestProviderQueue_PriorityOrderAndExpiredLeaseReclaim(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	for i, priority := range []int{1, 9, 5} {
		if _, err := st.EnqueueProviderItem(ctx, QueueItem{
			Provider: "ipqs", IP: "198.51.100." + string(rune('1'+i)), Priority: priority,
			NextRunAtNs: 10, EnqueuedAtNs: int64(10 + i),
		}, EnqueueIfMissing); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	// Lower the first row's enqueue time so the order assertion is about priority.
	items, err := st.ClaimProviderItems(ctx, "ipqs", 10, 20, 3, "w1")
	if err != nil || len(items) != 3 {
		t.Fatalf("claim = %d (err %v)", len(items), err)
	}
	if items[0].Priority != 9 || items[1].Priority != 5 || items[2].Priority != 1 {
		t.Fatalf("claim order = %d,%d,%d", items[0].Priority, items[1].Priority, items[2].Priority)
	}

	// A crashed worker's expired lease becomes claimable again.
	reclaimed, err := st.ClaimProviderItems(ctx, "ipqs", 25, 40, 3, "w2")
	if err != nil || len(reclaimed) != 3 {
		t.Fatalf("reclaim = %d (err %v)", len(reclaimed), err)
	}
	if reclaimed[0].LeaseOwner != "w2" {
		t.Fatalf("lease owner = %q", reclaimed[0].LeaseOwner)
	}
}

func TestProviderQueue_BackoffAndFailureState(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if _, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "ipqs", IP: "198.51.100.9", Priority: 1, NextRunAtNs: 0, EnqueuedAtNs: 0,
	}, EnqueueIfMissing); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Exponential backoff of §3.3: 30s * 2^attempts capped at six hours.
	backoff := computeBackoff(2)
	if backoff != 120*time.Second {
		t.Fatalf("backoff(2) = %s, want 2m", backoff)
	}
	if computeBackoff(20) != 6*time.Hour {
		t.Fatalf("backoff(20) = %s, want 6h", computeBackoff(20))
	}

	if err := st.FailProviderItem(ctx, "ipqs", "198.51.100.9", 2, 500, "PROVIDER_UNAVAILABLE", false); err != nil {
		t.Fatalf("FailProviderItem: %v", err)
	}
	counts, _ := st.ProviderQueueCounts(ctx, "ipqs")
	if counts.Queued != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v", counts)
	}
	// Not claimable before next_run_at_ns.
	items, err := st.ClaimProviderItems(ctx, "ipqs", 499, 600, 5, "w")
	if err != nil || len(items) != 0 {
		t.Fatalf("early claim = %d (err %v)", len(items), err)
	}
	items, err = st.ClaimProviderItems(ctx, "ipqs", 500, 600, 5, "w")
	if err != nil || len(items) != 1 || items[0].Attempts != 2 {
		t.Fatalf("claim = %+v (err %v)", items, err)
	}

	// Five failed attempts mark the row terminally failed.
	if err := st.FailProviderItem(ctx, "ipqs", "198.51.100.9", 5, 0, "PROVIDER_UNAVAILABLE", true); err != nil {
		t.Fatalf("FailProviderItem terminal: %v", err)
	}
	counts, _ = st.ProviderQueueCounts(ctx, "ipqs")
	if counts.Failed != 1 {
		t.Fatalf("counts = %+v, want one failed row", counts)
	}

	// Requeue without counting an attempt (budget gate closed).
	if err := st.RequeueProviderItem(ctx, "ipqs", "198.51.100.9", 999, "BUDGET"); err != nil {
		t.Fatalf("RequeueProviderItem: %v", err)
	}
	counts, _ = st.ProviderQueueCounts(ctx, "ipqs")
	if counts.Queued != 1 || counts.Failed != 0 {
		t.Fatalf("counts after requeue = %+v", counts)
	}
}

// computeBackoff mirrors jobs.providerBackoff so the store test documents the
// exponential schedule independently of the worker package.
func computeBackoff(attempts int) time.Duration {
	delay := 30 * time.Second
	for i := 0; i < attempts; i++ {
		delay *= 2
		if delay >= 6*time.Hour {
			return 6 * time.Hour
		}
	}
	return delay
}

func TestProviderQueue_ResetStaleAndCleanup(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	for i, status := range []string{QueueQueued, QueueDone, QueueFailed} {
		ip := "198.51.100." + string(rune('1'+i))
		if _, err := st.EnqueueProviderItem(ctx, QueueItem{
			Provider: "proxycheck", IP: ip, Priority: 1, NextRunAtNs: int64(100 + i), EnqueuedAtNs: 1,
		}, EnqueueIfMissing); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`UPDATE provider_queue SET status = ? WHERE provider = 'proxycheck' AND ip = ?`, status, ip); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	if _, err := st.ClaimProviderItems(ctx, "proxycheck", 100, 200, 5, "w"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	reset, err := st.ResetStaleProviderItems(ctx)
	if err != nil {
		t.Fatalf("ResetStaleProviderItems: %v", err)
	}
	if reset != 1 {
		t.Fatalf("reset = %d, want 1", reset)
	}

	deleted, err := st.CleanupProviderQueue(ctx, 200, 0)
	if err != nil {
		t.Fatalf("CleanupProviderQueue: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
}

func TestJobs_CreateClaimAndPipelineBreakpoint(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	items := []JobItem{
		{NodeHash: "n1", NextRunAtNs: 10, UpdatedAtNs: 10},
		{NodeHash: "n2", NextRunAtNs: 10, UpdatedAtNs: 10},
	}
	if err := st.CreateJob(ctx, Job{
		ID: "job", Kind: "intel", Priority: 100, RequestJSON: `{"kind":"intel"}`,
		CreatedBy: "admin", CreatedAtNs: 5,
	}, items); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	job, err := st.GetJob(ctx, "job")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != JobQueued || job.Total != 2 {
		t.Fatalf("job = %+v", job)
	}
	if _, err := st.GetJob(ctx, "missing"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("err = %v, want ErrJobNotFound", err)
	}

	claimed, err := st.ClaimJobItems(ctx, ClaimOptions{NowNs: 10, LeaseUntilNs: 100, Limit: 10, Owner: "w1"})
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim = %d (err %v)", len(claimed), err)
	}
	if claimed[0].Status != ItemRunning || claimed[0].Attempts != 1 {
		t.Fatalf("claimed = %+v", claimed[0])
	}
	job, _ = st.GetJob(ctx, "job")
	if job.Status != JobRunning || job.StartedAtNs != 10 {
		t.Fatalf("job after claim = %+v", job)
	}

	// Breakpoint: store step 3 and requeue for a later restart.
	if err := st.DeferJobItem(ctx, "job", "n1", 3, 500, `{"steps":[1,2,3]}`, 11); err != nil {
		t.Fatalf("DeferJobItem: %v", err)
	}
	item, ok, err := st.GetJobItem(ctx, "job", "n1")
	if err != nil || !ok {
		t.Fatalf("GetJobItem: ok=%v err=%v", ok, err)
	}
	if item.Status != ItemQueued || item.StepIndex != 3 || item.NextRunAtNs != 500 {
		t.Fatalf("item = %+v", item)
	}

	// The resumed worker claims only the deferred item after its next_run_at.
	claimed, err = st.ClaimJobItems(ctx, ClaimOptions{NowNs: 499, LeaseUntilNs: 600, Limit: 10, Owner: "w1"})
	if err != nil || len(claimed) != 1 || claimed[0].NodeHash != "n2" {
		t.Fatalf("claim before deferral = %+v (err %v)", claimed, err)
	}
	if err := st.SaveJobItemStep(ctx, "job", "n2", 5, `{"steps":[1,2,3,4,5]}`, 700, 12); err != nil {
		t.Fatalf("SaveJobItemStep: %v", err)
	}
	item, _, _ = st.GetJobItem(ctx, "job", "n2")
	if item.StepIndex != 5 || item.Status != ItemRunning {
		t.Fatalf("item n2 = %+v", item)
	}

	claimed, err = st.ClaimJobItems(ctx, ClaimOptions{NowNs: 500, LeaseUntilNs: 600, Limit: 10, Owner: "w2"})
	if err != nil || len(claimed) != 1 || claimed[0].NodeHash != "n1" || claimed[0].StepIndex != 3 {
		t.Fatalf("resumed claim = %+v (err %v)", claimed, err)
	}

	if err := st.FinishJobItem(ctx, "job", "n1", ItemDone, "", `{}`, 20); err != nil {
		t.Fatalf("FinishJobItem: %v", err)
	}
	if err := st.FinishJobItem(ctx, "job", "n2", ItemSkipped, "", `{}`, 20); err != nil {
		t.Fatalf("FinishJobItem: %v", err)
	}
	settled, err := st.RefreshJob(ctx, "job", 30)
	if err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}
	if settled.Status != JobSucceeded || settled.Done != 1 || settled.Skipped != 1 || settled.FinishedAtNs != 30 {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestJobs_CompletionPartialCancelAndRetry(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, Job{ID: "j", Kind: "full", Priority: 100, CreatedAtNs: 1},
		[]JobItem{{NodeHash: "a", NextRunAtNs: 1, UpdatedAtNs: 1}, {NodeHash: "b", NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 5, Owner: "w"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.FinishJobItem(ctx, "j", "a", ItemFailed, "STEP_TIMEOUT", `{}`, 5); err != nil {
		t.Fatalf("FinishJobItem: %v", err)
	}
	if err := st.FinishJobItem(ctx, "j", "b", ItemDone, "", `{}`, 5); err != nil {
		t.Fatalf("FinishJobItem: %v", err)
	}
	job, err := st.RefreshJob(ctx, "j", 6)
	if err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}
	if job.Status != JobPartial || job.Failed != 1 {
		t.Fatalf("job = %+v, want partial with 1 failure", job)
	}

	progress, err := st.JobProgressView(ctx, "j")
	if err != nil {
		t.Fatalf("JobProgressView: %v", err)
	}
	if progress.Total != 2 || progress.Done != 1 || progress.Failed != 1 || progress.Pending != 0 {
		t.Fatalf("progress = %+v", progress)
	}

	retried, err := st.RetryFailedJobItems(ctx, "j", 100)
	if err != nil || retried != 1 {
		t.Fatalf("RetryFailedJobItems = %d (err %v)", retried, err)
	}
	job, _ = st.GetJob(ctx, "j")
	if job.Status != JobQueued || job.FinishedAtNs != 0 {
		t.Fatalf("job after retry = %+v", job)
	}

	if err := st.CancelJob(ctx, "j"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	job, _ = st.RefreshJob(ctx, "j", 200)
	if job.Status != JobCanceled {
		t.Fatalf("job after cancel = %+v", job)
	}
	counters, err := st.JobItemCounters(ctx, "j")
	if err != nil || counters.Canceled != 1 {
		t.Fatalf("counters = %+v (err %v), want one canceled item", counters, err)
	}
	if err := st.CancelJob(ctx, "nope"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("err = %v, want ErrJobNotFound", err)
	}
}

func TestJobs_ResetRunningItemsAndPendingLookups(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, Job{ID: "j", Kind: "intel", Priority: 10, CreatedAtNs: 1},
		[]JobItem{{NodeHash: "a", NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 5, Owner: "w"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	reset, err := st.ResetRunningJobItems(ctx)
	if err != nil || reset != 1 {
		t.Fatalf("ResetRunningJobItems = %d (err %v)", reset, err)
	}
	item, _, _ := st.GetJobItem(ctx, "j", "a")
	if item.Status != ItemQueued || item.LeaseOwner != "" || item.LeaseUntilNs != 0 {
		t.Fatalf("item after reset = %+v", item)
	}
	job, _ := st.GetJob(ctx, "j")
	if job.Status != JobRunning {
		t.Fatalf("running jobs must stay running after a restart, got %q", job.Status)
	}

	// Online lookups still owned by the job show up as pending_online_lookups.
	if _, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "proxycheck", IP: "198.51.100.1", Priority: 10, JobID: "j", NextRunAtNs: 1, EnqueuedAtNs: 1,
	}, EnqueueIfMissing); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "proxycheck", IP: "198.51.100.2", Priority: 10, JobID: "j", NextRunAtNs: 1, EnqueuedAtNs: 1,
	}, EnqueueIfMissing); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := st.ResolveProviderItem(ctx, "proxycheck", "198.51.100.2"); err != nil {
		t.Fatalf("ResolveProviderItem: %v", err)
	}
	pending, err := st.CountPendingOnlineLookups(ctx, "j")
	if err != nil || pending != 1 {
		t.Fatalf("pending = %d (err %v), want 1", pending, err)
	}
	progress, err := st.JobProgressView(ctx, "j")
	if err != nil || progress.PendingOnlineItems != 1 {
		t.Fatalf("progress = %+v (err %v)", progress, err)
	}
	if n, err := st.DeleteJobQueueItems(ctx, "j"); err != nil || n != 2 {
		t.Fatalf("DeleteJobQueueItems = %d (err %v)", n, err)
	}
}

func TestJobs_ListAndFilters(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	for i, id := range []string{"a", "b", "c"} {
		if err := st.CreateJob(ctx, Job{
			ID: id, Kind: "intel", Priority: 10 - i, Status: JobQueued, CreatedAtNs: int64(i + 1),
		}, nil); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
	}
	if err := st.MarkJobRunning(ctx, "a", 100); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}
	all, err := st.ListJobs(ctx, "", 10, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListJobs = %d (err %v)", len(all), err)
	}
	if all[0].ID != "c" {
		t.Fatalf("expected newest first, got %s", all[0].ID)
	}
	active, err := st.ListActiveJobs(ctx, 10)
	if err != nil || len(active) != 3 {
		t.Fatalf("ListActiveJobs = %d (err %v)", len(active), err)
	}
	if active[0].Priority != 10 {
		t.Fatalf("active order = %+v", active)
	}
	count, err := st.CountActiveJobs(ctx)
	if err != nil || count != 3 {
		t.Fatalf("CountActiveJobs = %d (err %v)", count, err)
	}
	count, err = st.CountJobs(ctx, JobRunning)
	if err != nil || count != 1 {
		t.Fatalf("CountJobs(running) = %d (err %v)", count, err)
	}
	items, err := st.ListJobItems(ctx, JobItemFilter{JobID: "a"})
	if err != nil || len(items) != 0 {
		t.Fatalf("ListJobItems = %d (err %v)", len(items), err)
	}
	if n, err := st.CountJobItems(ctx, "a", ""); err != nil || n != 0 {
		t.Fatalf("CountJobItems = %d (err %v)", n, err)
	}
}

func TestJobs_ClaimRespectsJobScope(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, Job{ID: "j1", Kind: "intel", Priority: 5, CreatedAtNs: 1},
		[]JobItem{{NodeHash: "a", NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.CreateJob(ctx, Job{ID: "j2", Kind: "intel", Priority: 50, CreatedAtNs: 2},
		[]JobItem{{NodeHash: "b", NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// Higher priority first.
	claimed, err := st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 1, Owner: "w"})
	if err != nil || len(claimed) != 1 || claimed[0].JobID != "j2" {
		t.Fatalf("claim = %+v (err %v)", claimed, err)
	}
	// JobIDs restricts the claim to the given jobs.
	claimed, err = st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 5, Owner: "w", JobIDs: []string{"j2"}})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("scoped claim = %+v (err %v)", claimed, err)
	}
	claimed, err = st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 5, Owner: "w", JobIDs: []string{"j1"}})
	if err != nil || len(claimed) != 1 || claimed[0].JobID != "j1" {
		t.Fatalf("scoped claim = %+v (err %v)", claimed, err)
	}
	// Canceled jobs are excluded from claims.
	if err := st.CreateJob(ctx, Job{ID: "j3", Kind: "intel", Priority: 99, CreatedAtNs: 3},
		[]JobItem{{NodeHash: "c", NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.CancelJob(ctx, "j3"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	claimed, err = st.ClaimJobItems(ctx, ClaimOptions{NowNs: 1, LeaseUntilNs: 10, Limit: 5, Owner: "w"})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("claim after cancel = %+v (err %v)", claimed, err)
	}
}
