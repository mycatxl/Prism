package node

import (
	"testing"
	"time"
)

func TestReferenceLatencyPreservesFractionalMilliseconds(t *testing.T) {
	entry := NewNodeEntry(HashFromRawOptions([]byte(`{"type":"http"}`)), nil, time.Now(), 16)
	entry.LatencyTable.LoadEntry("a.example", DomainLatencyStats{Ewma: 250 * time.Microsecond, LastUpdated: time.Now()})
	if latency, ok := AverageEWMAForDomainsMs(entry, []string{"a.example"}); !ok || latency != 0.25 {
		t.Fatalf("reference latency = %g ms, want 0.25", latency)
	}
	entry.LatencyTable.LoadEntry("a.example", DomainLatencyStats{Ewma: 1250 * time.Microsecond, LastUpdated: time.Now()})
	entry.LatencyTable.LoadEntry("b.example", DomainLatencyStats{Ewma: 1750 * time.Microsecond, LastUpdated: time.Now()})
	if latency, ok := AverageEWMAForDomainsMs(entry, []string{"a.example", "b.example"}); !ok || latency != 1.5 {
		t.Fatalf("reference average = %g ms, want 1.5", latency)
	}
}
