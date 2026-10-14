package usage

import (
	"time"

	"prism/internal/keys"
)

// Decision is the outcome of a quota check.
//
// It is a value rather than an error so the caller can log which limit was hit
// without matching on an error string, and so the reason can be reported to the
// client without leaking anything about other tenants.
type Decision struct {
	Allowed bool
	// Reason is empty when Allowed is true. It names the limit that refused the
	// request: "traffic", "concurrency", "rate", "expired" or "disabled".
	Reason string
}

// Reasons a request is refused. They are stable strings: the API reports them
// and the tests assert on them.
const (
	ReasonTraffic     = "traffic"
	ReasonConcurrency = "concurrency"
	ReasonRate        = "rate"
	ReasonExpired     = "expired"
	ReasonDisabled    = "disabled"
)

// DefaultGracePercent is the tolerance applied to the traffic limit before a
// session that is already running is cut.
//
// It is not a free allowance: a request that would start after the limit has
// no grace at all, and is refused outright. The grace exists only so that a
// transfer that crossed the line is not severed with its last bytes still in
// flight, which costs the operator one percent and saves every large download
// from failing at the finish line.
const DefaultGracePercent = 1

// Limiter decides whether a request may proceed.
//
// It reads the live counters and the key's own plan snapshot, so it never
// touches the database. Every method is safe for concurrent use.
type Limiter struct {
	counters *Counters
	// now is the clock the expiry checks read. It is a field so tests can pin
	// it without sleeping.
	now func() int64
}

// NewLimiter returns a limiter over the given counters.
func NewLimiter(counters *Counters) *Limiter {
	return &Limiter{
		counters: counters,
		now:      func() int64 { return time.Now().UnixNano() },
	}
}

// Check evaluates the limits a request must satisfy before it is routed.
//
// The order is deliberate: the cheap checks that cannot change mid-request come
// first, and the byte comparison comes last. A refusal names the first limit
// that failed, so an operator sees the most fundamental problem rather than a
// downstream symptom.
//
// concurrent is the number of connections this key already holds, which the
// caller tracks because only it knows when a connection opens and closes.
//
// A nil limiter allows everything: the data plane holds one unconditionally,
// and a deployment that never built one must behave as it did before quotas
// existed rather than refuse every request.
func (l *Limiter) Check(key *keys.KeyState, concurrent int) Decision {
	if l == nil {
		return Decision{Allowed: true}
	}
	if key == nil {
		// No key means the transitional global-token path, which has no plan
		// and therefore no quota. Refusing here would break every deployment
		// that has not been migrated.
		return Decision{Allowed: true}
	}

	nowNs := l.now()
	if !key.Usable(nowNs) {
		// Usable already covers a disabled key, a disabled user, an expired key
		// and an expired or suspended plan. The distinction matters to the
		// operator, not to the client, so it is collapsed into one reason that
		// does not tell a caller which part of their account is at fault.
		return Decision{Reason: ReasonDisabled}
	}

	if limit := key.Plan.MaxConcurrent; limit > 0 && concurrent >= limit {
		return Decision{Reason: ReasonConcurrency}
	}

	if limit := key.Plan.TrafficBytes; limit > 0 {
		used := l.usedBytes(key)
		if used >= limit {
			return Decision{Reason: ReasonTraffic}
		}
	}

	return Decision{Allowed: true}
}

// usedBytes reports how much of the key's allowance has been consumed.
//
// The user aggregate is the authority, not the per-key counter: a subscription
// grants its traffic to the user, and a user with several keys must not get the
// whole allowance once per key. The key's own counter is added only when the
// user aggregate is unavailable, which happens when a key has no owner (the
// single-tenant path) and when the process started after the usage was
// persisted but before the aggregate was restored.
func (l *Limiter) usedBytes(key *keys.KeyState) int64 {
	if l == nil || l.counters == nil {
		return 0
	}
	if key.UserID != "" {
		if total := l.counters.UserBytes(key.UserID); total > 0 {
			return total
		}
	}
	// Sum every platform bucket of this key: the allowance is per user, and a
	// key may reach several platforms.
	var total int64
	for _, counterKey := range l.counters.Keys() {
		if counterKey.KeyID != key.KeyID {
			continue
		}
		total += l.counters.KeyBytes(counterKey).Total()
	}
	return total
}

// Exceeded reports whether the key has already used more than its allowance,
// with the grace fraction applied.
//
// It is what a live connection checks: a request that is already running is not
// refused the moment the counter crosses the limit, because the bytes that
// crossed it are already in flight. The grace gives the transfer a chance to
// finish rather than cutting every long download exactly at the limit.
//
// gracePercent is the tolerance in percent of the limit; zero means no grace.
func (l *Limiter) Exceeded(key *keys.KeyState, gracePercent int) bool {
	if key == nil || key.Plan.TrafficBytes <= 0 {
		return false
	}
	limit := key.Plan.TrafficBytes
	grace := limit / 100 * int64(gracePercent)
	used := l.usedBytes(key)
	if used < limit {
		return false
	}
	return used >= limit+grace
}

// SetNow replaces the clock. It exists for the tests, which need a fixed time
// rather than a sleep.
func (l *Limiter) SetNow(now func() int64) {
	if l == nil || now == nil {
		return
	}
	l.now = now
}
