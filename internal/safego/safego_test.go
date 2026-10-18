package safego

import (
	"sync"
	"testing"
)

// A panic inside the guarded function must not escape: the caller is a
// long-lived loop, and an escape here ends the process.
func TestRunContainsAPanic(t *testing.T) {
	reached := false
	Run("a test pass", func() {
		reached = true
		panic("boom in a background pass")
	})
	if !reached {
		t.Fatal("Run never called the function")
	}
}

// The defer inside the guarded function is what closes the done channel or
// releases the wait group, so it has to run during the unwind.
func TestRunStillRunsTheDefersInside(t *testing.T) {
	var mu sync.Mutex
	order := make([]string, 0, 2)
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	Run("a test pass", func() {
		defer record("defer")
		record("body")
		panic("boom")
	})

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "body" || order[1] != "defer" {
		t.Fatalf("the deferred call did not run during the unwind: %v", order)
	}
}

// Go is the same containment on its own goroutine, and it must still return
// rather than take the test binary down.
func TestGoContainsAPanicOnItsOwnGoroutine(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	Go("a test goroutine", func() {
		defer wg.Done()
		panic("boom on a worker goroutine")
	})
	wg.Wait()
}
