package safego

import (
	"sync/atomic"
	"testing"
)

func TestPanicObserverReceivesNoPanicPayloadAndCanBeRemoved(t *testing.T) {
	var count atomic.Int32
	restore := SetPanicObserver(func(name string) {
		if name == "fixture pool task" || name == "fixture after removal" {
			count.Add(1)
		}
	})
	defer restore()
	Run("fixture pool task", func() { panic("private fixture payload") })
	Run("fixture normal task", func() {})
	if count.Load() != 1 {
		t.Fatal("panic observer was not called exactly once")
	}
	restore()
	Run("fixture after removal", func() { panic("fixture") })
	if count.Load() != 1 {
		t.Fatal("removed observer was still called")
	}
}
