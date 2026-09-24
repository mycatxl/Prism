package publicsource

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// referenceTopK is the behaviour topNodes must reproduce exactly: materialise
// everything, de-duplicate by hash, sort the hashes and take the first k.
func referenceTopK(items [][2]string, k int) []hashEntry {
	unique := make(map[string]string, len(items))
	for i, item := range items {
		if _, exists := unique[item[0]]; exists {
			continue
		}
		unique[item[0]] = item[1]
		_ = i
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if k > 0 && len(keys) > k {
		keys = keys[:k]
	}
	out := make([]hashEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, hashEntry{hash: key, raw: json.RawMessage(unique[key])})
	}
	return out
}

func generateItems(n int, seed int64) [][2]string {
	rng := rand.New(rand.NewSource(seed))
	items := make([][2]string, 0, n)
	for i := 0; i < n; i++ {
		// 64-char lowercase hex, the shape node.Hash.Hex() produces.
		hash := fmt.Sprintf("%064x", rng.Uint64()<<32|uint64(rng.Intn(1<<16)))
		items = append(items, [2]string{hash, fmt.Sprintf(`{"type":"socks","server":"10.0.0.%d","server_port":%d}`, i%256, 1024+i)})
		// Re-add some entries to exercise de-duplication.
		if i%7 == 0 {
			items = append(items, [2]string{hash, `{"duplicate":true}`})
		}
	}
	return items
}

func assertSameEntries(t *testing.T, got, want []hashEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].hash != want[i].hash {
			t.Fatalf("entry %d hash = %q, want %q", i, got[i].hash, want[i].hash)
		}
		if string(got[i].raw) != string(want[i].raw) {
			t.Fatalf("entry %d raw = %s, want %s", i, got[i].raw, want[i].raw)
		}
	}
}

func TestTopNodesMatchesReferenceForBoundedLimits(t *testing.T) {
	items := generateItems(5000, 42)
	for _, k := range []int{1, 10, 999, 1000, 4999, 5000, 6000} {
		t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
			set := newTopNodes(k)
			for _, item := range items {
				set.Add(item[0], json.RawMessage(item[1]))
			}
			assertSameEntries(t, set.Sorted(), referenceTopK(items, k))
		})
	}
}

func TestTopNodesUnboundedKeepsEverything(t *testing.T) {
	items := generateItems(2000, 7)
	set := newTopNodes(0)
	for _, item := range items {
		set.Add(item[0], json.RawMessage(item[1]))
	}
	assertSameEntries(t, set.Sorted(), referenceTopK(items, 0))
	if set.Len() != len(set.Sorted()) {
		t.Fatalf("Len() = %d, Sorted() has %d", set.Len(), len(set.Sorted()))
	}
}

func TestTopNodesDeduplicates(t *testing.T) {
	set := newTopNodes(10)
	raw := json.RawMessage(`{"type":"http"}`)
	set.Add("bbbb", raw)
	set.Add("bbbb", json.RawMessage(`{"type":"socks"}`))
	set.Add("aaaa", raw)
	set.Add("aaaa", raw)

	entries := set.Sorted()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after de-duplication, got %d", len(entries))
	}
	if entries[0].hash != "aaaa" || entries[1].hash != "bbbb" {
		t.Fatalf("unexpected order: %#v", entries)
	}
	// The first occurrence wins, matching the previous map-based behaviour.
	if string(entries[1].raw) != `{"type":"http"}` {
		t.Fatalf("the later duplicate overwrote the first: %s", entries[1].raw)
	}
}

func TestTopNodesEvictsLargestAndNeverReadmits(t *testing.T) {
	set := newTopNodes(3)
	// Ascending hashes: only the three smallest may survive.
	for _, hash := range []string{"aa", "bb", "cc", "dd", "ee"} {
		set.Add(hash, json.RawMessage(`{}`))
	}
	entries := set.Sorted()
	want := []string{"aa", "bb", "cc"}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, hash := range want {
		if entries[i].hash != hash {
			t.Fatalf("entry %d = %q, want %q", i, entries[i].hash, hash)
		}
	}

	// A hash that was rejected earlier must stay rejected when offered again.
	set.Add("ee", json.RawMessage(`{}`))
	set.Add("zz", json.RawMessage(`{}`))
	for _, entry := range set.Sorted() {
		if entry.hash == "ee" || entry.hash == "zz" {
			t.Fatalf("a rejected hash was admitted later: %q", entry.hash)
		}
	}
}

func TestTopNodesConvergesOnTheGlobalMinimum(t *testing.T) {
	// Feeding hashes in descending order is the worst case for a bounded set:
	// every single insert has to evict.
	items := generateItems(3000, 99)
	sort.Slice(items, func(i, j int) bool { return items[i][0] > items[j][0] })

	set := newTopNodes(50)
	for _, item := range items {
		set.Add(item[0], json.RawMessage(item[1]))
	}
	assertSameEntries(t, set.Sorted(), referenceTopK(items, 50))
}

func TestTopNodesNegativeLimitIsUnbounded(t *testing.T) {
	set := newTopNodes(-5)
	items := generateItems(100, 1)
	for _, item := range items {
		set.Add(item[0], json.RawMessage(item[1]))
	}
	want := referenceTopK(items, 0)
	if set.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", set.Len(), len(want))
	}
}

func TestTopNodesNilAndEmptyAreSafe(t *testing.T) {
	var nilSet *topNodes
	if nilSet.Len() != 0 {
		t.Fatal("nil Len() must be 0")
	}
	if nilSet.Sorted() != nil {
		t.Fatal("nil Sorted() must be nil")
	}
	nilSet.Range(func(string, json.RawMessage) bool {
		t.Fatal("nil Range() must not call back")
		return true
	})

	empty := newTopNodes(10)
	if empty.Len() != 0 {
		t.Fatal("empty Len() must be 0")
	}
	if len(empty.Sorted()) != 0 {
		t.Fatal("empty Sorted() must be empty")
	}
	empty.Range(func(string, json.RawMessage) bool {
		t.Fatal("empty Range() must not call back")
		return true
	})
}

func TestTopNodesRangeVisitsEveryRetainedEntry(t *testing.T) {
	set := newTopNodes(20)
	items := generateItems(500, 5)
	for _, item := range items {
		set.Add(item[0], json.RawMessage(item[1]))
	}
	seen := make(map[string]int)
	set.Range(func(hash string, _ json.RawMessage) bool {
		seen[hash]++
		return true
	})
	if len(seen) != set.Len() {
		t.Fatalf("Range visited %d distinct hashes, Len() = %d", len(seen), set.Len())
	}
	for hash, count := range seen {
		if count != 1 {
			t.Fatalf("hash %q visited %d times", hash, count)
		}
	}
}

func TestTopNodesRangeStopsEarly(t *testing.T) {
	set := newTopNodes(50)
	items := generateItems(200, 11)
	for _, item := range items {
		set.Add(item[0], json.RawMessage(item[1]))
	}
	visited := 0
	set.Range(func(string, json.RawMessage) bool {
		visited++
		return visited < 3
	})
	if visited != 3 {
		t.Fatalf("Range honoured the stop signal after %d visits, want 3", visited)
	}
}
