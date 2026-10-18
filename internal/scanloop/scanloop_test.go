package scanloop

import (
	"sync/atomic"
	"testing"
	"time"
)

// A pass that panics must end that pass only: the loop is a long-lived scanner
// and the next tick has to keep running. Before the pass was guarded, this
// panic took the process down.
func TestRunSurvivesAPanickingPass(t *testing.T) {
	stopCh := make(chan struct{})
	defer close(stopCh)

	var passes atomic.Int32
	done := make(chan struct{})
	var once atomic.Bool

	go Run(stopCh, time.Millisecond, 0, func() {
		n := passes.Add(1)
		if n == 1 {
			panic("boom in a scan pass")
		}
		if n >= 3 && once.CompareAndSwap(false, true) {
			close(done)
		}
	})

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the loop stopped after the panicking pass: %d pass(es) ran", passes.Load())
	}
}
