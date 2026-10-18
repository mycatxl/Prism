// Package safego runs background work so that a panic inside it does not take
// the process down. A panic in a long-lived loop would otherwise terminate
// every endpoint, every tunnel and the management plane with it.
package safego

import (
	"log"
	"runtime/debug"
)

// Run calls fn and contains a panic: it is logged with its stack and swallowed,
// so the caller - usually one pass of a periodic loop - keeps going. A panic in
// one pass is a bug in that pass, not a reason to stop serving traffic.
func Run(name string, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("safego: recovered a panic in %s: %v\n%s", name, recovered, debug.Stack())
		}
	}()
	fn()
}

// Go runs fn on its own goroutine with the same containment. Defers inside fn
// still run during the unwind, so a `defer close(done)` or `defer wg.Done()`
// keeps its meaning for whoever waits on it.
func Go(name string, fn func()) {
	go Run(name, fn)
}
