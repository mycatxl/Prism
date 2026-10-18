package usage

import (
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

// quotaKey builds a usable key state with the given plan limits. The plan
// status is active and nothing expires, so a refusal can only come from the
// limit under test.
func quotaKey(t *testing.T, userID string, plan keys.PlanSnapshot) *keys.KeyState {
	t.Helper()
	return &keys.KeyState{
		KeyID:      "k-1",
		UserID:     userID,
		Scope:      model.KeyScopeProxy,
		Status:     model.KeyStatusActive,
		UserStatus: model.UserStatusActive,
		Plan:       plan,
	}
}

func TestLimiter_AllowsAKeyWithinItsLimits(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes:  1000,
		MaxConcurrent: 4,
		Status:        model.SubscriptionStatusActive,
	})

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 400, EgressBytes: 100})

	if got := limiter.Check(key, 2); !got.Allowed {
		t.Fatalf("a request within the limits was refused: %+v", got)
	}
}

// The transitional path has no plan and therefore no quota: refusing it would
// break every deployment that has not been migrated to keys yet.
func TestLimiter_AllowsTheKeylessPath(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	if got := limiter.Check(nil, 1000); !got.Allowed {
		t.Fatalf("the keyless path was refused: %+v", got)
	}
}

func TestLimiter_RefusesWhenTheTrafficIsUsedUp(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes: 1000,
		Status:       model.SubscriptionStatusActive,
	})

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 600, EgressBytes: 400})

	got := limiter.Check(key, 0)
	if got.Allowed {
		t.Fatal("a request over the traffic limit was allowed")
	}
	if got.Reason != ReasonTraffic {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonTraffic)
	}
}

// The allowance belongs to the subscription, not to each key: a user with two
// keys must not get the whole allowance twice.
func TestLimiter_TrafficIsCountedPerUserAcrossKeys(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes: 1000,
		Status:       model.SubscriptionStatusActive,
	})

	// Half the allowance through each of two keys.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 500})
	counters.Add(CounterKey{KeyID: "k-2", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 500})

	got := limiter.Check(key, 0)
	if got.Allowed {
		t.Fatal("the second key's traffic did not count towards the shared allowance")
	}
	if got.Reason != ReasonTraffic {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonTraffic)
	}
}

// A period rollover zeroes the user's aggregate and deliberately leaves the
// per-key counters holding the previous period's bytes, which the flush has not
// written to the usage tables yet. Reading that zero as "no aggregate" made the
// check fall back to the key's own counter, so the previous period's leftovers
// were compared against the new period's allowance: a user who had just been
// granted a fresh period was refused outright, for as long as the flush was
// behind (ten seconds by default, longer when a write is retried), and the
// traffic dimension has no grace to soften it.
func TestLimiter_RolloverDoesNotReadThePreviousPeriodsBytes(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes: 1000,
		Status:       model.SubscriptionStatusActive,
	})

	// The old period: over the limit, with the per-key bucket still undrained.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1200})
	if got := limiter.Check(key, 0); got.Allowed {
		t.Fatalf("a key past its traffic limit was allowed: %+v", got)
	}

	// The rollover clears the aggregate for the new period, and nothing else.
	counters.ResetUser("u-1")

	got := limiter.Check(key, 0)
	if !got.Allowed {
		t.Fatalf("the first request of a new period was refused with %q: the previous period's undrained bucket was read as the new period's usage", got.Reason)
	}
	if used := limiter.usedBytes(key); used != 0 {
		t.Fatalf("the new period reports %d bytes used, want 0", used)
	}
	// Exceeded reads the same total, so a live connection must not be cut off
	// by a period it no longer belongs to.
	if limiter.Exceeded(key, DefaultGracePercent) {
		t.Fatal("a live connection was treated as over the new period's limit")
	}
	// The per-key bucket is untouched on purpose: it is the record of what
	// actually moved, and the next flush still has to write it.
	if total := counters.KeyTotal("k-1"); total != 1200 {
		t.Fatalf("the per-key total is %d, want the previous period's 1200", total)
	}

	// Traffic moved in the new period counts against the new period.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1000})
	if got := limiter.Check(key, 0); got.Allowed {
		t.Fatal("traffic moved in the new period did not count towards its limit")
	}
}

// A key with no owner falls back to its own counter, which is the single-tenant
// path: there is no user aggregate to read.
func TestLimiter_FallsBackToTheKeyCounterWithoutAnOwner(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "", keys.PlanSnapshot{
		TrafficBytes: 100,
		Status:       model.SubscriptionStatusActive,
	})

	counters.Add(CounterKey{KeyID: "k-1", PlatformID: "p-1"}, Delta{IngressBytes: 150})

	got := limiter.Check(key, 0)
	if got.Allowed {
		t.Fatal("a key without an owner was allowed past its own counter")
	}
	if got.Reason != ReasonTraffic {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonTraffic)
	}
}

// A zero traffic limit means unlimited, which is the documented default.
func TestLimiter_ZeroTrafficLimitIsUnlimited(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1 << 40})

	if got := limiter.Check(key, 0); !got.Allowed {
		t.Fatalf("a zero traffic limit refused a request: %+v", got)
	}
}

func TestLimiter_RefusesOverTheConcurrencyLimit(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		MaxConcurrent: 3,
		Status:        model.SubscriptionStatusActive,
	})

	// Below the limit is allowed; at the limit is not, because the new
	// connection would make it four.
	if got := limiter.Check(key, 2); !got.Allowed {
		t.Fatalf("a request below the concurrency limit was refused: %+v", got)
	}
	got := limiter.Check(key, 3)
	if got.Allowed {
		t.Fatal("a request at the concurrency limit was allowed")
	}
	if got.Reason != ReasonConcurrency {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonConcurrency)
	}
}

// A zero concurrency limit means unlimited.
func TestLimiter_ZeroConcurrencyLimitIsUnlimited(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})

	if got := limiter.Check(key, 10_000); !got.Allowed {
		t.Fatalf("a zero concurrency limit refused a request: %+v", got)
	}
}

// A disabled key, a disabled user, an expired key and a suspended plan all
// refuse, and they all report the same reason so the response does not tell a
// caller which part of the account is at fault.
func TestLimiter_RefusesUnusableKeys(t *testing.T) {
	nowNs := time.Now().UnixNano()
	cases := []struct {
		name   string
		mutate func(*keys.KeyState)
	}{
		{"disabled key", func(k *keys.KeyState) { k.Status = model.KeyStatusDisabled }},
		{"expired key", func(k *keys.KeyState) { k.ExpiresAtNs = nowNs - 1 }},
		{"disabled user", func(k *keys.KeyState) { k.UserStatus = model.UserStatusDisabled }},
		{"suspended plan", func(k *keys.KeyState) { k.Plan.Status = model.SubscriptionStatusSuspended }},
		{"expired plan", func(k *keys.KeyState) { k.Plan.ExpiresAtNs = nowNs - 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})
			tc.mutate(key)

			got := NewLimiter(NewCounters()).Check(key, 0)
			if got.Allowed {
				t.Fatal("an unusable key was allowed")
			}
			if got.Reason != ReasonDisabled {
				t.Fatalf("reason = %q, want %q", got.Reason, ReasonDisabled)
			}
		})
	}
}

// The order matters: an unusable key must report that rather than a quota it
// happens to be over, because the operator needs the fundamental problem.
func TestLimiter_UnusableKeyReportsBeforeQuota(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes:  10,
		MaxConcurrent: 1,
		Status:        model.SubscriptionStatusActive,
	})
	key.Status = model.KeyStatusDisabled
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1000})

	got := limiter.Check(key, 5)
	if got.Reason != ReasonDisabled {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonDisabled)
	}
}

// Exceeded is the live-connection check: it applies the grace fraction so a
// transfer that crossed the limit is not cut off mid-flight.
func TestLimiter_ExceededAppliesTheGrace(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes: 1000,
		Status:       model.SubscriptionStatusActive,
	})
	key.UserID = "u-1"

	// At the limit but inside a 10% grace: not yet exceeded.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1050})
	if limiter.Exceeded(key, 10) {
		t.Fatal("a transfer inside the grace was treated as exceeded")
	}

	// Past the grace: exceeded.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})
	if !limiter.Exceeded(key, 10) {
		t.Fatal("a transfer past the grace was not treated as exceeded")
	}
}

// With no grace, the limit itself is the boundary.
func TestLimiter_ExceededWithoutGrace(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		TrafficBytes: 1000,
		Status:       model.SubscriptionStatusActive,
	})

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 999})
	if limiter.Exceeded(key, 0) {
		t.Fatal("a transfer below the limit was treated as exceeded")
	}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1})
	if !limiter.Exceeded(key, 0) {
		t.Fatal("a transfer at the limit was not treated as exceeded")
	}
}

// A key without a traffic limit is never exceeded, whatever it has moved.
func TestLimiter_ExceededIsFalseWithoutALimit(t *testing.T) {
	counters := NewCounters()
	limiter := NewLimiter(counters)
	key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1 << 40})

	if limiter.Exceeded(key, 10) {
		t.Fatal("a key without a traffic limit was treated as exceeded")
	}
	if limiter.Exceeded(nil, 10) {
		t.Fatal("a nil key was treated as exceeded")
	}
}

// The expiry checks read the injected clock, so a plan that expires is refused
// without waiting for wall-clock time.
func TestLimiter_ExpiryUsesTheInjectedClock(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	expiresAt := time.Now().Add(time.Hour).UnixNano()
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		Status:      model.SubscriptionStatusActive,
		ExpiresAtNs: expiresAt,
	})

	if got := limiter.Check(key, 0); !got.Allowed {
		t.Fatalf("an unexpired plan was refused: %+v", got)
	}

	limiter.SetNow(func() int64 { return expiresAt + 1 })
	got := limiter.Check(key, 0)
	if got.Allowed {
		t.Fatal("an expired plan was allowed")
	}
	if got.Reason != ReasonDisabled {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonDisabled)
	}
}

// A nil limiter or a nil counter set must not panic: the data plane holds one
// unconditionally and a deployment without metering passes nothing.
func TestLimiter_NilIsSafe(t *testing.T) {
	var limiter *Limiter
	key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})

	if got := limiter.Check(key, 0); !got.Allowed {
		t.Fatalf("a nil limiter refused a request: %+v", got)
	}
	if limiter.Exceeded(key, 10) {
		t.Fatal("a nil limiter reported exceeded")
	}

	noCounters := NewLimiter(nil)
	if got := noCounters.Check(key, 0); !got.Allowed {
		t.Fatalf("a limiter without counters refused a request: %+v", got)
	}
}

// MaxRPS was stored, snapshotted and shipped to the data plane with nothing
// reading it, so a plan that advertised a rate gave away an unlimited one.
// These pin the schedule the limiter now keeps.
func TestLimiter_RefusesBeyondTheRateLimit(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	key := quotaKey(t, "u-1", keys.PlanSnapshot{
		MaxRPS: 2,
		Status: model.SubscriptionStatusActive,
	})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	limiter.SetNow(func() int64 { return now })

	for i := 0; i < 2; i++ {
		if got := limiter.Check(key, 0); !got.Allowed {
			t.Fatalf("request %d within two per second was refused: %+v", i+1, got)
		}
	}
	if got := limiter.Check(key, 0); got.Allowed || got.Reason != ReasonRate {
		t.Fatalf("the third request in one second must be refused for rate: %+v", got)
	}
	// The refusals above must not have pushed the schedule out: half a second
	// after the pair, exactly one slot is back.
	now += int64(500 * time.Millisecond)
	if got := limiter.Check(key, 0); !got.Allowed {
		t.Fatalf("a request after the interval was refused: %+v", got)
	}
	if got := limiter.Check(key, 0); got.Allowed {
		t.Fatalf("the schedule handed back a second slot early: %+v", got)
	}
}

// The budget belongs to the key, so one key cannot spend another key's
// allowance and the user's second key is not throttled by the first.
func TestLimiter_RateIsPerKeyNotPerUser(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	plan := keys.PlanSnapshot{MaxRPS: 1, Status: model.SubscriptionStatusActive}
	first := quotaKey(t, "u-1", plan)
	second := quotaKey(t, "u-1", plan)
	second.KeyID = "k-2"
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	limiter.SetNow(func() int64 { return now })

	if got := limiter.Check(first, 0); !got.Allowed {
		t.Fatalf("first key refused: %+v", got)
	}
	if got := limiter.Check(second, 0); !got.Allowed {
		t.Fatalf("one key's requests must not spend another key's budget: %+v", got)
	}
	if got := limiter.Check(first, 0); got.Allowed {
		t.Fatalf("the first key had its second request in the same second admitted: %+v", got)
	}
}

// A plan with no rate limit is not slowed down, and an idle key owes nothing
// for the time it was quiet.
func TestLimiter_RateLimitOfZeroIsUnlimited(t *testing.T) {
	limiter := NewLimiter(NewCounters())
	key := quotaKey(t, "u-1", keys.PlanSnapshot{Status: model.SubscriptionStatusActive})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	limiter.SetNow(func() int64 { return now })

	for i := 0; i < 1000; i++ {
		if got := limiter.Check(key, 0); !got.Allowed {
			t.Fatalf("an unlimited plan was refused on request %d: %+v", i+1, got)
		}
	}
}
