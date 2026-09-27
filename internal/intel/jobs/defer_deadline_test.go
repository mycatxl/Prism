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

// TestDeferDeadlineSaturatesAtTheBackoffMaximum documents the ceiling: once the
// exponential step reaches backoffMax the park time stops growing, so a gate that
// stays closed for days costs one retry every six hours rather than a spin.
func TestDeferDeadlineSaturatesAtTheBackoffMaximum(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	item := store.JobItem{NodeHash: strings.Repeat("d", 32), Attempts: 40}
	got := deferDeadline(now, item, now+int64(time.Second))
	if got < now+int64(backoffMax) {
		t.Fatalf("deadline = +%s, want at least +%s", time.Duration(got-now), backoffMax)
	}
	if upper := now + int64(2*backoffMax); got > upper {
		t.Fatalf("deadline = +%s, want at most +%s", time.Duration(got-now), 2*backoffMax)
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
