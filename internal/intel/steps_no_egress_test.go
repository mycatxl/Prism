package intel

import (
	"context"
	"sync"
	"testing"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// PRISM-DEVIATION: none. These tests pin a defect found in a real deployment:
// a node whose egress probe had not landed yet was reported as a finished,
// fully-checked node that had produced no evidence at all.

// noEgressHandlers builds the three provider steps over an empty intel.db. A
// bare registry is enough: every step under test returns before it ever looks
// at a data source, and that is exactly the property being pinned.
func noEgressHandlers(t *testing.T, st *store.Store) StepHandlers {
	t.Helper()
	handlers := NewStepHandlers(StepOptions{
		Store:    st,
		Registry: providers.NewRegistry(),
		Config:   func() jobs.Config { return jobs.Config{Enabled: true}.Normalize() },
		Clock:    jobs.SystemClock{},
		Logf:     t.Logf,
	})
	if handlers.Offline == nil || handlers.EnqueueOnline == nil || handlers.ViaNode == nil {
		t.Fatal("NewStepHandlers did not wire the provider steps")
	}
	return handlers
}

// noEgressRequest seeds one running intel job for one node.
func noEgressRequest(t *testing.T, st *store.Store, jobID, nodeHash string, stepIndex int) {
	t.Helper()
	requestJSON := `{"kind":"intel","scope":{"node_hashes":["` + nodeHash + `"]}}`
	now := time.Now().UTC().UnixNano()
	if err := st.CreateJob(context.Background(), store.Job{
		ID: jobID, Kind: string(jobs.KindIntel), Status: store.JobRunning,
		RequestJSON: requestJSON, CreatedBy: jobs.CreatedByAdmin(), CreatedAtNs: now,
	}, []store.JobItem{{
		JobID: jobID, NodeHash: nodeHash, Status: store.ItemQueued, StepIndex: stepIndex,
		NextRunAtNs: now, UpdatedAtNs: now,
	}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
}

// TestNoEgressObservationIsRetryableNotSkipped pins the shape of the result the
// provider steps return when the node has no egress observation yet.
//
// The defect it guards against: the steps used to return skipped(), which only
// sets Summary. jobs.Manager does not stop on a summary - it walks the remaining
// steps and finishes the item as ItemDone, so the job settled as succeeded while
// the node had produced no evidence at all. The steps must instead name an error
// code, which is what puts the item on the retry path.
func TestNoEgressObservationIsRetryableNotSkipped(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()
	handlers := noEgressHandlers(t, st)

	hash := node.HashFromRawOptions([]byte(`{"type":"direct","tag":"prism-no-egress"}`))
	nodeHash := hash.String()

	steps := []struct {
		name    string
		handler func(context.Context, string, string) jobs.StepResult
	}{
		{"offline", handlers.Offline},
		{"enqueue_online", handlers.EnqueueOnline},
		{"via_node", handlers.ViaNode},
	}

	// Case 1: the node was never probed - node_egress holds no row.
	noEgressRequest(t, st, "job-no-egress-missing", nodeHash, 1)
	for _, tc := range steps {
		result := tc.handler(ctx, "job-no-egress-missing", nodeHash)
		if result.ErrorCode != CodeNoEgressObservation {
			t.Fatalf("%s without an egress row: error code = %q (%v), want %q",
				tc.name, result.ErrorCode, result.Summary, CodeNoEgressObservation)
		}
		if _, ok := result.Summary["skipped"]; ok {
			t.Fatalf("%s without an egress row reported a skip: %v", tc.name, result.Summary)
		}
		if result.Skip {
			t.Fatalf("%s without an egress row asked to skip the item", tc.name)
		}
	}

	// Case 2: the row exists but holds no usable address (both families empty).
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: nodeHash}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
	noEgressRequest(t, st, "job-no-egress-empty", nodeHash, 1)
	for _, tc := range steps {
		result := tc.handler(ctx, "job-no-egress-empty", nodeHash)
		if result.ErrorCode != CodeNoEgressObservation {
			t.Fatalf("%s with an empty egress row: error code = %q (%v), want %q",
				tc.name, result.ErrorCode, result.Summary, CodeNoEgressObservation)
		}
		if result.Skip {
			t.Fatalf("%s with an empty egress row asked to skip the item", tc.name)
		}
	}
}

// steppingClock is a clock a test advances from another goroutine while the
// manager's worker pool runs. jobs.Manager reads it from several goroutines, so
// the value is guarded.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// noEgressRunner routes the manager's steps to the real provider handlers, so
// the test drives the production result through the production retry path.
type noEgressRunner struct {
	handlers StepHandlers
}

func (r noEgressRunner) RunStep(ctx context.Context, _ jobs.Kind, step jobs.Step, jobID, nodeHash string) jobs.StepResult {
	switch step {
	case jobs.StepOffline:
		return r.handlers.Offline(ctx, jobID, nodeHash)
	case jobs.StepEnqueueOnline:
		return r.handlers.EnqueueOnline(ctx, jobID, nodeHash)
	case jobs.StepViaNode:
		return r.handlers.ViaNode(ctx, jobID, nodeHash)
	default:
		return jobs.StepResult{Summary: map[string]any{"step": int(step)}}
	}
}

// TestNoEgressObservationSettlesItemFailedAndJobPartial drives the real provider
// steps through the real job manager until the attempts run out. A node that
// never gets an egress observation must end as a failed item and a partial job -
// not as a finished node with no evidence behind it.
func TestNoEgressObservationSettlesItemFailedAndJobPartial(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()

	hash := node.HashFromRawOptions([]byte(`{"type":"direct","tag":"prism-no-egress-settle"}`))
	nodeHash := hash.String()

	// The item is parked at step 1: the egress probe of this test's kind is not
	// the subject here, the three provider steps are.
	const jobID = "job-no-egress-settle"
	noEgressRequest(t, st, jobID, nodeHash, 1)

	clock := &steppingClock{now: time.Now().UTC()}
	manager := jobs.New(jobs.Options{
		Store:  st,
		Runner: noEgressRunner{handlers: noEgressHandlers(t, st)},
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return []string{nodeHash}, nil
		}),
		Config: func() jobs.Config {
			return jobs.Config{Enabled: true, NodeWorkers: 1, MaxRunningJobs: 1}.Normalize()
		},
		Clock: clock,
		Tick:  10 * time.Millisecond,
		Logf:  func(string, ...any) {},
	})
	manager.Start()
	defer manager.Stop()

	// Each retry is scheduled with exponential backoff (30s, 1m, 2m, 4m), so the
	// test jumps the clock forward instead of waiting for it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		item, ok, err := st.GetJobItem(ctx, jobID, nodeHash)
		if err != nil || !ok {
			t.Fatalf("read job item: ok=%v err=%v", ok, err)
		}
		if item.Status == store.ItemFailed {
			if item.ErrorCode != CodeNoEgressObservation {
				t.Fatalf("item error code = %q, want %q", item.ErrorCode, CodeNoEgressObservation)
			}
			if item.Attempts != 5 {
				t.Fatalf("item attempts = %d, want 5 (maxItemAttempts)", item.Attempts)
			}
			break
		}
		if item.Status != store.ItemQueued && item.Status != store.ItemRunning {
			t.Fatalf("item status = %q (%v), want a queued item that keeps retrying or a terminal failure",
				item.Status, item.ResultJSON)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the item never settled: status=%q attempts=%d", item.Status, item.Attempts)
		}
		clock.advance(10 * time.Minute)
		time.Sleep(20 * time.Millisecond)
	}

	// finishItem writes the item and then settles the job in a second step
	// (FinishJobItem followed by publish -> RefreshJob), so an item that reads
	// as failed does not yet guarantee the job counters moved. Wait for the job
	// to settle instead of racing that window.
	var job store.Job
	var err error
	for {
		job, err = st.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if job.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job never settled after its item failed: status=%q failed=%d",
				job.Status, job.Failed)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != store.JobPartial {
		t.Fatalf("job status = %q, want %q: a node that produced no evidence must not be reported as a success",
			job.Status, store.JobPartial)
	}
	if job.Failed != 1 || job.Done != 0 {
		t.Fatalf("job counters = failed %d / done %d, want failed 1 / done 0", job.Failed, job.Done)
	}
}
