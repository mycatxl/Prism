package usage

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"prism/internal/keys"
)

func TestTrafficReservationSharedAcrossConcurrentKeysAndDirections(t *testing.T) {
	counters := NewCounters()
	gate := &Gate{Limiter: NewLimiter(counters)}
	var moved atomic.Int64
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			key := &keys.KeyState{KeyID: fmt.Sprint(i), UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 101}}
			for range 10 {
				n, finish := gate.ReserveTraffic(key, "platform", i%2 == 0, 13)
				finish(n)
				finish(n) // completion must be idempotent
				moved.Add(n)
			}
		})
	}
	wg.Wait()
	if moved.Load() != 101 || counters.UserBytes("tenant") != 101 {
		t.Fatalf("moved=%d charged=%d, want exactly 101", moved.Load(), counters.UserBytes("tenant"))
	}
	var persisted int64
	for _, delta := range counters.Drain(counters.Keys()) {
		persisted += delta.Total()
	}
	if persisted != 101 {
		t.Fatalf("persistable traffic=%d, want 101", persisted)
	}
}

func TestTrafficReservationRefundAndPeriodRollover(t *testing.T) {
	counters := NewCounters()
	gate := &Gate{Limiter: NewLimiter(counters)}
	key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	n, finish := gate.ReserveTraffic(key, "platform", true, 100)
	if n != 100 {
		t.Fatal(n)
	}
	if gate.Exceeded(key, 0) {
		t.Fatal("pending reservation treated as delivered traffic")
	}
	finish(7)
	if counters.UserBytes("tenant") != 7 {
		t.Fatal("short write was not refunded")
	}
	_, late := gate.ReserveTraffic(key, "platform", false, 50)
	counters.BeginUserPeriodTransition("tenant").Commit("new-subscription", 42)
	late(30)
	if counters.UserBytes("tenant") != 0 {
		t.Fatal("old reservation changed new allowance")
	}
	_, current := gate.ReserveTraffic(key, "platform", true, 100)
	current(100)
	if !gate.Exceeded(key, 0) {
		t.Fatal("fully delivered allowance did not stop the stream")
	}
	if counters.UserBytes("tenant") != 100 {
		t.Fatal("new allowance was not independently charged")
	}
	var oldBytes, newBytes int64
	for bucket, delta := range counters.Drain(counters.Keys()) {
		if bucket.subscriptionID == "new-subscription" {
			newBytes += delta.Total()
		} else {
			oldBytes += delta.Total()
		}
	}
	if oldBytes != 37 || newBytes != 100 {
		t.Fatalf("period accounting old=%d new=%d", oldBytes, newBytes)
	}
}

func TestTrafficReservationFailsClosedBeforeSeedAndCannotOverflow(t *testing.T) {
	counters := NewCounters()
	gate := &Gate{Limiter: NewLimiter(counters)}
	key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: math.MaxInt64}}
	counters.RequireSeed()
	if n, _ := gate.ReserveTraffic(key, "", true, 10); n != 0 {
		t.Fatal("unknown persisted usage was admitted")
	}
	counters.LoadUser("tenant", math.MaxInt64-5)
	counters.MarkSeedReady()
	n, finish := gate.ReserveTraffic(key, "", true, math.MaxInt64)
	if n != 5 {
		t.Fatal(n)
	}
	finish(n)
	if counters.UserBytes("tenant") != math.MaxInt64 {
		t.Fatal("reservation overflow")
	}
}
