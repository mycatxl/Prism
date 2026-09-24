package node

import (
	"testing"
	"time"
)

func TestLatencyTable_FirstRecord(t *testing.T) {
	lt := NewLatencyTable(16)

	wasEmpty := lt.Update("example.com", 100*time.Millisecond, 30*time.Second)
	if !wasEmpty {
		t.Fatal("should report wasEmpty on first-ever record")
	}

	stats, ok := lt.GetDomainStats("example.com")
	if !ok {
		t.Fatal("should find stats for example.com")
	}
	if stats.Ewma != 100*time.Millisecond {
		t.Fatalf("first Ewma should equal raw latency, got %v", stats.Ewma)
	}
}

func TestLatencyTable_SecondRecord_NotWasEmpty(t *testing.T) {
	lt := NewLatencyTable(16)

	lt.Update("example.com", 100*time.Millisecond, 30*time.Second)
	wasEmpty := lt.Update("example.com", 200*time.Millisecond, 30*time.Second)
	if wasEmpty {
		t.Fatal("should not report wasEmpty on second record")
	}
}

func TestLatencyTable_TDEWMA_Decay(t *testing.T) {
	lt := NewLatencyTable(16)

	// Preload with known stats.
	base := time.Now().Add(-10 * time.Second)
	lt.LoadEntry("example.com", DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: base,
	})

	// Update with a much higher value.
	lt.Update("example.com", 500*time.Millisecond, 30*time.Second)

	stats, _ := lt.GetDomainStats("example.com")
	// New EWMA should be between old (100ms) and new (500ms).
	if stats.Ewma <= 100*time.Millisecond || stats.Ewma >= 500*time.Millisecond {
		t.Fatalf("EWMA should be between 100ms and 500ms, got %v", stats.Ewma)
	}
}

func TestLatencyTable_BoundedEviction_RegularLRU(t *testing.T) {
	lt := NewLatencyTable(2)
	lt.Update("a.com", 10*time.Millisecond, 30*time.Second)
	lt.Update("b.com", 20*time.Millisecond, 30*time.Second)
	lt.Update("c.com", 30*time.Millisecond, 30*time.Second)

	if _, ok := lt.GetDomainStats("a.com"); ok {
		t.Fatal("expected oldest regular entry to be evicted")
	}
	if _, ok := lt.GetDomainStats("b.com"); !ok {
		t.Fatal("expected b.com to remain in regular LRU")
	}
	if _, ok := lt.GetDomainStats("c.com"); !ok {
		t.Fatal("expected c.com to remain in regular LRU")
	}
}

func TestLatencyTable_Get_TouchesRegularLRU(t *testing.T) {
	lt := NewLatencyTable(2)
	lt.Update("a.com", 10*time.Millisecond, 30*time.Second)
	lt.Update("b.com", 20*time.Millisecond, 30*time.Second)

	// Read touch is throttled; wait over the minimum interval first.
	time.Sleep(latencyReadTouchMinInterval + 20*time.Millisecond)
	if _, ok := lt.GetDomainStats("a.com"); !ok {
		t.Fatal("expected a.com to exist")
	}
	lt.Update("c.com", 30*time.Millisecond, 30*time.Second)

	if _, ok := lt.GetDomainStats("b.com"); ok {
		t.Fatal("expected b.com to be evicted after read-touch on a.com")
	}
	if _, ok := lt.GetDomainStats("a.com"); !ok {
		t.Fatal("expected a.com to stay due to read-touch")
	}
	if _, ok := lt.GetDomainStats("c.com"); !ok {
		t.Fatal("expected c.com to exist")
	}
}

func TestLatencyTable_AuthorityResident(t *testing.T) {
	lt := NewLatencyTable(1)

	lt.UpdateClassified("gstatic.com", 5*time.Millisecond, 30*time.Second, true)
	lt.Update("a.com", 10*time.Millisecond, 30*time.Second)
	lt.Update("b.com", 20*time.Millisecond, 30*time.Second)

	if _, ok := lt.GetDomainStats("gstatic.com"); !ok {
		t.Fatal("authority domain should stay resident")
	}
	if _, ok := lt.GetDomainStats("a.com"); ok {
		t.Fatal("oldest regular entry should be evicted")
	}
	if _, ok := lt.GetDomainStats("b.com"); !ok {
		t.Fatal("latest regular entry should remain")
	}
}

func TestLatencyTable_ClassifiedUpdate_MigratesPartitions(t *testing.T) {
	lt := NewLatencyTable(1)

	lt.UpdateClassified("x.com", 10*time.Millisecond, 30*time.Second, true)
	lt.UpdateClassified("x.com", 20*time.Millisecond, 30*time.Second, false) // authority -> regular
	lt.Update("y.com", 30*time.Millisecond, 30*time.Second)                  // evicts oldest regular (x.com)

	if _, ok := lt.GetDomainStats("x.com"); ok {
		t.Fatal("x.com should be evicted after migrating to regular partition")
	}
	if _, ok := lt.GetDomainStats("y.com"); !ok {
		t.Fatal("y.com should remain in regular partition")
	}
}

func TestLatencyTable_Range(t *testing.T) {
	lt := NewLatencyTable(16)

	lt.Update("a.com", 10*time.Millisecond, 30*time.Second)
	lt.Update("b.com", 20*time.Millisecond, 30*time.Second)

	count := 0
	lt.Range(func(domain string, stats DomainLatencyStats) bool {
		count++
		return true
	})
	if count != 2 {
		t.Fatalf("expected 2 entries in Range, got %d", count)
	}
}

func TestLatencyTable_NotFound(t *testing.T) {
	lt := NewLatencyTable(16)

	_, ok := lt.GetDomainStats("nonexistent.com")
	if ok {
		t.Fatal("should not find stats for nonexistent domain")
	}
}

func TestLatencyTable_LoadEntry(t *testing.T) {
	lt := NewLatencyTable(16)
	now := time.Now()

	lt.LoadEntry("test.com", DomainLatencyStats{
		Ewma:        50 * time.Millisecond,
		LastUpdated: now,
	})

	stats, ok := lt.GetDomainStats("test.com")
	if !ok {
		t.Fatal("should find loaded entry")
	}
	if stats.Ewma != 50*time.Millisecond {
		t.Fatalf("LoadEntry should preserve exact Ewma, got %v", stats.Ewma)
	}
}

func TestAverageEWMAForDomainsMs(t *testing.T) {
	entry := NewNodeEntry(HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1","port":443}`)), nil, time.Now(), 16)
	entry.LatencyTable.LoadEntry("cloudflare.com", DomainLatencyStats{
		Ewma:        40 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("github.com", DomainLatencyStats{
		Ewma:        60 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("example.com", DomainLatencyStats{
		Ewma:        10 * time.Millisecond,
		LastUpdated: time.Now(),
	})

	avg, ok := AverageEWMAForDomainsMs(entry, []string{"cloudflare.com", "github.com", "gstatic.com"})
	if !ok {
		t.Fatal("expected average to be available")
	}
	if avg != 50 {
		t.Fatalf("average ms: got %v, want 50", avg)
	}
}

func TestAverageEWMAForDomainsMs_NoMatches(t *testing.T) {
	entry := NewNodeEntry(HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1","port":443}`)), nil, time.Now(), 16)
	entry.LatencyTable.LoadEntry("cloudflare.com", DomainLatencyStats{
		Ewma:        40 * time.Millisecond,
		LastUpdated: time.Now(),
	})

	if _, ok := AverageEWMAForDomainsMs(entry, []string{"github.com"}); ok {
		t.Fatal("expected no average when no domains match")
	}
}
