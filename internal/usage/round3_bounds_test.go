package usage

import (
	"math"
	"testing"
)

func TestRound3CountersCannotWrapOrUndercountAfterDrain(t *testing.T) {
	for _, first := range []int{0, 1} {
		c := NewCounters()
		keys := []CounterKey{{KeyID: "key", PlatformID: "large"}, {KeyID: "key", PlatformID: "small"}}
		c.Add(keys[0], Delta{IngressBytes: math.MaxInt64, EgressBytes: math.MaxInt64, Requests: math.MaxInt64, Errors: math.MaxInt64})
		c.Add(keys[0], Delta{IngressBytes: 1, Requests: 1, Errors: 1})
		c.Add(keys[1], Delta{IngressBytes: 100})
		if got := c.KeyBytes(keys[0]); got.Requests != math.MaxInt64 || got.Errors != math.MaxInt64 {
			t.Fatalf("wrapped counters: %+v", got)
		}
		if got := c.KeyTotal("key"); got != math.MaxInt64 {
			t.Fatalf("before drain: %d", got)
		}
		drained := c.Drain([]CounterKey{keys[first]})
		want := int64(math.MaxInt64)
		if first == 0 {
			want = 100
		}
		if got := c.KeyTotal("key"); got != want {
			t.Fatalf("drain %d: total=%d want=%d", first, got, want)
		}
		c.Requeue(drained)
		if got := c.KeyTotal("key"); got != math.MaxInt64 {
			t.Fatalf("requeue total=%d", got)
		}
		c.Drain(c.Keys())
		if c.KeyTotal("key") != 0 || c.Size() != 0 || c.keyBytes.Size() != 0 {
			t.Fatal("drain retained counters")
		}
	}
}
