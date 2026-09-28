package intel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// viaNodeTestClock is a clock a test can advance. A retry of step 4 has to land
// after the provider's QPS valve, otherwise the step parks the item instead of
// asking the source again.
type viaNodeTestClock struct{ now time.Time }

func (c *viaNodeTestClock) Now() time.Time { return c.now }

// Names of the triggers that make every evidence write fail.
const (
	blockEvidenceInsertTrigger = "prism_test_block_evidence_insert"
	blockEvidenceUpdateTrigger = "prism_test_block_evidence_update"
)

// blockEvidenceWrites makes the next evidence writes fail, deterministically.
//
// A trigger is the least invasive way to reach that state: it fails the real
// store.UpsertEvidence with a real error, so the production error path is
// exercised as it is in the field, while the read path (node_egress, the parked
// item summary) keeps working - which a closed or read-only store would not
// allow, and which the step needs. It also adds no test-only seam to the
// pipeline, unlike an injectable persistResult.
func blockEvidenceWrites(t *testing.T, st *store.Store) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TRIGGER ` + blockEvidenceInsertTrigger + ` BEFORE INSERT ON evidence
			BEGIN SELECT RAISE(ABORT, 'prism test: evidence writes are blocked'); END`,
		`CREATE TRIGGER ` + blockEvidenceUpdateTrigger + ` BEFORE UPDATE ON evidence
			BEGIN SELECT RAISE(ABORT, 'prism test: evidence writes are blocked'); END`,
	} {
		if _, err := st.DB().ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("install the evidence write blocker: %v", err)
		}
	}
}

// allowEvidenceWrites removes the blocker of blockEvidenceWrites.
func allowEvidenceWrites(t *testing.T, st *store.Store) {
	t.Helper()
	for _, name := range []string{blockEvidenceInsertTrigger, blockEvidenceUpdateTrigger} {
		if _, err := st.DB().ExecContext(context.Background(), `DROP TRIGGER `+name); err != nil {
			t.Fatalf("drop trigger %s: %v", name, err)
		}
	}
}

// summaryHasString reports whether one of the summary's string lists holds want.
func summaryHasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestViaNodeStepRetriesASourceWhoseEvidenceDidNotLand pins the hole between the
// two halves of step 4: a data source can answer cleanly and still leave no row
// in intel.db.
//
// via_node_answered is the only record of "this source has been asked", and
// answeredViaNodeSources skips exactly that set on the next attempt of the same
// item. Recording a source whose write failed therefore loses its evidence for
// the rest of the item's life: the item is scored with a hole in its evidence
// table while the summary claims the source replied.
//
// The write failure is deterministic (see blockEvidenceWrites). Reverting the
// fix - counting a source as answered on a clean lookup alone - turns the
// assertions after the first attempt red, and with them the retry assertion:
// ippure would be skipped instead of asked again.
func TestViaNodeStepRetriesASourceWhoseEvidenceDidNotLand(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()

	server, hits := countingProviderServer(t)
	serverAddr := strings.TrimPrefix(server.URL, "http://")
	nodeOutbound := &loopbackOutbound{target: serverAddr}
	registry := e2eRegistry(t, server.URL)

	hash := node.HashFromRawOptions([]byte(`{"type":"direct","tag":"prism-vianode-evidence"}`))
	nodeHash := hash.String()

	// Step 4 reads the node's addresses from node_egress.
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{
		NodeHash:     nodeHash,
		IPv4:         e2eEgressIP,
		V4ObservedNs: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	// One item sitting at step 4 with no previous summary.
	const jobID = "job-vianode-evidence"
	requestJSON := `{"kind":"intel","scope":{"node_hashes":["` + nodeHash + `"]}}`
	if err := st.CreateJob(ctx, store.Job{
		ID: jobID, Kind: string(jobs.KindIntel), Status: store.JobRunning,
		RequestJSON: requestJSON, Total: 1, CreatedBy: jobs.CreatedByAdmin(),
	}, []store.JobItem{{
		JobID: jobID, NodeHash: nodeHash, Status: store.ItemQueued, StepIndex: 3,
	}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	clock := &viaNodeTestClock{now: time.Now().UTC()}
	handlers := NewStepHandlers(StepOptions{
		Store:    st,
		Registry: registry,
		Outbound: func(string) (adapter.Outbound, bool) { return nodeOutbound, true },
		Config:   func() jobs.Config { return jobs.Config{Enabled: true}.Normalize() },
		Clock:    clock,
		Logf:     t.Logf,
	})
	if handlers.ViaNode == nil {
		t.Fatal("NewStepHandlers did not wire ViaNode")
	}

	// --- attempt 1: every lookup is clean, every evidence write is refused ---
	blockEvidenceWrites(t, st)
	beforeIPPure := hits("/ippure")
	first := handlers.ViaNode(ctx, jobID, nodeHash)
	if first.ErrorCode != "" {
		t.Fatalf("ViaNode error = %s (%v)", first.ErrorCode, first.Summary)
	}
	if asked := hits("/ippure") - beforeIPPure; asked == 0 {
		t.Fatal("ippure was never asked: this test needs its lookup to come back clean")
	}

	sources, _ := first.Summary["via_node_sources"].([]string)
	if !summaryHasString(sources, "ippure") {
		t.Fatalf("via_node_sources = %v, want it to list the source that was asked", sources)
	}
	answered, _ := first.Summary["via_node_answered"].([]string)
	if summaryHasString(answered, "ippure") {
		t.Fatalf("via_node_answered = %v, want ippure out of it: its evidence never reached intel.db", answered)
	}
	failedWrites, ok := first.Summary["via_node_evidence_failed"].(int)
	if !ok || failedWrites == 0 {
		t.Fatalf("via_node_evidence_failed = %v, want a count > 0 for the refused writes",
			first.Summary["via_node_evidence_failed"])
	}
	if row, ok, err := st.GetEvidence(ctx, e2eEgressIP, "ippure"); err != nil || ok {
		t.Fatalf("evidence row for ippure: ok=%v row=%+v err=%v, want none: the write was refused", ok, row, err)
	}

	// The job manager parks the item with the running summary
	// (jobs.Manager.deferItem -> store.DeferJobItem); that stored summary is
	// what the next attempt reads.
	stored, err := json.Marshal(first.Summary)
	if err != nil {
		t.Fatalf("marshal the step summary: %v", err)
	}
	if err := st.DeferJobItem(ctx, jobID, nodeHash, 3,
		clock.now.Add(time.Minute).UnixNano(), string(stored), clock.now.UnixNano()); err != nil {
		t.Fatalf("DeferJobItem: %v", err)
	}
	parked, ok, err := st.GetJobItem(ctx, jobID, nodeHash)
	if err != nil || !ok {
		t.Fatalf("read the parked item: ok=%v err=%v", ok, err)
	}
	if parked.Status != store.ItemQueued {
		t.Fatalf("parked item status = %q, want %q", parked.Status, store.ItemQueued)
	}

	// --- attempt 2: the store writes again -----------------------------------
	allowEvidenceWrites(t, st)
	clock.now = clock.now.Add(2 * time.Second) // past every provider's QPS valve
	beforeIPPure = hits("/ippure")
	second := handlers.ViaNode(ctx, jobID, nodeHash)
	if second.ErrorCode != "" {
		t.Fatalf("second ViaNode error = %s (%v)", second.ErrorCode, second.Summary)
	}
	if asked := hits("/ippure") - beforeIPPure; asked == 0 {
		t.Fatal("ippure was not asked again: a source whose evidence is missing must be retried, not skipped")
	}
	answered, _ = second.Summary["via_node_answered"].([]string)
	if !summaryHasString(answered, "ippure") {
		t.Fatalf("via_node_answered = %v, want ippure once its evidence is stored", answered)
	}
	row, ok, err := st.GetEvidence(ctx, e2eEgressIP, "ippure")
	if err != nil || !ok || row.Status != store.StatusOk {
		t.Fatalf("ippure evidence after the retry: ok=%v row=%+v err=%v", ok, row, err)
	}
}
