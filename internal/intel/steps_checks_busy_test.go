package intel

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"prism/internal/intel/checks"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
)

// PRISM-DEVIATION: none. These tests pin a defect found in a real deployment:
// a rule that never executed was stored as a node_checks row with outcome=error,
// which reads as a property of the node, and the item then went through the
// exponential error backoff until the rule was dropped for good.
//
// "Never executed" is what the engine reports as CHECK_BUSY or CHECK_CANCELED.
// The tests below reproduce CHECK_BUSY honestly: one rule, a per-rule concurrency
// of one, and another node holding that slot while the step waits for it.

// checksFixtureRuleYAML is one rule whose only step calls targetURL.
func checksFixtureRuleYAML(id, targetURL string) string {
	return `id: ` + id + `
version: 1
name: Fixture ` + id + `
category: other
enabled: true
ttl: 2h
timeout: 30s
calibrated: fixture
steps:
  - id: home
    request:
      method: GET
      url: ` + targetURL + `
outcomes:
  - when: {step: home, status_in: [200], body_contains: ready}
    outcome: available
  - when: {step: home, status_in: [403]}
    outcome: blocked
  - when: {step: home, error: any}
    outcome: error
default: unknown
`
}

// checksFixtureEngine builds an engine holding exactly one rule that answers
// /ready with 200 and a body containing "ready".
func checksFixtureEngine(t *testing.T, ruleID, serverURL string) *checks.Engine {
	t.Helper()
	engine := checks.NewEngine(checks.Options{
		BuiltinFS: fs.FS(fstest.MapFS{
			"builtin/" + ruleID + ".yaml": &fstest.MapFile{
				Data: []byte(checksFixtureRuleYAML(ruleID, serverURL+"/ready")),
			},
		}),
		Now:  time.Now,
		Logf: t.Logf,
	})
	if errs := engine.LoadErrors(); len(errs) != 0 {
		t.Fatalf("check rule load errors: %v", errs)
	}
	if _, ok := engine.Rule(ruleID); !ok {
		t.Fatalf("check rule %s was not loaded", ruleID)
	}
	return engine
}

// checksFixtureServer answers the rule target and counts the requests it served.
//
// served, when non-nil, receives one signal per served request. hold, when
// non-nil, keeps the request open until it is closed - which is how one node
// keeps the rule's only concurrency slot busy while another node tries to run it.
func checksFixtureServer(t *testing.T, served chan<- struct{}, hold <-chan struct{}) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ready") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		hits++
		mu.Unlock()
		if served != nil {
			served <- struct{}{}
		}
		if hold != nil {
			<-hold
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>ready</html>"))
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
}

// checksStepHandlers wires the checks step over st: the node under test dials the
// fixture server through the loopback outbound.
func checksStepHandlers(t *testing.T, st *store.Store, engine *checks.Engine, target string) StepHandlers {
	t.Helper()
	handlers := NewStepHandlers(StepOptions{
		Store:    st,
		Registry: providers.NewRegistry(),
		Checks:   engine,
		Outbound: func(string) (adapter.Outbound, bool) {
			return &loopbackOutbound{target: target}, true
		},
		Config: func() jobs.Config { return jobs.Config{Enabled: true}.Normalize() },
		Clock:  jobs.SystemClock{},
		Logf:   t.Logf,
	})
	if handlers.Checks == nil {
		t.Fatal("NewStepHandlers did not wire the checks step")
	}
	return handlers
}

// checksRequest seeds one running checks job selecting ruleID for nodeHash, with
// step 1 (egress) already completed.
func checksRequest(t *testing.T, st *store.Store, jobID, nodeHash, ruleID string) {
	t.Helper()
	requestJSON := `{"kind":"checks","scope":{"node_hashes":["` + nodeHash + `"]},"checks":["` + ruleID + `"]}`
	now := time.Now().UTC().UnixNano()
	if err := st.CreateJob(context.Background(), store.Job{
		ID: jobID, Kind: string(jobs.KindChecks), Status: store.JobRunning,
		RequestJSON: requestJSON, CreatedBy: jobs.CreatedByAdmin(), CreatedAtNs: now,
	}, []store.JobItem{{
		JobID: jobID, NodeHash: nodeHash, Status: store.ItemQueued, StepIndex: 1,
		NextRunAtNs: now, UpdatedAtNs: now,
	}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
}

// seedNodeEgress stores the egress observation the checks step records with its
// results.
func seedNodeEgress(t *testing.T, st *store.Store, nodeHash, ipv4 string) {
	t.Helper()
	if err := st.UpsertNodeEgress(context.Background(), store.NodeEgress{
		NodeHash: nodeHash, IPv4: ipv4,
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
}

// holdRuleSlot occupies the only concurrency slot of ruleID: a run of another
// node starts the rule and blocks inside the fixture server until the returned
// release function is called. It returns once the rule is provably inside the
// server, so the slot is held for real and not merely requested.
func holdRuleSlot(t *testing.T, engine *checks.Engine, ruleID, target string, served <-chan struct{}, hold chan struct{}) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A different NodeKey keeps this run out of the stripe lock of the node
		// under test, so it competes for the per-rule semaphore - which is the
		// resource that produces CHECK_BUSY.
		engine.Run(context.Background(), checks.RunRequest{
			NodeKey:  "slot-holder-node",
			Outbound: &loopbackOutbound{target: target},
			RuleIDs:  []string{ruleID},
		})
	}()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		close(hold)
		<-done
		t.Fatal("the slot holder never reached the rule target")
	}
	var once sync.Once
	return func() {
		once.Do(func() { close(hold) })
		<-done
	}
}

// TestChecksStepParksWithoutStoringUnrunRules pins the lossless half of the fix:
// a rule that never ran must not become a node_checks row, and the item must be
// parked rather than failed, so the rule keeps its chance on the next attempt.
func TestChecksStepParksWithoutStoringUnrunRules(t *testing.T) {
	st := openIntelStore(t)
	const (
		jobID    = "job-check-not-run"
		nodeHash = "node-check-not-run"
		ruleID   = "not_run_fixture"
		egressIP = "203.0.113.9"
	)
	seedNodeEgress(t, st, nodeHash, egressIP)
	checksRequest(t, st, jobID, nodeHash, ruleID)

	served := make(chan struct{}, 4)
	hold := make(chan struct{})
	server, hits := checksFixtureServer(t, served, hold)
	engine := checksFixtureEngine(t, ruleID, server.URL)
	engine.SetConcurrencyPerCheck(1)
	target := strings.TrimPrefix(server.URL, "http://")
	handlers := checksStepHandlers(t, st, engine, target)

	release := holdRuleSlot(t, engine, ruleID, target, served, hold)
	defer release()

	// The rule's only slot is taken by the other node, so this step can never
	// acquire it and the engine reports CHECK_BUSY once the context runs out.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	before := hits()
	result := handlers.Checks(ctx, jobID, nodeHash)

	if result.ErrorCode != "" {
		t.Fatalf("a rule that never ran must not fail the item: %q (%v)", result.ErrorCode, result.Summary)
	}
	if result.Skip {
		t.Fatalf("a rule that never ran must not skip the item: %v", result.Summary)
	}
	if result.DeferUntilNs <= 0 {
		t.Fatalf("the item must be parked for a retry, got DeferUntilNs=%d (%v)",
			result.DeferUntilNs, result.Summary)
	}
	if _, ok := result.Summary["skipped"]; ok {
		t.Fatalf("the item must not be reported as skipped: %v", result.Summary)
	}
	notRun, _ := result.Summary["checks_not_run"].([]string)
	if len(notRun) != 1 || notRun[0] != ruleID {
		t.Fatalf("checks_not_run = %v, want [%s]", result.Summary["checks_not_run"], ruleID)
	}
	if after := hits(); after != before {
		t.Fatalf("the rule was executed %d time(s) while its slot was held, want 0", after-before)
	}
	if row, ok, err := st.GetNodeCheck(context.Background(), nodeHash, ruleID); err != nil || ok {
		t.Fatalf("node_checks row for a rule that never ran: ok=%v row=%+v err=%v, want none", ok, row, err)
	}
}

// TestChecksStepStoresRealResultsAndSkipsThePark pins the other side: a rule that
// did run keeps its row and does not park the item.
func TestChecksStepStoresRealResultsAndSkipsThePark(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()
	const (
		jobID    = "job-check-ran"
		nodeHash = "node-check-ran"
		ruleID   = "ran_fixture"
		egressIP = "203.0.113.10"
	)
	seedNodeEgress(t, st, nodeHash, egressIP)
	checksRequest(t, st, jobID, nodeHash, ruleID)

	server, hits := checksFixtureServer(t, nil, nil)
	engine := checksFixtureEngine(t, ruleID, server.URL)
	handlers := checksStepHandlers(t, st, engine, strings.TrimPrefix(server.URL, "http://"))

	result := handlers.Checks(ctx, jobID, nodeHash)
	if result.ErrorCode != "" {
		t.Fatalf("Checks error = %s (%v)", result.ErrorCode, result.Summary)
	}
	if result.DeferUntilNs != 0 {
		t.Fatalf("a fully executed step must not park the item: %v", result.Summary)
	}
	if _, ok := result.Summary["checks_not_run"]; ok {
		t.Fatalf("no rule was held back, so nothing may be reported as not run: %v", result.Summary)
	}
	if stored, _ := result.Summary["checks_stored"].(int); stored != 1 {
		t.Fatalf("checks_stored = %v, want 1", result.Summary["checks_stored"])
	}
	if got := hits(); got != 1 {
		t.Fatalf("the rule target was asked %d times, want 1", got)
	}
	row, ok, err := st.GetNodeCheck(ctx, nodeHash, ruleID)
	if err != nil || !ok {
		t.Fatalf("the executed rule must leave a row: ok=%v err=%v", ok, err)
	}
	if row.Outcome != checks.OutcomeAvailable {
		t.Fatalf("row outcome = %q, want %q", row.Outcome, checks.OutcomeAvailable)
	}
	if row.EgressIP != egressIP {
		t.Fatalf("row egress = %q, want %q", row.EgressIP, egressIP)
	}
}

// TestChecksStepParkRoundTripsThroughTheStore guards the merge behaviour of the
// scheduler: jobs.Manager merges each step's summary into one map and stores it
// with the parked item, so the park must survive that round trip.
func TestChecksStepParkRoundTripsThroughTheStore(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()
	const (
		jobID    = "job-check-park"
		nodeHash = "node-check-park"
		ruleID   = "park_fixture"
		egressIP = "203.0.113.11"
	)
	seedNodeEgress(t, st, nodeHash, egressIP)
	checksRequest(t, st, jobID, nodeHash, ruleID)

	served := make(chan struct{}, 4)
	hold := make(chan struct{})
	server, _ := checksFixtureServer(t, served, hold)
	engine := checksFixtureEngine(t, ruleID, server.URL)
	engine.SetConcurrencyPerCheck(1)
	target := strings.TrimPrefix(server.URL, "http://")
	handlers := checksStepHandlers(t, st, engine, target)

	release := holdRuleSlot(t, engine, ruleID, target, served, hold)
	defer release()

	stepCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result := handlers.Checks(stepCtx, jobID, nodeHash)
	if result.DeferUntilNs <= 0 {
		t.Fatalf("want a park, got %v", result.Summary)
	}

	raw, err := json.Marshal(result.Summary)
	if err != nil {
		t.Fatalf("marshal the step summary: %v", err)
	}
	now := time.Now().UTC().UnixNano()
	if err := st.DeferJobItem(ctx, jobID, nodeHash, 1, now, string(raw), now); err != nil {
		t.Fatalf("DeferJobItem: %v", err)
	}
	parked, ok, err := st.GetJobItem(ctx, jobID, nodeHash)
	if err != nil || !ok {
		t.Fatalf("read the parked item: ok=%v err=%v", ok, err)
	}
	if parked.Status != store.ItemQueued {
		t.Fatalf("parked item status = %q, want %q", parked.Status, store.ItemQueued)
	}
	if parked.ErrorCode != "" {
		t.Fatalf("a park must not record an error: %q", parked.ErrorCode)
	}
	if !json.Valid([]byte(parked.ResultJSON)) {
		t.Fatalf("stored summary is not valid JSON: %q", parked.ResultJSON)
	}
	if !strings.Contains(parked.ResultJSON, ruleID) {
		t.Fatalf("stored summary %q lost the rule that still has to run", parked.ResultJSON)
	}
}
