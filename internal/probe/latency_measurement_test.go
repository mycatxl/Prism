package probe

import (
	"strings"
	"testing"
	"time"

	"prism/internal/node"
)

func TestSuccessfulHTTPWithoutLatencyIsNotMeasurementSuccess(t *testing.T) {
	pool, hash, entry := newProbeFailureTestPool(t, `{"type":"zero-latency"}`)
	mgr := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) { return []byte("ok"), 0, nil }})
	err := mgr.performLatencyProbe(hash, "http://example.test/probe")
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatal("missing TLS measurement must return actionable error")
	}
	if entry.LatencyTable.Size() != 0 {
		t.Fatal("zero latency became evidence")
	}
}
