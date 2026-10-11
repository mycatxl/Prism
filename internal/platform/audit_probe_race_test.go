package platform

import (
	"testing"

	"prism/internal/node"
)

// TestAudit_QualitySnapshotIsNotWrittenWhileTheViewIsRead pins the lock
// discipline of Platform.qualitySnapshot.
//
// SetQualitySnapshot writes the field, evaluateNode reads it under viewMu (via
// FullRebuild/NotifyDirty). The writers on the pool side (GlobalNodePool.
// instrumentQuality, SetQualitySnapshot and RegisterPlatform) do not hold viewMu,
// so a rebuild that re-installs the projection can run concurrently with a dirty
// event that evaluates a node - the field is an interface value, which is two
// words, and neither access is atomic.
//
// The probe drives the exact pair of paths: one goroutine keeps rebuilding the
// view (the read side), the other keeps re-installing the snapshot (the write
// side). Run with -race to observe the report; without -race the test still
// asserts that both paths ran.
func TestAudit_QualitySnapshotIsNotWrittenWhileTheViewIsRead(t *testing.T) {
	plat := NewPlatform("race", "Race", nil, nil)
	entry := &node.NodeEntry{}
	entry.Hash = node.Hash{1}
	getEntry := func(node.Hash) (*node.NodeEntry, bool) { return entry, true }
	poolRange := func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(entry.Hash, entry)
	}

	snaps := []QualitySnapshotReader{
		newFakeSnapshot(),
		newFakeSnapshot().set("198.51.100.1", lite()),
	}

	const rounds = 4000
	done := make(chan struct{})

	// The write side: what GlobalNodePool.instrumentQuality does on every
	// rebuild, and what SetQualitySnapshot does for every platform.
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			plat.SetQualitySnapshot(snaps[i%len(snaps)])
		}
	}()

	// The read side: the rebuild/dirty paths that read the field under viewMu.
	for i := 0; i < rounds; i++ {
		plat.FullRebuild(poolRange, nil, nil, nil)
		plat.NotifyDirty(entry.Hash, getEntry, nil, nil, nil)
	}
	<-done

	// The write is visible to the reader afterwards, which is the contract the
	// atomic store has to keep. (Whether the node itself is admitted is the
	// business of the criteria tests, not of this probe.)
	plat.SetQualitySnapshot(snaps[1])
	if got := plat.qualityReader(); got == nil {
		t.Fatal("the projection written by SetQualitySnapshot is not visible to the reader")
	}
	plat.SetQualitySnapshot(nil)
	if got := plat.qualityReader(); got != nil {
		t.Fatal("clearing the projection left a reader behind: the admission path would not fail closed")
	}
}
