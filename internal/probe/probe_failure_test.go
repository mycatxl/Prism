package probe

import (
	"errors"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/topology"
)

// newProbeFailureTestPool builds a pool with one buildable node that has an
// outbound stored, mirroring the setup of the existing probe tests.
func newProbeFailureTestPool(t *testing.T, raw string) (*topology.GlobalNodePool, node.Hash, *node.NodeEntry) {
	t.Helper()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})
	hash := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(hash, []byte(raw), "sub1")
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)
	return pool, hash, entry
}

func TestProbeEgress_FetchFailureRecordsProbeFailureClass(t *testing.T) {
	pool, hash, entry := newProbeFailureTestPool(t, `{"type":"probe-err-egress"}`)
	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("dial tcp 198.51.100.7:443: connect: connection refused")
		},
	})

	mgr.probeEgress(hash, entry)

	class, detail, at := entry.GetProbeFailure()
	if class != node.ProbeErrorRefused {
		t.Fatalf("class: got %q, want %q", class, node.ProbeErrorRefused)
	}
	want := "dial tcp 198.51.100.7:443: connect: connection refused"
	if detail != want {
		t.Fatalf("detail: got %q, want %q", detail, want)
	}
	if at.IsZero() {
		t.Fatal("probe failure timestamp should be non-zero")
	}
	if entry.FailureCount.Load() != 1 {
		t.Fatalf("FailureCount: got %d, want 1", entry.FailureCount.Load())
	}

	// A subsequent successful probe clears the recorded failure.
	mgr.fetcher = func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
		return []byte("fl=1\nip=203.0.113.9\nloc=DE\nts=1"), 5 * time.Millisecond, nil
	}
	mgr.probeEgress(hash, entry)
	if class, detail, at := entry.GetProbeFailure(); class != node.ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("success should clear the probe failure, got (%q, %q, %v)", class, detail, at)
	}
}

func TestProbeEgress_ParseFailureRecordsParseFailed(t *testing.T) {
	pool, hash, entry := newProbeFailureTestPool(t, `{"type":"probe-err-parse"}`)
	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
			return []byte("<html>not a trace response</html>"), 7 * time.Millisecond, nil
		},
	})

	mgr.probeEgress(hash, entry)

	class, detail, at := entry.GetProbeFailure()
	if class != node.ProbeErrorParse {
		t.Fatalf("class: got %q, want %q", class, node.ProbeErrorParse)
	}
	if detail == "" {
		t.Fatal("parse failure detail should not be empty")
	}
	if at.IsZero() {
		t.Fatal("probe failure timestamp should be non-zero")
	}
	// The fetch itself succeeded, so the parse failure is the first counted
	// failure: this path used to record neither a count nor a reason.
	if entry.FailureCount.Load() != 1 {
		t.Fatalf("FailureCount: got %d, want 1", entry.FailureCount.Load())
	}
}

func TestProbeLatency_FetchFailureRecordsProbeFailureClass(t *testing.T) {
	pool, hash, entry := newProbeFailureTestPool(t, `{"type":"probe-err-latency"}`)
	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("context deadline exceeded (Client.Timeout exceeded)")
		},
	})

	mgr.probeLatency(hash, entry, "https://example.invalid/generate_204")

	class, detail, at := entry.GetProbeFailure()
	if class != node.ProbeErrorTimeout {
		t.Fatalf("class: got %q, want %q", class, node.ProbeErrorTimeout)
	}
	if detail == "" {
		t.Fatal("failure detail should not be empty")
	}
	if at.IsZero() {
		t.Fatal("probe failure timestamp should be non-zero")
	}
}
