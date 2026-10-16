package usage

import (
	"fmt"
	"sync"
	"testing"

	"prism/internal/keys"
	"prism/internal/model"
)

// KeyTotal is the O(1) answer to "how much has this key moved": it is maintained
// as the bytes arrive instead of walking the buckets on every quota check. It
// must therefore equal what the walk returned - the sum of the buckets that are
// still present - for every key, on every platform, after every drain.
func TestCounters_KeyTotalTracksOnlyThePresentBuckets(t *testing.T) {
	counters := NewCounters()
	counters.Add(CounterKey{KeyID: "k-other", PlatformID: "p-1"}, Delta{IngressBytes: 5_000})
	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-1"}, Delta{IngressBytes: 300})
	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-2"}, Delta{EgressBytes: 200})

	if got := counters.KeyTotal("k-1"); got != 500 {
		t.Fatalf("KeyTotal(k-1) = %d, want 500 (both platforms of the key)", got)
	}
	if got := counters.KeyTotal("k-other"); got != 5_000 {
		t.Fatalf("KeyTotal(k-other) = %d, want 5000: another key's bytes leaked into the total", got)
	}
	if got := counters.KeyTotal(""); got != 0 {
		t.Fatalf("KeyTotal(\"\") = %d, want 0: traffic with no key belongs to no aggregate", got)
	}

	// A drain takes its bucket with it, exactly as the walk it replaces did.
	counters.Drain([]CounterKey{{KeyID: "k-1", PlatformID: "p-1"}})
	if got := counters.KeyTotal("k-1"); got != 200 {
		t.Fatalf("KeyTotal(k-1) = %d after draining one platform, want 200", got)
	}
	if got := counters.KeyTotal("k-other"); got != 5_000 {
		t.Fatalf("draining k-1 changed another key's total to %d", got)
	}

	// Bytes that arrive after the drain go back into the total.
	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})
	if got := counters.KeyTotal("k-1"); got != 300 {
		t.Fatalf("KeyTotal(k-1) = %d after new traffic, want 300", got)
	}

	// Draining everything leaves zero rather than a stale figure.
	counters.Drain(counters.Keys())
	if got := counters.KeyTotal("k-1"); got != 0 {
		t.Fatalf("KeyTotal(k-1) = %d after a full drain, want 0", got)
	}
}

// The running total has to stay exact while the counters are being written and
// drained at the same time: it is read on the quota path, where a drifted figure
// would either refuse a user early or let them past their allowance.
func TestCounters_KeyTotalIsExactUnderConcurrentWritesAndDrains(t *testing.T) {
	counters := NewCounters()
	const (
		writers   = 8
		perWriter = 500
		platforms = 4
	)

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := CounterKey{KeyID: "k-1", PlatformID: fmt.Sprintf("p-%d", i%platforms)}
			for j := 0; j < perWriter; j++ {
				counters.Add(key, Delta{IngressBytes: 1})
			}
		}(i)
	}
	for i := 0; i < platforms; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				counters.Drain([]CounterKey{{KeyID: "k-1", PlatformID: fmt.Sprintf("p-%d", i)}})
			}
		}(i)
	}
	wg.Wait()

	// Whatever the interleaving was, the total must equal the sum of the
	// buckets that are still present - the invariant the walk relied on.
	var want int64
	for _, key := range counters.Keys() {
		if key.KeyID != "k-1" {
			continue
		}
		want += counters.KeyBytes(key).Total()
	}
	if got := counters.KeyTotal("k-1"); got != want {
		t.Fatalf("KeyTotal(k-1) = %d, want %d (the sum of the present buckets)", got, want)
	}
}

// The fallback a quota check makes has to sum every platform the key reached:
// that is what the bucket walk returned, and the running total replaces it.
func TestLimiter_FallbackCountsEveryPlatformOfTheKey(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "", keys.PlanSnapshot{
		TrafficBytes: 400,
		Status:       model.SubscriptionStatusActive,
	})

	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-1"}, Delta{IngressBytes: 250})
	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-2"}, Delta{EgressBytes: 200})

	got := limiter.Check(key, 0)
	if got.Allowed {
		t.Fatal("two platforms' traffic did not add up: the key was allowed past its allowance")
	}
	if got.Reason != ReasonTraffic {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonTraffic)
	}
}
