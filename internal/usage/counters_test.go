package usage

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCounters_AddAndRead(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}

	counters.Add(key, Delta{IngressBytes: 100, EgressBytes: 40, Requests: 1})
	counters.Add(key, Delta{IngressBytes: 10, EgressBytes: 5, Requests: 1, Errors: 1})

	got := counters.KeyBytes(key)
	if got.IngressBytes != 110 || got.EgressBytes != 45 {
		t.Fatalf("bytes = %+v, want ingress 110 egress 45", got)
	}
	if got.Requests != 2 || got.Errors != 1 {
		t.Fatalf("counts = %+v, want 2 requests 1 error", got)
	}
	if total := got.Total(); total != 155 {
		t.Fatalf("total = %d, want 155", total)
	}
}

// The user aggregate is what a subscription quota is checked against, so a
// delta must reach it as well as the per-key bucket.
func TestCounters_AggregatePerUser(t *testing.T) {
	counters := NewCounters()
	first := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}
	second := CounterKey{KeyID: "k-2", UserID: "u-1", PlatformID: "p-2"}
	other := CounterKey{KeyID: "k-3", UserID: "u-2", PlatformID: "p-1"}

	counters.Add(first, Delta{IngressBytes: 100, EgressBytes: 0})
	counters.Add(second, Delta{IngressBytes: 0, EgressBytes: 50})
	counters.Add(other, Delta{IngressBytes: 999, EgressBytes: 0})

	// Both of u-1's keys count towards the one allowance.
	if got := counters.UserBytes("u-1"); got != 150 {
		t.Fatalf("user u-1 total = %d, want 150", got)
	}
	if got := counters.UserBytes("u-2"); got != 999 {
		t.Fatalf("user u-2 total = %d, want 999", got)
	}
	if got := counters.UserBytes("u-missing"); got != 0 {
		t.Fatalf("an unknown user has %d bytes, want 0", got)
	}
}

// A key with no owner is the single-tenant path. It still counts per key, but
// it must not create a user aggregate that would be attributed to nobody.
func TestCounters_KeyWithoutOwnerCountsOnlyPerKey(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", PlatformID: "p-1"}

	counters.Add(key, Delta{IngressBytes: 70, EgressBytes: 30})

	if got := counters.KeyBytes(key).Total(); got != 100 {
		t.Fatalf("key total = %d, want 100", got)
	}
	if got := counters.UserBytes(""); got != 0 {
		t.Fatalf("the empty owner has %d bytes, want 0", got)
	}
}

// Platforms are separate buckets: an operator wants to see which one the
// traffic went to, and a per-platform quota depends on it.
func TestCounters_SeparatePlatforms(t *testing.T) {
	counters := NewCounters()
	first := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}
	second := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-2"}

	counters.Add(first, Delta{IngressBytes: 10})
	counters.Add(second, Delta{IngressBytes: 20})

	if got := counters.KeyBytes(first).IngressBytes; got != 10 {
		t.Fatalf("p-1 ingress = %d, want 10", got)
	}
	if got := counters.KeyBytes(second).IngressBytes; got != 20 {
		t.Fatalf("p-2 ingress = %d, want 20", got)
	}
	// The user sees both.
	if got := counters.UserBytes("u-1"); got != 30 {
		t.Fatalf("user total = %d, want 30", got)
	}
	if got := counters.Size(); got != 2 {
		t.Fatalf("buckets = %d, want 2", got)
	}
}

// A zero delta must not create a bucket: an empty request would otherwise leave
// one entry per key for the life of the process.
func TestCounters_ZeroDeltaCreatesNothing(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}

	counters.Add(key, Delta{})

	if got := counters.Size(); got != 0 {
		t.Fatalf("buckets = %d, want 0", got)
	}
	if got := counters.UserBytes("u-1"); got != 0 {
		t.Fatalf("user total = %d, want 0", got)
	}
}

// Drain takes the counters away: the flusher persists what it drained, and a
// value left behind would be counted twice on the next pass.
func TestCounters_DrainRemovesTheBuckets(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}
	counters.Add(key, Delta{IngressBytes: 500, EgressBytes: 250, Requests: 3})

	drained := counters.Drain([]CounterKey{key})
	got, ok := drained[key]
	if !ok {
		t.Fatal("the bucket was not drained")
	}
	if got.IngressBytes != 500 || got.EgressBytes != 250 || got.Requests != 3 {
		t.Fatalf("drained = %+v", got)
	}
	if counters.Size() != 0 {
		t.Fatalf("buckets after drain = %d, want 0", counters.Size())
	}
	if counters.KeyBytes(key) != (Delta{}) {
		t.Fatal("the drained bucket still reports bytes")
	}

	// The user aggregate survives the drain: it is the running total a quota is
	// checked against, and resetting it would hand the allowance back every
	// time the flusher ran.
	if got := counters.UserBytes("u-1"); got != 750 {
		t.Fatalf("user total after drain = %d, want 750", got)
	}
}

func TestCounters_DrainSkipsMissingKeys(t *testing.T) {
	counters := NewCounters()
	drained := counters.Drain([]CounterKey{{KeyID: "absent"}})
	if len(drained) != 0 {
		t.Fatalf("drained = %+v, want nothing", drained)
	}
}

// The counters are hit once per byte on the hot path, so concurrent use must be
// correct without a shared lock.
func TestCounters_ConcurrentAddsAreExact(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}

	const goroutines = 64
	const perGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				counters.Add(key, Delta{IngressBytes: 1})
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * perGoroutine)
	if got := counters.KeyBytes(key).IngressBytes; got != want {
		t.Fatalf("ingress = %d, want %d", got, want)
	}
	if got := counters.UserBytes("u-1"); got != want {
		t.Fatalf("user total = %d, want %d", got, want)
	}
}

// Draining while writers are active must not lose or duplicate a byte: every
// byte is either drained or still in the counter, never both and never neither.
func TestCounters_DrainUnderConcurrentWrites(t *testing.T) {
	counters := NewCounters()
	key := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}

	const writers = 16
	const perWriter = 500

	var drained atomic.Int64
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			for _, delta := range counters.Drain([]CounterKey{key}) {
				drained.Add(delta.Total())
			}
		}
	}()

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				counters.Add(key, Delta{IngressBytes: 1})
			}
		}()
	}
	wg.Wait()

	// Whatever the flusher did not take is still in the bucket.
	remaining := counters.KeyBytes(key).Total()
	total := drained.Load() + remaining
	want := int64(writers * perWriter)
	if total != want {
		t.Fatalf("drained %d + remaining %d = %d, want %d", drained.Load(), remaining, total, want)
	}
	// The user aggregate is never drained, so it must hold every byte.
	if got := counters.UserBytes("u-1"); got != want {
		t.Fatalf("user total = %d, want %d", got, want)
	}
}

func TestCounters_KeysReportsEveryBucket(t *testing.T) {
	counters := NewCounters()
	for i := 0; i < 5; i++ {
		counters.Add(CounterKey{KeyID: fmt.Sprintf("k-%d", i), UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1})
	}

	keys := counters.Keys()
	if len(keys) != 5 {
		t.Fatalf("keys = %d, want 5", len(keys))
	}
	seen := make(map[string]bool)
	for _, key := range keys {
		seen[key.KeyID] = true
	}
	for i := 0; i < 5; i++ {
		if !seen[fmt.Sprintf("k-%d", i)] {
			t.Fatalf("key k-%d is missing from %v", i, keys)
		}
	}
}

// A nil counter set must be usable rather than panic: the data plane holds one
// unconditionally, and a deployment without metering passes nil.
func TestCounters_NilIsSafe(t *testing.T) {
	var counters *Counters
	counters.Add(CounterKey{KeyID: "k"}, Delta{IngressBytes: 1})
	if got := counters.KeyBytes(CounterKey{KeyID: "k"}); got != (Delta{}) {
		t.Fatalf("nil counters reported %+v", got)
	}
	if got := counters.UserBytes("u"); got != 0 {
		t.Fatalf("nil counters reported %d bytes", got)
	}
	if got := counters.Size(); got != 0 {
		t.Fatalf("nil counters reported size %d", got)
	}
	if got := counters.Keys(); got != nil {
		t.Fatalf("nil counters reported keys %v", got)
	}
	if got := counters.Drain([]CounterKey{{KeyID: "k"}}); len(got) != 0 {
		t.Fatalf("nil counters drained %+v", got)
	}
}

// "No aggregate" and "an aggregate of zero" are different answers, and the
// quota check has to be able to tell them apart: a period rollover zeroes the
// total while keeping the entry, and the key's own counter still holds the
// previous period's bytes until the flush writes them.
func TestCounters_UserBytesPresentDistinguishesZeroFromAbsent(t *testing.T) {
	counters := NewCounters()

	if total, ok := counters.UserBytesPresent("u-1"); ok || total != 0 {
		t.Fatalf("an unknown user reported (%d, %v), want (0, false)", total, ok)
	}
	// The empty user id has no aggregate by definition, and neither has a nil
	// counter set.
	if _, ok := counters.UserBytesPresent(""); ok {
		t.Fatal("the empty user id reported an aggregate")
	}
	var none *Counters
	if total, ok := none.UserBytesPresent("u-1"); ok || total != 0 {
		t.Fatalf("a nil counter set reported (%d, %v)", total, ok)
	}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 400})
	if total, ok := counters.UserBytesPresent("u-1"); !ok || total != 400 {
		t.Fatalf("after 400 bytes the user reported (%d, %v), want (400, true)", total, ok)
	}

	counters.ResetUser("u-1")
	if total, ok := counters.UserBytesPresent("u-1"); !ok || total != 0 {
		t.Fatalf("after a rollover the user reported (%d, %v), want (0, true): the entry has to survive the reset", total, ok)
	}
}
