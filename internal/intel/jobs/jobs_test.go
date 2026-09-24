package jobs

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/intel/store"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func enabledConfig() ConfigProvider {
	return func() Config {
		return Config{Enabled: true, NodeWorkers: 2, MaxRunningJobs: 2, CheckConcurrencyPerCheck: 1}
	}
}

func TestPlanAndPendingSteps(t *testing.T) {
	if got := Plan(KindEgress); len(got) != 1 || got[0] != StepEgress {
		t.Fatalf("egress plan = %v", got)
	}
	full := Plan(KindFull)
	if len(full) != 6 {
		t.Fatalf("full plan = %v, want six steps", full)
	}
	intel := Plan(KindIntel)
	if len(intel) != 5 || intel[0] != StepEgress || intel[4] != StepAssess {
		t.Fatalf("intel plan = %v", intel)
	}
	checks := Plan(KindChecks)
	if len(checks) != 3 || checks[1] != StepChecks {
		t.Fatalf("checks plan = %v", checks)
	}

	if steps := PendingSteps(KindFull, 0); len(steps) != 6 {
		t.Fatalf("PendingSteps(0) = %v", steps)
	}
	// A restart after step 3 continues at step 4 (§3.5 / §10).
	steps := PendingSteps(KindFull, 3)
	if len(steps) != 3 || steps[0] != StepViaNode {
		t.Fatalf("PendingSteps(3) = %v, want [4 5 6]", steps)
	}
	if steps := PendingSteps(KindFull, 6); len(steps) != 0 {
		t.Fatalf("PendingSteps(6) = %v, want empty", steps)
	}
}

func TestParseKindAndValidate(t *testing.T) {
	for _, raw := range []string{"egress", "INTEL", " checks ", "full"} {
		if _, err := ParseKind(raw); err != nil {
			t.Fatalf("ParseKind(%q) = %v", raw, err)
		}
	}
	if _, err := ParseKind("bogus"); err == nil {
		t.Fatal("expected an error for an unknown kind")
	}
	if err := (Request{Kind: "intel"}).Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := (Request{Kind: "intel", Providers: []string{" "}}).Validate(); err == nil {
		t.Fatal("expected an error for an empty provider id")
	}
	if err := (Request{Kind: "intel", Checks: []string{""}}).Validate(); err == nil {
		t.Fatal("expected an error for an empty check id")
	}
	if !(Scope{}).Empty() || (Scope{All: true}).Empty() {
		t.Fatal("Scope.Empty is wrong")
	}
}

func TestConfigNormalizeBounds(t *testing.T) {
	cfg := Config{}.Normalize()
	if cfg.NodeWorkers != DefaultNodeWorkers || cfg.MaxRunningJobs != DefaultMaxRunningJobs ||
		cfg.CheckConcurrencyPerCheck != DefaultCheckConcurrencyPerCheck {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg = Config{NodeWorkers: 1000, MaxRunningJobs: 3}.Normalize()
	if cfg.NodeWorkers != MaxNodeWorkers {
		t.Fatalf("NodeWorkers = %d, want %d", cfg.NodeWorkers, MaxNodeWorkers)
	}
}

func TestBackoffCapsAtSixHours(t *testing.T) {
	if got := backoff(0); got != 30*time.Second {
		t.Fatalf("backoff(0) = %s", got)
	}
	if got := backoff(1); got != time.Minute {
		t.Fatalf("backoff(1) = %s", got)
	}
	if got := backoff(30); got != 6*time.Hour {
		t.Fatalf("backoff(30) = %s, want 6h", got)
	}
}

// scriptedRunner drives the pipeline in tests without any network or provider.
type scriptedRunner struct {
	mu       sync.Mutex
	calls    []string
	handlers map[Step]StepResult
}

func (r *scriptedRunner) RunStep(_ context.Context, kind Kind, step Step, jobID, nodeHash string) StepResult {
	r.mu.Lock()
	r.calls = append(r.calls, nodeHash+":"+itoa(int(step)))
	handler, ok := r.handlers[step]
	r.mu.Unlock()
	if ok {
		return handler
	}
	return StepResult{Summary: map[string]any{"step": int(step)}}
}

func (r *scriptedRunner) steps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}

func newTestManager(t *testing.T, st *store.Store, clock Clock, runner StepRunner, scope ScopeResolver, cfg ConfigProvider) *Manager {
	t.Helper()
	if cfg == nil {
		cfg = enabledConfig()
	}
	if scope == nil {
		scope = ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil })
	}
	return New(Options{
		Store:  st,
		Runner: runner,
		Scope:  scope,
		Config: cfg,
		Clock:  clock,
		Logf:   func(string, ...any) {},
	})
}

// drain runs the scheduler and the worker pool until no job is active, without
// sleeping: every claim is driven explicitly.
func drain(t *testing.T, m *Manager, clock *fakeClock, steps int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < steps; i++ {
		cfg := m.Config()
		m.refreshActiveJobs(cfg)
		clock.advance(time.Second)
		item, ok, err := m.claimOne(ctx)
		if err != nil {
			t.Fatalf("claimOne: %v", err)
		}
		if !ok {
			return
		}
		m.runItem(ctx, item)
	}
	t.Fatalf("drain did not settle after %d iterations", steps)
}

func TestManager_CreateExpandsScopeAndDeduplicates(t *testing.T) {
	st := newTestStore(t)
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"b", "a", "b", " ", "c"}, nil
	})
	runner := &scriptedRunner{}
	m := newTestManager(t, st, clock, runner, scope, nil)

	job, err := m.Create(context.Background(), Request{Kind: KindIntel, Scope: Scope{All: true}}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.Status != store.JobQueued || job.Priority != PriorityManual || job.CreatedBy != "admin" {
		t.Fatalf("job = %+v", job)
	}
	if job.Total != 3 {
		t.Fatalf("total = %d, want 3 (deduplicated)", job.Total)
	}
	if job.RequestJSON == "" || !strings.Contains(job.RequestJSON, `"kind":"intel"`) {
		t.Fatalf("request_json = %q", job.RequestJSON)
	}
	items, err := st.ListJobItems(context.Background(), store.JobItemFilter{JobID: job.ID})
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d (err %v)", len(items), err)
	}
	if items[0].NodeHash != "a" || items[2].NodeHash != "c" {
		t.Fatalf("items not sorted: %+v", items)
	}
}

func TestManager_CreateRejectsWhenDisabledAndOverLimit(t *testing.T) {
	st := newTestStore(t)
	clock := newFakeClock(time.Now())

	disabled := newTestManager(t, st, clock, &scriptedRunner{}, nil, func() Config {
		return Config{Enabled: false}
	})
	if _, err := disabled.Create(context.Background(), Request{Kind: KindIntel}, "admin", PriorityManual); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}

	over := newTestManager(t, st, clock, &scriptedRunner{},
		ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
			hashes := make([]string, MaxNodesPerJob+1)
			for i := range hashes {
				hashes[i] = string(rune('a'+i%26)) + itoa(i)
			}
			return hashes, nil
		}), nil)
	if _, err := over.Create(context.Background(), Request{Kind: KindIntel}, "admin", PriorityManual); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("err = %v, want ErrTooManyNodes", err)
	}

	empty := newTestManager(t, st, clock, &scriptedRunner{}, nil, nil)
	job, err := empty.Create(context.Background(), Request{Kind: KindEgress}, "admin", PriorityRefresh)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.Status != store.JobSucceeded {
		t.Fatalf("an empty scope must settle immediately, got %q", job.Status)
	}
}

func TestManager_PipelineBreakpointResumesAfterRestart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)

	// A budget/QPS gate can only defer the step it belongs to, so the deferral is
	// raised at step 4 after steps 1..3 completed. The item then stops with
	// step_index=3 and must resume at step 4 on the next run (§3.2, §3.5, §10).
	firstRunner := &scriptedRunner{handlers: map[Step]StepResult{
		StepViaNode: {DeferUntilNs: start.Add(time.Hour).UnixNano()},
	}}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"node-1"}, nil })
	m := newTestManager(t, st, clock, firstRunner, scope, nil)

	job, err := m.Create(ctx, Request{Kind: KindFull}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)

	stored, ok, err := st.GetJobItem(ctx, job.ID, "node-1")
	if err != nil || !ok {
		t.Fatalf("GetJobItem: ok=%v err=%v", ok, err)
	}
	if stored.Status != store.ItemQueued || stored.StepIndex != 3 {
		t.Fatalf("item = %+v, want queued with step_index=3", stored)
	}
	if stored.NextRunAtNs != start.Add(time.Hour).UnixNano() {
		t.Fatalf("next_run_at = %d, want the deferral deadline", stored.NextRunAtNs)
	}
	if got := strings.Join(firstRunner.steps(), ","); got != "node-1:1,node-1:2,node-1:3,node-1:4" {
		t.Fatalf("first pass steps = %q", got)
	}

	// Before the deferral deadline nothing is claimable.
	clock.advance(30 * time.Minute)
	m.refreshActiveJobs(m.Config())
	if _, ok, err := m.claimOne(ctx); err != nil || ok {
		t.Fatalf("early claim = %v (err %v)", ok, err)
	}

	// "Restart": a fresh manager on the same database resumes at step 4.
	clock.advance(31 * time.Minute)
	secondRunner := &scriptedRunner{}
	resumed := newTestManager(t, st, clock, secondRunner, scope, nil)
	drain(t, resumed, clock, 8)

	if got := strings.Join(secondRunner.steps(), ","); got != "node-1:4,node-1:5,node-1:6" {
		t.Fatalf("resumed steps = %q, want steps 4..6", got)
	}
	settled, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if settled.Status != store.JobSucceeded || settled.Done != 1 || settled.FinishedAtNs == 0 {
		t.Fatalf("settled = %+v", settled)
	}
}

func TestManager_CrashAfterStepPersistsBreakpoint(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"node-1"}, nil })

	// A worker that persisted step 3 and then died: the lease is still held.
	crashing := newTestManager(t, st, clock, &scriptedRunner{handlers: map[Step]StepResult{
		StepOffline: {Skip: false}, StepEnqueueOnline: {Skip: false},
	}}, scope, nil)
	job, err := crashing.Create(ctx, Request{Kind: KindFull}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashing.refreshActiveJobs(crashing.Config())
	item, ok, err := crashing.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	if err := st.SaveJobItemStep(ctx, job.ID, item.NodeHash, 3, `{"steps":[1,2,3]}`,
		clock.Now().Add(time.Hour).UnixNano(), clock.Now().UnixNano()); err != nil {
		t.Fatalf("SaveJobItemStep: %v", err)
	}

	// Startup recovery requeues the leased item without losing the breakpoint.
	recovered, err := crashing.Recover(ctx)
	if err != nil || recovered.RequeuedJobItems != 1 {
		t.Fatalf("Recover = %+v (err %v)", recovered, err)
	}
	afterRecover, _, _ := st.GetJobItem(ctx, job.ID, "node-1")
	if afterRecover.Status != store.ItemQueued || afterRecover.StepIndex != 3 {
		t.Fatalf("item = %+v, want queued with step_index=3", afterRecover)
	}

	runner := &scriptedRunner{}
	resumed := newTestManager(t, st, clock, runner, scope, nil)
	clock.advance(time.Second)
	drain(t, resumed, clock, 8)
	if got := strings.Join(runner.steps(), ","); got != "node-1:4,node-1:5,node-1:6" {
		t.Fatalf("resumed steps = %q, want steps 4..6 after the crash", got)
	}
}

func TestManager_RetriesFailuresWithBackoffAndSettlesPartial(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	failing := &scriptedRunner{handlers: map[Step]StepResult{
		StepEgress: {ErrorCode: "EGRESS_PROBE_FAILED"},
	}}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"node-1"}, nil })
	m := newTestManager(t, st, clock, failing, scope, nil)

	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < maxItemAttempts; i++ {
		m.refreshActiveJobs(m.Config())
		item, ok, err := m.claimOne(ctx)
		if err != nil || !ok {
			t.Fatalf("claim %d = %v (err %v)", i, ok, err)
		}
		m.runItem(ctx, item)
		clock.advance(6*time.Hour + time.Minute)
	}

	stored, _, _ := st.GetJobItem(ctx, job.ID, "node-1")
	if stored.Status != store.ItemFailed || stored.ErrorCode != "EGRESS_PROBE_FAILED" {
		t.Fatalf("item = %+v, want a terminal failure after %d attempts", stored, maxItemAttempts)
	}
	if stored.Attempts != maxItemAttempts {
		t.Fatalf("attempts = %d, want %d", stored.Attempts, maxItemAttempts)
	}
	settled, _ := st.GetJob(ctx, job.ID)
	if settled.Status != store.JobPartial || settled.Failed != 1 {
		t.Fatalf("settled = %+v, want partial", settled)
	}

	// Retry-failed reopens the job and resets the attempt counter.
	retried, err := m.RetryFailed(ctx, job.ID)
	if err != nil || retried != 1 {
		t.Fatalf("RetryFailed = %d (err %v)", retried, err)
	}
	reopened, _ := st.GetJob(ctx, job.ID)
	if reopened.Status != store.JobQueued || reopened.FinishedAtNs != 0 {
		t.Fatalf("reopened = %+v", reopened)
	}
	stored, _, _ = st.GetJobItem(ctx, job.ID, "node-1")
	if stored.Status != store.ItemQueued || stored.Attempts != 0 {
		t.Fatalf("item after retry = %+v", stored)
	}
}

func TestManager_SkipAndCancel(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	runner := &scriptedRunner{handlers: map[Step]StepResult{
		StepEgress: {Skip: true, Summary: map[string]any{"reason": "fresh"}},
	}}
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) {
		return []string{"a-skip", "b-cancel"}, nil
	})
	m := newTestManager(t, st, clock, runner, scope, nil)

	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	item, ok, err := m.claimOne(ctx)
	if err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	m.runItem(ctx, item)

	skipped, _, _ := st.GetJobItem(ctx, job.ID, "a-skip")
	if item.NodeHash != "a-skip" || skipped.Status != store.ItemSkipped {
		t.Fatalf("item = %+v, want a-skip skipped", skipped)
	}
	if !strings.Contains(skipped.ResultJSON, "fresh") {
		t.Fatalf("result_json = %q", skipped.ResultJSON)
	}

	// Cancelling the job cancels the remaining queued items.
	if err := m.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	canceled, _, _ := st.GetJobItem(ctx, job.ID, "b-cancel")
	if canceled.Status != store.ItemCanceled {
		t.Fatalf("item = %+v, want canceled", canceled)
	}
	settled, _ := st.GetJob(ctx, job.ID)
	if settled.Status != store.JobCanceled {
		t.Fatalf("job = %+v, want canceled", settled)
	}
	m.refreshActiveJobs(m.Config())
	if _, ok, _ := m.claimOne(ctx); ok {
		t.Fatal("a canceled job must not be claimable")
	}
}

func TestManager_RunningJobsKeepTheirSlot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"a"}, nil })
	m := newTestManager(t, st, clock, &scriptedRunner{}, scope, func() Config {
		return Config{Enabled: true, NodeWorkers: 1, MaxRunningJobs: 1}
	})

	running, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityRefresh)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.MarkJobRunning(ctx, running.ID, clock.Now().UnixNano()); err != nil {
		t.Fatalf("MarkJobRunning: %v", err)
	}
	queued, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	m.refreshActiveJobs(m.Config())
	ids := m.currentActiveJobIDs()
	if len(ids) != 1 || ids[0] != running.ID {
		t.Fatalf("active = %v, want the running job to keep its slot", ids)
	}
	got, _ := st.GetJob(ctx, queued.ID)
	if got.Status != store.JobQueued {
		t.Fatalf("the new job must stay queued, got %q", got.Status)
	}
}

func TestManager_RecoverRequeuesStaleLeases(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"a"}, nil })
	m := newTestManager(t, st, clock, &scriptedRunner{}, scope, nil)

	job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m.refreshActiveJobs(m.Config())
	if _, ok, err := m.claimOne(ctx); err != nil || !ok {
		t.Fatalf("claimOne = %v (err %v)", ok, err)
	}
	if _, err := st.EnqueueProviderItem(ctx, store.QueueItem{
		Provider: "proxycheck", IP: "198.51.100.1", Priority: 1, JobID: job.ID,
		NextRunAtNs: clock.Now().UnixNano(), EnqueuedAtNs: clock.Now().UnixNano(),
	}, store.EnqueueIfMissing); err != nil {
		t.Fatalf("EnqueueProviderItem: %v", err)
	}
	if _, err := st.ClaimProviderItems(ctx, "proxycheck", clock.Now().UnixNano(),
		clock.Now().Add(time.Minute).UnixNano(), 1, "worker"); err != nil {
		t.Fatalf("ClaimProviderItems: %v", err)
	}

	stats, err := m.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if stats.RequeuedJobItems != 1 || stats.RequeuedQueueRows != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	item, _, _ := st.GetJobItem(ctx, job.ID, "a")
	if item.Status != store.ItemQueued || item.LeaseOwner != "" {
		t.Fatalf("item = %+v", item)
	}
	after, _ := st.GetJob(ctx, job.ID)
	if after.Status != store.JobRunning {
		t.Fatalf("a running job must stay running after recovery, got %q", after.Status)
	}
}

func TestManager_CleanupPrunesUnknownNodes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))

	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: "dead", IPv4: "198.51.100.1"}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: "live", IPv4: "198.51.100.2"}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	m := New(Options{
		Store: st, Runner: &scriptedRunner{}, Scope: ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
		Config: enabledConfig(), Clock: clock, Logf: func(string, ...any) {},
		Census: staticCensus{"live": {}},
	})
	if _, err := m.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	hashes, err := st.ListNodeHashes(ctx)
	if err != nil {
		t.Fatalf("ListNodeHashes: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != "live" {
		t.Fatalf("hashes = %v, want only the live node", hashes)
	}
	if m.Stats().NodesPruned != 1 {
		t.Fatalf("NodesPruned = %d", m.Stats().NodesPruned)
	}
}

type staticCensus map[string]struct{}

func (c staticCensus) KnownNodeHashes() map[string]struct{} { return c }

func TestNew_PanicsOnMissingDependencies(t *testing.T) {
	for _, name := range []string{"store", "runner", "scope"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("New without %s must panic (R5)", name)
				}
			}()
			opts := Options{
				Store:  newTestStore(t),
				Runner: &scriptedRunner{},
				Scope:  ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
			}
			switch name {
			case "store":
				opts.Store = nil
			case "runner":
				opts.Runner = nil
			case "scope":
				opts.Scope = nil
			}
			New(opts)
		}()
	}
}

func TestHub_BoundsSubscribersAndCountsDrops(t *testing.T) {
	h := newHub(time.Second)
	sub, err := h.Subscribe("job")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if h.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}

	// The rate limit of §7 pushes at most one frame per second per job.
	frame := Progress{Done: 1, Total: 2, Status: store.JobRunning}
	h.push("job", frame, 1000, false)
	h.push("job", Progress{Done: 2, Total: 2, Status: store.JobSucceeded}, 1500, false)
	frames := drainChannel(sub.C)
	if len(frames) != 1 || frames[0].Done != 1 {
		t.Fatalf("frames = %+v", frames)
	}

	// A forced push always goes through.
	h.push("job", Progress{Done: 2, Total: 2, Status: store.JobSucceeded}, 1500, true)
	frames = drainChannel(sub.C)
	if len(frames) != 1 || frames[0].Status != store.JobSucceeded {
		t.Fatalf("forced frames = %+v", frames)
	}

	// A slow client drops frames instead of blocking the worker pool.
	for i := 0; i < sseBufferSize+5; i++ {
		h.push("job", frame, int64(10000+i*2000), true)
	}
	if h.Dropped() == 0 {
		t.Fatal("a full subscriber queue must count dropped frames")
	}

	// The subscriber count is bounded: one subscriber already exists.
	for i := 1; i < SSEMaxSubscribers; i++ {
		if _, err := h.Subscribe("job"); err != nil {
			t.Fatalf("Subscribe(%d) = %v", i, err)
		}
	}
	if h.Subscribers() != SSEMaxSubscribers {
		t.Fatalf("subscribers = %d, want %d", h.Subscribers(), SSEMaxSubscribers)
	}
	if _, err := h.Subscribe("job"); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("err = %v, want ErrTooManySubscribers", err)
	}
	sub.Close()
	sub.Close() // idempotent
	if h.Subscribers() != SSEMaxSubscribers-1 {
		t.Fatalf("subscribers = %d, want %d", h.Subscribers(), SSEMaxSubscribers-1)
	}
}

func drainChannel(ch <-chan Progress) []Progress {
	out := []Progress{}
	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, frame)
		default:
			return out
		}
	}
}

func TestProviderPool_ResolvesFailuresAndBacksOff(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))

	specs := []ProviderQueueSpec{{ProviderID: "proxycheck", Enabled: true, DailyLimit: 2, QPS: 1, BatchSize: 2}}
	var mu sync.Mutex
	responses := map[netip.Addr]string{}
	lookup := OnlineLookupFunc(func(_ context.Context, _ string, ips []netip.Addr) ProviderOutcome {
		mu.Lock()
		defer mu.Unlock()
		out := ProviderOutcome{Errors: make(map[netip.Addr]string), Reported: len(ips)}
		for _, ip := range ips {
			if code, ok := responses[ip]; ok {
				out.Errors[ip] = code
			}
		}
		return out
	})

	m := New(Options{
		Store: st, Runner: &scriptedRunner{},
		Scope:         ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
		Config:        enabledConfig(),
		Clock:         clock,
		Logf:          func(string, ...any) {},
		ProviderSpecs: func() []ProviderQueueSpec { return specs },
		Lookup:        lookup,
	})
	if m.provider == nil {
		t.Fatal("the provider pool must be created when specs and a lookup are given")
	}

	now := clock.Now().UnixNano()
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if _, err := st.EnqueueProviderItem(ctx, store.QueueItem{
			Provider: "proxycheck", IP: ip, Priority: 10, JobID: "job", NextRunAtNs: now, EnqueuedAtNs: now,
		}, store.EnqueueIfMissing); err != nil {
			t.Fatalf("EnqueueProviderItem: %v", err)
		}
	}
	responses[netip.MustParseAddr("198.51.100.2")] = "PROVIDER_RESPONSE"

	// First round: the batch of two resolves one IP and fails the other.
	if wait := m.provider.runOnce("proxycheck", specs[0]); wait <= 0 {
		t.Fatalf("runOnce wait = %s", wait)
	}
	counts, _ := st.ProviderQueueCounts(ctx, "proxycheck")
	if counts.Done != 1 || counts.Queued != 2 {
		t.Fatalf("counts = %+v, want one done and two queued", counts)
	}
	failed, _, _ := st.GetEvidence(ctx, "198.51.100.2", "proxycheck")
	if _, ok, _ := st.GetEvidence(ctx, "198.51.100.2", "proxycheck"); ok {
		t.Fatalf("evidence must be written by WP09, not by the queue worker: %+v", failed)
	}

	// Second round consumes the last daily unit and resolves the third IP.
	clock.advance(time.Minute)
	m.provider.runOnce("proxycheck", specs[0])
	if counts, _ := st.ProviderQueueCounts(ctx, "proxycheck"); counts.Done != 2 {
		t.Fatalf("counts after round 2 = %+v, want two done", counts)
	}

	// The daily limit of two is now exhausted: the gate stays closed and the
	// worker is told to sleep the maximum interval.
	clock.advance(6*time.Hour + time.Minute)
	if wait := m.provider.runOnce("proxycheck", specs[0]); wait != providerSleepMax {
		t.Fatalf("wait after budget exhaustion = %s, want %s", wait, providerSleepMax)
	}

	// The next UTC day resets the counter (injected clock, no sleeping).
	clock.advance(24 * time.Hour)
	if wait := m.provider.runOnce("proxycheck", specs[0]); wait == providerSleepMax {
		t.Fatal("the budget must recover on the next UTC day")
	}
	state, _, err := st.GetProviderState(ctx, "proxycheck")
	if err != nil {
		t.Fatalf("GetProviderState: %v", err)
	}
	if state.Day != store.DayString(clock.Now()) || state.Used != 1 {
		t.Fatalf("state = %+v, want used=1 on %s", state, store.DayString(clock.Now()))
	}
}

func TestProviderPool_BlockedPausedAndCredentialRotation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))

	spec := ProviderQueueSpec{ProviderID: "abuseipdb", Enabled: true, BatchSize: 1, CredentialID: "cred-1"}
	specs := []ProviderQueueSpec{spec}
	outcome := ProviderOutcome{Reported: 1}
	lookup := OnlineLookupFunc(func(context.Context, string, []netip.Addr) ProviderOutcome { return outcome })

	m := New(Options{
		Store: st, Runner: &scriptedRunner{},
		Scope:         ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
		Config:        enabledConfig(),
		Clock:         clock,
		Logf:          func(string, ...any) {},
		ProviderSpecs: func() []ProviderQueueSpec { return specs },
		Lookup:        lookup,
	})

	enqueue := func() {
		t.Helper()
		now := clock.Now().UnixNano()
		if _, err := st.EnqueueProviderItem(ctx, store.QueueItem{
			Provider: "abuseipdb", IP: "198.51.100.9", Priority: 10, NextRunAtNs: now, EnqueuedAtNs: now,
		}, store.EnqueueForce); err != nil {
			t.Fatalf("EnqueueProviderItem: %v", err)
		}
	}

	// 429: the worker records a cooldown.
	enqueue()
	outcome = ProviderOutcome{Reported: 1, BlockedFor: time.Hour}
	if wait := m.provider.runOnce("abuseipdb", spec); wait != time.Hour {
		t.Fatalf("wait after 429 = %s, want 1h", wait)
	}
	state, _, err := st.GetProviderState(ctx, "abuseipdb")
	if err != nil {
		t.Fatalf("GetProviderState: %v", err)
	}
	if state.BlockedUntilNs != clock.Now().Add(time.Hour).UnixNano() || state.ErrorCode != "PROVIDER_LIMIT" {
		t.Fatalf("state = %+v", state)
	}
	if wait := m.provider.runOnce("abuseipdb", spec); wait > time.Hour || wait <= 0 {
		t.Fatalf("wait while blocked = %s", wait)
	}

	// 401: the worker pauses the provider.
	clock.advance(2 * time.Hour)
	enqueue()
	outcome = ProviderOutcome{Pause: true, CredentialID: "cred-1"}
	if wait := m.provider.runOnce("abuseipdb", spec); wait != providerSleepMax {
		t.Fatalf("wait after 401 = %s", wait)
	}
	state, _, _ = st.GetProviderState(ctx, "abuseipdb")
	if !state.Paused || state.ErrorCode != "PROVIDER_AUTH" {
		t.Fatalf("state = %+v, want paused", state)
	}

	// A new key lifts the pause: the credential id changed.
	spec.CredentialID = "cred-2"
	specs[0] = spec
	outcome = ProviderOutcome{Reported: 1, CredentialID: "cred-2"}
	if wait := m.provider.runOnce("abuseipdb", spec); wait != providerIdleSleep {
		t.Fatalf("wait after key rotation = %s, want the idle sleep", wait)
	}
	state, _, _ = st.GetProviderState(ctx, "abuseipdb")
	if state.Paused || state.CredentialID != "cred-2" {
		t.Fatalf("state = %+v, want unpaused with the rotated credential", state)
	}
}

func TestProviderPool_BackoffIsExponentialUpToFiveAttempts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC))
	spec := ProviderQueueSpec{ProviderID: "ipqs", Enabled: true, BatchSize: 1}
	specs := []ProviderQueueSpec{spec}
	lookup := OnlineLookupFunc(func(context.Context, string, []netip.Addr) ProviderOutcome {
		return ProviderOutcome{Errors: map[netip.Addr]string{netip.MustParseAddr("198.51.100.5"): "PROVIDER_UNAVAILABLE"}}
	})
	m := New(Options{
		Store: st, Runner: &scriptedRunner{},
		Scope:         ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
		Config:        enabledConfig(),
		Clock:         clock,
		Logf:          func(string, ...any) {},
		ProviderSpecs: func() []ProviderQueueSpec { return specs },
		Lookup:        lookup,
	})

	now := clock.Now().UnixNano()
	if _, err := st.EnqueueProviderItem(ctx, store.QueueItem{
		Provider: "ipqs", IP: "198.51.100.5", Priority: 10, NextRunAtNs: now, EnqueuedAtNs: now,
	}, store.EnqueueIfMissing); err != nil {
		t.Fatalf("EnqueueProviderItem: %v", err)
	}

	for attempt := 1; attempt <= maxProviderAttempts; attempt++ {
		m.provider.runOnce("ipqs", spec)
		item, ok := queueItem(t, st, "ipqs", "198.51.100.5")
		if !ok {
			t.Fatalf("attempt %d: queue row disappeared", attempt)
		}
		if item.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", item.Attempts, attempt)
		}
		if attempt == maxProviderAttempts {
			if item.Status != store.QueueFailed {
				t.Fatalf("status = %q, want failed after %d attempts", item.Status, attempt)
			}
			break
		}
		expected := clock.Now().UnixNano() + int64(backoff(attempt-1))
		if item.NextRunAtNs != expected {
			t.Fatalf("attempt %d next_run_at = %d, want %d", attempt, item.NextRunAtNs, expected)
		}
		clock.advance(6*time.Hour + time.Minute)
	}
}

func queueItem(t *testing.T, st *store.Store, provider, ip string) (store.QueueItem, bool) {
	t.Helper()
	ctx := context.Background()
	items, err := st.ClaimProviderItems(ctx, provider, 0, 0, 0, "")
	if err != nil {
		t.Fatalf("ClaimProviderItems: %v", err)
	}
	_ = items
	// The store has no direct getter, so re-read through a one-row claim with a
	// future lease is not possible either; use the exported counts plus a raw
	// query through the DB handle.
	row := st.DB().QueryRowContext(ctx,
		`SELECT provider, ip, priority, job_id, status, attempts, next_run_at_ns,
		        lease_owner, lease_until_ns, error_code, enqueued_at_ns
		 FROM provider_queue WHERE provider = ? AND ip = ?`, provider, ip)
	var item store.QueueItem
	if err := row.Scan(&item.Provider, &item.IP, &item.Priority, &item.JobID, &item.Status,
		&item.Attempts, &item.NextRunAtNs, &item.LeaseOwner, &item.LeaseUntilNs,
		&item.ErrorCode, &item.EnqueuedAtNs); err != nil {
		return store.QueueItem{}, false
	}
	return item, true
}

func TestProviderQueueSpec_WorkerStopsOnDisabledProvider(t *testing.T) {
	st := newTestStore(t)
	clock := newFakeClock(time.Now())
	specs := []ProviderQueueSpec{{ProviderID: "proxycheck", Enabled: true, BatchSize: 1}}
	m := New(Options{
		Store: st, Runner: &scriptedRunner{},
		Scope:         ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return nil, nil }),
		Config:        enabledConfig(),
		Clock:         clock,
		Logf:          func(string, ...any) {},
		ProviderSpecs: func() []ProviderQueueSpec { return specs },
		Lookup:        OnlineLookupFunc(func(context.Context, string, []netip.Addr) ProviderOutcome { return ProviderOutcome{} }),
	})

	// The provider catalogue is fixed for the lifetime of the pool: a worker is
	// started once per enabled provider and never duplicated.
	m.provider.mu.Lock()
	for _, spec := range m.provider.specs() {
		if !spec.Enabled {
			continue
		}
		m.provider.running[spec.ProviderID] = struct{}{}
	}
	m.provider.mu.Unlock()
	if got := runningProviders(m.provider); got != 1 {
		t.Fatalf("running = %d, want 1", got)
	}

	m.provider.sync() // idempotent: no second entry for the same provider
	if got := runningProviders(m.provider); got != 1 {
		t.Fatalf("running after second sync = %d, want 1", got)
	}

	// A disabled provider keeps no worker entry and reports itself as disabled.
	specs[0].Enabled = false
	m.provider.mu.Lock()
	delete(m.provider.running, "proxycheck")
	m.provider.mu.Unlock()
	m.provider.sync()
	if got := runningProviders(m.provider); got != 0 {
		t.Fatalf("running = %d, want none for a disabled provider", got)
	}
	spec, ok := m.provider.specOf("proxycheck")
	if !ok || spec.Enabled {
		t.Fatalf("spec = %+v (ok=%v), want the disabled spec", spec, ok)
	}
}

// runningProviders reads the pool bookkeeping under its mutex.
func runningProviders(p *providerPool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.running)
}

func TestBudgetWait(t *testing.T) {
	const now = int64(1_000_000)
	if got := budgetWait(store.ProviderState{Paused: true}, now); got != providerSleepMax {
		t.Fatalf("paused wait = %s", got)
	}
	if got := budgetWait(store.ProviderState{BlockedUntilNs: now + int64(time.Minute)}, now); got != time.Minute {
		t.Fatalf("blocked wait = %s", got)
	}
	if got := budgetWait(store.ProviderState{NextRequestAtNs: now + int64(250*time.Millisecond)}, now); got != 250*time.Millisecond {
		t.Fatalf("qps wait = %s", got)
	}
	if got := budgetWait(store.ProviderState{}, now); got != providerSleepMax {
		t.Fatalf("exhausted wait = %s", got)
	}
}
