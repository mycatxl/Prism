package state

import (
	"testing"
	"time"

	"prism/internal/model"
)

func TestCacheFlushReportsFailureAndRetainsDirtyEntries(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.MarkNodeStatic("fixture-node")
	if err := engine.CacheRepo.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := NewCacheFlushWorker(engine, CacheReaders{ReadNodeStatic: func(hash string) *model.NodeStatic { return &model.NodeStatic{Hash: hash, RawOptions: []byte(`{}`)} }}, func() int { return 1 }, func() time.Duration { return time.Minute }, time.Second)
	var reported bool
	w.OnError = func(err error) { reported = err != nil }
	w.doFlush()
	if !reported || engine.DirtyCount() != 1 {
		t.Fatal("failed cache flush must notify and preserve dirty entries")
	}
}
