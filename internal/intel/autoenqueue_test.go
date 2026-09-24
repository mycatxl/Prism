package intel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// newAutoEnqueueService builds a service whose scope resolver echoes the
// requested node hashes and whose intel_enabled flag is controllable.
func newAutoEnqueueService(t *testing.T, enabled *bool) *Service {
	t.Helper()
	st := openIntelStore(t)
	svc, err := NewService(Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(_ context.Context, scope jobs.Scope) ([]string, error) {
			return scope.NodeHashes, nil
		}),
		Config: func() jobs.Config {
			return jobs.Config{Enabled: *enabled, NodeWorkers: 1, MaxRunningJobs: 1}
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// testNodeHash builds a deterministic 32-hex node hash. The tag is stripped by
// the node hash function, so the server field carries the uniqueness.
func testNodeHash(t *testing.T, tag string) string {
	t.Helper()
	return node.HashFromRawOptions([]byte(`{"type":"direct","server":"` + tag + `"}`)).String()
}

// TestSubscriptionAutoEnqueueHonoursFlags covers WP08 §3.6: auto_intel=false
// never enqueues, intel_enabled=false never enqueues, auto_intel=true enqueues
// exactly one job per subscription whose kind follows intel_auto_checks.
func TestSubscriptionAutoEnqueueHonoursFlags(t *testing.T) {
	ctx := context.Background()
	enabled := true
	svc := newAutoEnqueueService(t, &enabled)
	st := svc.Store()

	first := testNodeHash(t, "auto-intel-first")
	created, err := svc.AutoEnqueueSubscriptionNodes(ctx, "sub-on", true, []string{first}, false)
	if err != nil || !created {
		t.Fatalf("auto_intel=true must enqueue a job: created=%v err=%v", created, err)
	}
	job, ok, err := findSubscriptionJob(ctx, st, "sub-on")
	if err != nil || !ok {
		t.Fatalf("automatic job missing: ok=%v err=%v", ok, err)
	}
	if job.Kind != string(jobs.KindIntel) {
		t.Fatalf("intel_auto_checks=false must create kind=intel, got %q", job.Kind)
	}
	if job.Priority != jobs.PrioritySubscription {
		t.Fatalf("automatic job priority = %d, want %d", job.Priority, jobs.PrioritySubscription)
	}
	items, err := st.ListJobItems(ctx, store.JobItemFilter{JobID: job.ID})
	if err != nil {
		t.Fatalf("list job items: %v", err)
	}
	if len(items) != 1 || items[0].NodeHash != first {
		t.Fatalf("automatic job items = %+v, want exactly %s", items, first)
	}

	// auto_checks=true switches the kind to full.
	second := testNodeHash(t, "auto-intel-second")
	if _, err := svc.AutoEnqueueSubscriptionNodes(ctx, "sub-on", true, []string{second}, true); err != nil {
		t.Fatalf("auto_checks job: %v", err)
	}
	full, err := findJobWithKind(ctx, st, string(jobs.KindFull))
	if err != nil {
		t.Fatalf("kind=full job missing: %v", err)
	}
	if full.CreatedBy != jobs.CreatedBySubscription("sub-on") {
		t.Fatalf("created_by = %q", full.CreatedBy)
	}

	// auto_intel=false never enqueues.
	created, err = svc.AutoEnqueueSubscriptionNodes(ctx, "sub-off", false, []string{testNodeHash(t, "auto-intel-off")}, false)
	if err != nil {
		t.Fatalf("auto_intel=false must not fail: %v", err)
	}
	if created {
		t.Fatal("auto_intel=false must not create a job")
	}
	if _, ok, _ := findSubscriptionJob(ctx, st, "sub-off"); ok {
		t.Fatal("auto_intel=false created a job for sub-off")
	}

	// intel_enabled=false suspends the auto-enqueue path entirely.
	enabled = false
	created, err = svc.AutoEnqueueSubscriptionNodes(ctx, "sub-on", true, []string{testNodeHash(t, "auto-intel-disabled")}, false)
	if created || !errors.Is(err, jobs.ErrDisabled) {
		t.Fatalf("intel_enabled=false must not enqueue: created=%v err=%v", created, err)
	}
	enabled = true
}

// TestAutoEnqueueRefusesOverLimitBatch covers R4: a batch above the per-job node
// cap fails with the named limit error instead of being truncated.
func TestAutoEnqueueRefusesOverLimitBatch(t *testing.T) {
	ctx := context.Background()
	enabled := true
	svc := newAutoEnqueueService(t, &enabled)
	st := svc.Store()

	tooMany := make([]string, 0, jobs.MaxNodesPerJob+1)
	for i := 0; i <= jobs.MaxNodesPerJob; i++ {
		tooMany = append(tooMany, fmt.Sprintf("%032x", i))
	}

	created, err := svc.AutoEnqueueSubscriptionNodes(ctx, "sub-huge", true, tooMany, false)
	if created {
		t.Fatal("an over-limit batch must not create a job")
	}
	if !errors.Is(err, jobs.ErrTooManyNodes) {
		t.Fatalf("over-limit batch error = %v, want jobs.ErrTooManyNodes", err)
	}
	count, err := st.CountJobs(ctx, "")
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 0 {
		t.Fatalf("jobs after an over-limit batch = %d, want 0", count)
	}

	// The job manager enforces the same cap for manual jobs (§3.1).
	if _, err := svc.CreateJob(ctx, jobs.Request{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: tooMany},
	}, jobs.CreatedByAdmin(), jobs.PriorityManual); !errors.Is(err, jobs.ErrTooManyNodes) {
		t.Fatalf("manual over-limit scope error = %v, want jobs.ErrTooManyNodes", err)
	}
}

// TestAutoEnqueueCoalescesSubscriptionRefresh proves one refresh wave becomes a
// single job per subscription and that a disabled subscription stays out of it.
func TestAutoEnqueueCoalescesSubscriptionRefresh(t *testing.T) {
	ctx := context.Background()
	enabled := true
	svc := newAutoEnqueueService(t, &enabled)
	st := svc.Store()

	svc.EnableAutoEnqueue(AutoEnqueueOptions{
		Lookup: func(subscriptionID string) (bool, bool) {
			return subscriptionID == "sub-on", true
		},
		Delay: 20 * time.Millisecond,
		Logf:  t.Logf,
	})
	// flushHook is the deterministic seam of the coalescer: it fires once a flush
	// finished, i.e. after the job rows were written *and* the counters were
	// updated. Reading the counters as soon as the job row became visible (what
	// this test used to do) races the flush goroutine and made the assertion
	// flaky under load.
	flushed := make(chan struct{}, 4)
	svc.auto.flushHook = func() {
		select {
		case flushed <- struct{}{}:
		default:
		}
	}

	first := testNodeHash(t, "coalesce-first")
	second := testNodeHash(t, "coalesce-second")
	off := testNodeHash(t, "coalesce-off")
	// The whole refresh wave is buffered before the coalescer loop starts, so the
	// wave boundary is decided by the producer instead of by the 20 ms clock. The
	// buffered wake token still arms the timer exactly once, so the flush awaited
	// below is the coalescer's own timer-driven flush.
	if !svc.OfferSubscriptionNode("sub-on", first) {
		t.Fatal("OfferSubscriptionNode rejected a valid node")
	}
	if !svc.OfferSubscriptionNode("sub-off", off) {
		t.Fatal("OfferSubscriptionNode must buffer a disabled subscription too")
	}
	svc.OfferSubscriptionNode("sub-on", second)
	svc.OfferSubscriptionNode("sub-on", first) // duplicate is ignored

	svc.auto.start()
	defer svc.auto.stop()

	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("the coalescer did not flush the buffered refresh wave")
	}

	count, err := st.CountJobs(ctx, "")
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("jobs after the refresh wave = %d, want 1", count)
	}
	job, ok, err := findSubscriptionJob(ctx, st, "sub-on")
	if err != nil || !ok {
		t.Fatalf("coalesced job missing: ok=%v err=%v", ok, err)
	}
	items, err := st.ListJobItems(ctx, store.JobItemFilter{JobID: job.ID})
	if err != nil {
		t.Fatalf("list job items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("coalesced job has %d items, want 2 (one per new node)", len(items))
	}
	for _, item := range items {
		if item.NodeHash == off {
			t.Fatalf("the auto_intel=false subscription leaked node %s into the job", off)
		}
	}
	if _, ok, _ := findSubscriptionJob(ctx, st, "sub-off"); ok {
		t.Fatal("the auto_intel=false subscription created a job")
	}
	created, skipped, dropped, failed := svc.AutoEnqueueCounters()
	if created != 1 || skipped != 1 || dropped != 0 || failed != 0 {
		t.Fatalf("auto-enqueue counters = created:%d skipped:%d dropped:%d failed:%d", created, skipped, dropped, failed)
	}
}

// findSubscriptionJob returns the newest job created by a subscription.
func findSubscriptionJob(ctx context.Context, st *store.Store, subscriptionID string) (store.Job, bool, error) {
	return findJob(ctx, st, func(job store.Job) bool {
		return job.CreatedBy == jobs.CreatedBySubscription(subscriptionID)
	})
}

// findJobWithKind returns the newest job of one kind.
func findJobWithKind(ctx context.Context, st *store.Store, kind string) (store.Job, error) {
	job, ok, err := findJob(ctx, st, func(job store.Job) bool { return job.Kind == kind })
	if err != nil {
		return store.Job{}, err
	}
	if !ok {
		return store.Job{}, fmt.Errorf("no %s job", kind)
	}
	return job, nil
}

func findJob(ctx context.Context, st *store.Store, match func(store.Job) bool) (store.Job, bool, error) {
	list, err := st.ListJobs(ctx, "", 100, 0)
	if err != nil {
		return store.Job{}, false, err
	}
	for _, job := range list {
		if match(job) {
			return job, true, nil
		}
	}
	return store.Job{}, false, nil
}
