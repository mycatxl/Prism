// Package testutil holds the test-only helpers the other packages share:
// the memory-accounting helpers, the no-op outbound and the dial-capable
// stub outbound builder.
package testutil

import "runtime"

// AllocDeltaMB returns the net live-heap growth between two memory snapshots in
// MiB, as a signed value.
//
// The subtraction is done in signed arithmetic on purpose: runtime.MemStats.Alloc
// is a uint64, and a capacity test that calls runtime.GC() between the two
// ReadMemStats calls can legitimately observe after.Alloc < before.Alloc.
// Computing `after.Alloc - before.Alloc` on uint64 wraps around to roughly 2^64,
// and dividing that by 1024*1024 prints a nonsense figure such as
// "Alloc=17592186044400.12MB" in the capacity report. Casting to int64 first
// makes a shrink report a small negative number instead of a fake huge one.
//
// A negative result is expected when the measured operation allocated less than
// the garbage the second GC reclaimed; see ReadMemStatsStable for a settled
// baseline. Prefer TotalAllocDeltaMB when the question is "how much did this
// allocate" rather than "how much is still live".
func AllocDeltaMB(before, after runtime.MemStats) float64 {
	return float64(int64(after.Alloc)-int64(before.Alloc)) / 1024 / 1024
}

// TotalAllocDeltaMB returns the allocation volume between two snapshots in MiB.
//
// TotalAlloc is cumulative and monotonic (it counts every byte ever allocated,
// including garbage), so it never underflows and is not sensitive to when the
// garbage collector happened to run. For a capacity report this is the honest
// "cost of this operation" figure.
func TotalAllocDeltaMB(before, after runtime.MemStats) float64 {
	return float64(int64(after.TotalAlloc)-int64(before.TotalAlloc)) / 1024 / 1024
}

// ReadMemStatsStable takes a settled heap snapshot.
//
// A single runtime.GC() can leave unreclaimed garbage in Alloc, so a baseline
// read right after it may include memory that the next GC will free — which is
// what makes a capacity test report a negative delta for an operation that
// plainly allocated. Two collections in a row settle the heap closely enough for
// the figure to be comparable between runs.
func ReadMemStatsStable(stats *runtime.MemStats) {
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(stats)
}
