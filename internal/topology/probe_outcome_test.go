package topology

import (
	"fmt"
	"sync"
	"testing"

	"prism/internal/node"
)

func TestRecordOutcome_FailureStoresClassAndDetail(t *testing.T) {
	pool, subMgr := newHealthTestPool(3)
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"outcome-store"}`)
	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("entry not found")
	}

	pool.RecordOutcome(h, false, node.ProbeErrorTimeout, "i/o timeout")

	class, detail, at := entry.GetProbeFailure()
	if class != node.ProbeErrorTimeout {
		t.Fatalf("class: got %q, want %q", class, node.ProbeErrorTimeout)
	}
	if detail != "i/o timeout" {
		t.Fatalf("detail: got %q, want %q", detail, "i/o timeout")
	}
	if at.IsZero() {
		t.Fatal("probe failure timestamp should be non-zero")
	}
	// Health semantics are unchanged: the failure still counts.
	if entry.FailureCount.Load() != 1 {
		t.Fatalf("FailureCount: got %d, want 1", entry.FailureCount.Load())
	}

	// A later failure with another class replaces the record.
	pool.RecordOutcome(h, false, node.ProbeErrorRefused, "connection refused")
	if class, detail, _ := entry.GetProbeFailure(); class != node.ProbeErrorRefused || detail != "connection refused" {
		t.Fatalf("replaced record: got (%q, %q)", class, detail)
	}

	// Success clears the probe failure and the health counters.
	pool.RecordOutcome(h, true, node.ProbeErrorNone, "")
	if class, detail, at := entry.GetProbeFailure(); class != node.ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("probe failure should be cleared on success, got (%q, %q, %v)", class, detail, at)
	}
	if entry.FailureCount.Load() != 0 {
		t.Fatalf("FailureCount: got %d, want 0 after success", entry.FailureCount.Load())
	}
}

func TestRecordOutcome_UnknownFailureDoesNotInventClass(t *testing.T) {
	pool, subMgr := newHealthTestPool(3)
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"outcome-unknown"}`)
	entry, _ := pool.GetEntry(h)

	pool.RecordOutcome(h, false, node.ProbeErrorNone, "")

	if class, detail, at := entry.GetProbeFailure(); class != node.ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("no class means no recorded failure, got (%q, %q, %v)", class, detail, at)
	}
	if entry.FailureCount.Load() != 1 {
		t.Fatalf("FailureCount: got %d, want 1", entry.FailureCount.Load())
	}
}

func TestRecordOutcome_LogsOnlyOnClassChangeAndRecovery(t *testing.T) {
	pool, subMgr := newHealthTestPool(10)

	var mu sync.Mutex
	var lines []string
	pool.SetLogf(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"outcome-log"}`)

	pool.RecordOutcome(h, false, node.ProbeErrorTimeout, "timeout one")
	pool.RecordOutcome(h, false, node.ProbeErrorTimeout, "timeout two") // same class: no log
	pool.RecordOutcome(h, false, node.ProbeErrorRefused, "refused")     // class change: log
	pool.RecordOutcome(h, true, node.ProbeErrorNone, "")                // recovery: log
	pool.RecordOutcome(h, true, node.ProbeErrorNone, "")                // already healthy: no log

	want := []string{
		fmt.Sprintf("[topology] node probe failed: hash=%s class=%s detail=%s", h.Hex(), node.ProbeErrorTimeout, "timeout one"),
		fmt.Sprintf("[topology] node probe failed: hash=%s class=%s detail=%s", h.Hex(), node.ProbeErrorRefused, "refused"),
		fmt.Sprintf("[topology] node probe recovered: hash=%s", h.Hex()),
	}

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()

	if len(got) != len(want) {
		t.Fatalf("log lines: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRecordResult_PreservesOriginalSemantics pins the behavior of the
// RecordResult wrapper after it started delegating to RecordOutcome.
func TestRecordResult_PreservesOriginalSemantics(t *testing.T) {
	pool, subMgr := newHealthTestPool(2)
	sub := subMgr.Lookup("s1")
	h := addTestNode(pool, sub, `{"type":"ss","n":"result-wrapper"}`)
	entry, _ := pool.GetEntry(h)

	// A new node starts circuit-open; a success brings it healthy.
	pool.RecordResult(h, true)
	if entry.IsCircuitOpen() {
		t.Fatal("node should recover after first success")
	}

	pool.RecordResult(h, false)
	pool.RecordResult(h, false)
	if !entry.IsCircuitOpen() {
		t.Fatal("circuit should open at MaxConsecutiveFailures")
	}
	if entry.FailureCount.Load() != 2 {
		t.Fatalf("FailureCount: got %d, want 2", entry.FailureCount.Load())
	}
	// RecordResult carries no classified reason and must not fabricate one.
	if class, detail, at := entry.GetProbeFailure(); class != node.ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("plain RecordResult must not record a probe failure, got (%q, %q, %v)", class, detail, at)
	}

	pool.RecordResult(h, true)
	if entry.FailureCount.Load() != 0 || entry.IsCircuitOpen() {
		t.Fatal("success should reset failure count and circuit state")
	}
}
