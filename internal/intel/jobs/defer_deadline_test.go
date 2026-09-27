package jobs

import (
	"strings"
	"testing"
	"time"

	"prism/internal/intel/store"
)

// TestDeferDeadlineRaisesShortGateWaitsToBackoff pins the 2026-10-02 fix. Step 4
// reports the provider's own next-allowed time, which for a provider-wide QPS
// valve is about a second; parking the whole inventory for exactly that long
// produced ~1.96M claims in 69 minutes and a job that never finished. The floor
// must push such a short wait out to the exponential backoff.
func TestDeferDeadlineRaisesShortGateWaitsToBackoff(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	item := store.JobItem{NodeHash: strings.Repeat("a", 32), Attempts: 1}

	got := deferDeadline(now, item, now+int64(time.Second))
	if got < now+int64(backoffBase) {
		t.Fatalf("deadline = +%s, want at least +%s (the gate said +1s)",
			time.Duration(got-now), backoffBase)
	}
	// The first backoff step plus at most one full jitter window.
	if upper := now + int64(2*backoffBase); got > upper {
		t.Fatalf("deadline = +%s, want at most +%s", time.Duration(got-now), 2*backoffBase)
	}
}

// TestDeferDeadlineKeepsLongerGateWaits makes sure the floor only ever raises a
// deadline: a gate that answers "come back in 20 minutes" keeps its own answer.
func TestDeferDeadlineKeepsLongerGateWaits(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	item := store.JobItem{NodeHash: strings.Repeat("b", 32), Attempts: 1}
	requested := now + int64(20*time.Minute)
	if got := deferDeadline(now, item, requested); got != requested {
		t.Fatalf("deadline = +%s, want the requested +20m", time.Duration(got-now))
	}
}

// TestDeferDeadlineGrowsWithAttempts pins the exponential part: repeated parks
// must space out instead of retrying on the same one-second cadence.
func TestDeferDeadlineGrowsWithAttempts(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	previous := int64(0)
	for _, attempts := range []int{1, 2, 3, 4, 5} {
		item := store.JobItem{NodeHash: strings.Repeat("c", 32), Attempts: attempts}
		got := deferDeadline(now, item, now+int64(time.Second))
		if got <= previous {
			t.Fatalf("attempts=%d deadline +%s, want more than the previous +%s",
				attempts, time.Duration(got-now), time.Duration(previous))
		}
		previous = got
	}
}

// TestDeferDeadlineSaturatesAtTheStepDeferralBound documents the ceiling: the
// floor stops growing at deferFloorMax, not at the 6h backoff ceiling. Step 4
// only parks an item when the gate reopens inside its own 30-minute bound, so a
// longer floor would hold the item past what the step agreed to wait - and while
// it is parked its job keeps one of the max_running_jobs slots.
func TestDeferDeadlineSaturatesAtTheStepDeferralBound(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	item := store.JobItem{NodeHash: strings.Repeat("d", 32), Attempts: 40}
	got := deferDeadline(now, item, now+int64(time.Second))
	if got < now+int64(deferFloorMax) {
		t.Fatalf("deadline = +%s, want at least +%s", time.Duration(got-now), deferFloorMax)
	}
	// The floor plus at most one full jitter window.
	if upper := now + int64(2*deferFloorMax); got > upper {
		t.Fatalf("deadline = +%s, want at most +%s", time.Duration(got-now), 2*deferFloorMax)
	}
	// It must no longer run away to the backoff ceiling.
	if got >= now+int64(backoffMax) {
		t.Fatalf("deadline = +%s, want below the %s backoff ceiling", time.Duration(got-now), backoffMax)
	}
}

// TestStableJitterIsDeterministicAndSpread checks both properties the schedule
// relies on: one node always gets the same offset (so a run is reproducible), and
// different nodes do not collide (so the inventory does not wake in one instant).
func TestStableJitterIsDeterministicAndSpread(t *testing.T) {
	window := 30 * time.Second
	seen := make(map[time.Duration]string)
	for _, hash := range []string{
		"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd", "eeeeeeee", "ffffffff",
	} {
		first := stableJitter(hash, window)
		if first < 0 || first >= window {
			t.Fatalf("stableJitter(%q, %s) = %s, want [0, %s)", hash, window, first, window)
		}
		if again := stableJitter(hash, window); again != first {
			t.Fatalf("stableJitter(%q) = %s then %s, want the same offset", hash, first, again)
		}
		seen[first] = hash
	}
	if len(seen) < 3 {
		t.Fatalf("six node hashes produced %d distinct offsets, want a spread", len(seen))
	}
	if got := stableJitter("a", 0); got != 0 {
		t.Fatalf("stableJitter with a zero window = %s, want 0", got)
	}
	if got := stableJitter("", window); got != 0 {
		t.Fatalf("stableJitter with an empty hash = %s, want 0", got)
	}
}

// TestStableJitterSpreadsAcrossTheWholeWindow is the test the first version of
// stableJitter would have failed. It derived the offset with `hash % window`,
// but the hash is 32 bits (max 2^32-1 ns, about 4.29 s) while every window used
// here is larger, so the modulo was the identity and every node landed inside the
// first 4.29 seconds of the window. That left the inventory waking in one burst -
// exactly what the jitter exists to prevent - so the assertion is about spread,
// not merely about offsets being distinct.
func TestStableJitterSpreadsAcrossTheWholeWindow(t *testing.T) {
	for _, window := range []time.Duration{time.Minute, 30 * time.Minute} {
		minSeen, maxSeen := window, time.Duration(0)
		distinct := make(map[time.Duration]struct{})
		for i := 0; i < 5000; i++ {
			// A distinct hash per iteration without another import: the pair
			// (i%32, i/32) is unique over this range.
			hash := strings.Repeat("a", i%32+1) + strings.Repeat("b", i/32+1)
			offset := stableJitter(hash, window)
			if offset < 0 || offset >= window {
				t.Fatalf("stableJitter(%q, %s) = %s, want [0, %s)", hash, window, offset, window)
			}
			if offset < minSeen {
				minSeen = offset
			}
			if offset > maxSeen {
				maxSeen = offset
			}
			distinct[offset] = struct{}{}
		}
		if maxSeen < window/2 {
			t.Fatalf("window %s: offsets only span [%s, %s]; a 32-bit hash taken modulo the window "+
				"cannot reach past ~4.29s", window, minSeen, maxSeen)
		}
		if len(distinct) < 1000 {
			t.Fatalf("window %s: 5000 hashes produced only %d distinct offsets", window, len(distinct))
		}
	}
}
