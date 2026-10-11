package platform

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
	"time"

	"prism/internal/node"
)

// rebuildProbePool builds n fully routable nodes with distinct hashes so the
// same platform can be rebuilt with generations of different sizes.
func rebuildProbePool(t *testing.T, prefix string, n int) ([]node.Hash, map[node.Hash]*node.NodeEntry) {
	t.Helper()
	hashes := make([]node.Hash, 0, n)
	entries := make(map[node.Hash]*node.NodeEntry, n)
	for i := 0; i < n; i++ {
		h := makeHash(fmt.Sprintf(`{"type":"ss","rebuild_probe":"%s","n":%d}`, prefix, i))
		hashes = append(hashes, h)
		entries[h] = makeFullyRoutableEntry(h, "sub1")
	}
	return hashes, entries
}

// rebuildWith runs one FullRebuild over the given generation and calls observe
// once per node — i.e. while the rebuild is still running.
func rebuildWith(
	plat *Platform,
	hashes []node.Hash,
	entries map[node.Hash]*node.NodeEntry,
	observe func(),
) {
	plat.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		for _, h := range hashes {
			observe()
			if !fn(h, entries[h]) {
				return
			}
		}
		observe()
	}, alwaysLookup, usGeoLookup, nil)
}

// TestPlatform_FullRebuild_NeverExposesEmptyView pins the publication contract
// of the routable view (audit 3.1 #9).
//
// The read path (View().RandomPick / Range / Contains, see
// internal/routing/random.go) does not take viewMu, so a rebuild that mutated
// the published view in place would hand those readers an empty view: they
// would observe size 0, RandomPick would report "nothing routable" and the data
// plane would answer 503 for the duration of the rebuild.
//
// The probe reads View().Size() from inside the poolRange callback, i.e. while
// the rebuild runs, and requires every observation to be either the previous
// generation's size or the next generation's size. This is deterministic: no
// timing luck is involved, so reverting FullRebuild to an in-place
// Clear+refill must fail it every time.
func TestPlatform_FullRebuild_NeverExposesEmptyView(t *testing.T) {
	plat := NewPlatform("p-rebuild", "RebuildProbe", nil, nil)

	oldHashes, oldEntries := rebuildProbePool(t, "old", 4)
	newHashes, newEntries := rebuildProbePool(t, "new", 2)
	growHashes, growEntries := rebuildProbePool(t, "grow", 5)

	// Generation 1: 4 nodes, no observation needed.
	rebuildWith(plat, oldHashes, oldEntries, func() {})
	if got := plat.View().Size(); got != len(oldHashes) {
		t.Fatalf("setup: view size = %d, want %d", got, len(oldHashes))
	}

	// Generation 2 shrinks 4 -> 2. While the rebuild runs the published view
	// must be the old generation (4) or the new one (2); the intermediate
	// counts 0/1 are exactly what an in-place Clear+refill exposes.
	oldSize, newSize := len(oldHashes), len(newHashes)
	var seen []int
	rebuildWith(plat, newHashes, newEntries, func() {
		seen = append(seen, plat.View().Size())
	})
	if len(seen) == 0 {
		t.Fatal("probe observed nothing during FullRebuild")
	}
	for i, got := range seen {
		if got != oldSize && got != newSize {
			t.Fatalf("observation %d during FullRebuild: View().Size() = %d, want %d (previous generation) or %d (next generation); an intermediate value means a reader without viewMu sees an incomplete view (audit 3.1 #9)", i, got, oldSize, newSize)
		}
	}
	if got := plat.View().Size(); got != newSize {
		t.Fatalf("after FullRebuild: view size = %d, want %d", got, newSize)
	}
	if plat.View().Contains(oldHashes[0]) {
		t.Fatal("the previous generation is still visible after the rebuild published the next one")
	}

	// Generation 3 grows 2 -> 5, same contract in the other direction.
	oldSize, newSize = len(newHashes), len(growHashes)
	seen = seen[:0]
	rebuildWith(plat, growHashes, growEntries, func() {
		seen = append(seen, plat.View().Size())
	})
	if len(seen) == 0 {
		t.Fatal("probe observed nothing during FullRebuild")
	}
	for i, got := range seen {
		if got != oldSize && got != newSize {
			t.Fatalf("observation %d during FullRebuild: View().Size() = %d, want %d (previous generation) or %d (next generation)", i, got, oldSize, newSize)
		}
	}
	if got := plat.View().Size(); got != newSize {
		t.Fatalf("after FullRebuild: view size = %d, want %d", got, newSize)
	}
}

// TestPlatform_FullRebuild_ConcurrentRandomPickNeverFails drives the exact
// production pair: the data plane's reader (View() + RandomPick, no viewMu) vs
// FullRebuild. As long as the pool holds routable nodes, RandomPick must never
// report "nothing routable" — that report is what internal/routing turns into a
// 503. Run with -race.
func TestPlatform_FullRebuild_ConcurrentRandomPickNeverFails(t *testing.T) {
	plat := NewPlatform("p-rebuild-race", "RebuildRace", nil, nil)
	hashes, entries := rebuildProbePool(t, "race", 64)

	// Seed the view: from here on every rebuild replaces a non-empty
	// generation with a non-empty one, which is the production shape after the
	// startup rebuild.
	rebuildWith(plat, hashes, entries, func() {})
	if got := plat.View().Size(); got != len(hashes) {
		t.Fatalf("setup: view size = %d, want %d", got, len(hashes))
	}

	const rebuildRounds = 300
	stop := make(chan struct{})
	var wg sync.WaitGroup

	errCh := make(chan string, 1)
	report := func(msg string) {
		select {
		case errCh <- msg:
		default:
		}
	}

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed+1))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if i%64 == 0 {
					runtime.Gosched()
				}
				view := plat.View()
				if view.Size() == 0 {
					report("View().Size() == 0 while the pool still holds routable nodes")
					return
				}
				h, ok := view.RandomPick(rng)
				if !ok || h == node.Zero {
					report(fmt.Sprintf("RandomPick returned (%s, %v) while the pool still holds routable nodes", h.Hex(), ok))
					return
				}
			}
		}(uint64(r) + 1)
	}

	for i := 0; i < rebuildRounds; i++ {
		rebuildWith(plat, hashes, entries, func() { runtime.Gosched() })
		select {
		case msg := <-errCh:
			close(stop)
			wg.Wait()
			t.Fatalf("%s (rebuild round %d of %d)", msg, i, rebuildRounds)
		default:
		}
	}
	close(stop)
	wg.Wait()

	select {
	case msg := <-errCh:
		t.Fatal(msg)
	default:
	}
	if got := plat.View().Size(); got != len(hashes) {
		t.Fatalf("after %d rebuilds: view size = %d, want %d", rebuildRounds, got, len(hashes))
	}
}

// TestPlatform_NotifyDirty_MutatesThePublishedGeneration pins that NotifyDirty
// keeps its in-place add/remove semantics and works on the generation the last
// FullRebuild published (a dirty event must never land on a stale view).
func TestPlatform_NotifyDirty_MutatesThePublishedGeneration(t *testing.T) {
	plat := NewPlatform("p-dirty", "DirtyProbe", nil, nil)
	baseHashes, baseEntries := rebuildProbePool(t, "base", 2)
	extraHashes, extraEntries := rebuildProbePool(t, "extra", 1)
	extra := extraHashes[0]

	rebuildWith(plat, baseHashes, baseEntries, func() {})
	if got := plat.View().Size(); got != len(baseHashes) {
		t.Fatalf("setup: view size = %d, want %d", got, len(baseHashes))
	}

	store := map[node.Hash]*node.NodeEntry{extra: extraEntries[extra]}
	getEntry := func(h node.Hash) (*node.NodeEntry, bool) {
		e, ok := store[h]
		return e, ok
	}

	// Dirty add lands on the published generation.
	plat.NotifyDirty(extra, getEntry, alwaysLookup, usGeoLookup, nil)
	if got := plat.View().Size(); got != len(baseHashes)+1 {
		t.Fatalf("after dirty add: view size = %d, want %d", got, len(baseHashes)+1)
	}
	if !plat.View().Contains(extra) {
		t.Fatal("dirty add did not reach the published generation")
	}

	// Circuit-break it, dirty remove must reach the same generation.
	extraEntries[extra].CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(extra, getEntry, alwaysLookup, usGeoLookup, nil)
	if got := plat.View().Size(); got != len(baseHashes) {
		t.Fatalf("after dirty remove: view size = %d, want %d", got, len(baseHashes))
	}
	if plat.View().Contains(extra) {
		t.Fatal("dirty remove did not reach the published generation")
	}

	// A later rebuild replaces the generation wholesale.
	rebuildWith(plat, baseHashes, baseEntries, func() {})
	if got := plat.View().Size(); got != len(baseHashes) {
		t.Fatalf("after rebuild: view size = %d, want %d", got, len(baseHashes))
	}
}
