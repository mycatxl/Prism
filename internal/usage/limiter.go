package usage

import (
	"math"
	"sync"
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

// DefaultGracePercent grants no traffic beyond the purchased allowance.
const DefaultGracePercent = 0

// Limiter decides whether a request may proceed.
//
// It reads the live counters and the key's own plan snapshot, so it never
// touches the database. Every method is safe for concurrent use.
type Limiter struct {
	counters *Counters
	// now is the clock the expiry checks read. It is a field so tests can pin
	// it without sleeping. nowMu protects the test hook as Check may run in
	// parallel with a test (or a live clock refresh).
	nowMu sync.RWMutex
	now   func() int64
	// rates is the per-key request schedule behind MaxRPS. It lives here
	// rather than in the data plane because this is the only object that sees
	// every request a key makes.
	rates rateTable
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
// A nil limiter allows a keyed request when quota enforcement is not wired.
// Unkeyed requests are always refused, even when the limiter itself is absent.
func (l *Limiter) Check(key *keys.KeyState, concurrent int) Decision {
	if key == nil {
		return Decision{Reason: ReasonDisabled}
	}
	if l == nil {
		return Decision{Allowed: true}
	}

	l.nowMu.RLock()
	now := l.now
	l.nowMu.RUnlock()
	if now == nil {
		now = time.Now().UnixNano
	}
	nowNs := now()
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

	if limit := key.Plan.MaxRPS; limit > 0 {
		if name := rateKey(key); name != "" {
			interval := int64(time.Second) / int64(limit)
			if interval < 1 {
				// A plan cannot issue more than one request per nanosecond. Treat
				// larger configured values as the tightest representable schedule
				// instead of turning the limit off when integer division yields 0.
				interval = 1
			}
			if !l.rates.allow(name, interval, int64(time.Second), nowNs) {
				return Decision{Reason: ReasonRate}
			}
		}
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
// whole allowance once per key. The key's own counter is read only when the
// process started after usage was persisted but before the aggregate was
// restored, or for a key without an owner in an internal migration fixture.
//
// Whether the aggregate exists is the question, not whether it is non-zero. A
// period rollover sets it back to zero while deliberately leaving the per-key
// counters holding the previous period's bytes - they are the record of what
// moved and are still waiting to be written to the usage tables - so treating a
// zero total as "no aggregate" read the old period's leftovers as the new
// period's usage and refused requests of users who had just been given a fresh
// allowance, for as long as the flush was behind.
func (l *Limiter) usedBytes(key *keys.KeyState) int64 {
	if l == nil || l.counters == nil {
		return 0
	}
	if key.UserID != "" {
		if !l.counters.SeedReady() {
			return math.MaxInt64
		}
		if total, ok := l.counters.UserBytesPresent(key.UserID); ok {
			return total
		}
	}
	// The fallback reads the key's running total, which every bucket of that
	// key feeds as the bytes arrive and which a drain reduces by exactly what
	// it removed. Walking the buckets here would return the same number and
	// cost one iteration per bucket in the process, on the path that is
	// supposed to answer "may this request proceed" before anything is
	// dialled.
	//
	// Like the walk it replaces, the total covers the buckets that have not
	// been flushed yet: traffic the flusher has already written to the usage
	// tables is out of it. That is the same answer the walk gave, and for the
	// paths that reach here - a key with no owner, and the window before a
	// restart's aggregate is restored - there is no better source.
	return l.counters.KeyTotal(key.KeyID)
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
	// Admission includes pending reservations, but stopping an existing stream
	// must depend on delivered bytes. Otherwise the sweep can cancel the write
	// that reserved the final allowance before that write has completed.
	if l != nil && l.counters != nil && l.counters.SeedReady() && key.UserID != "" {
		if settled, ok := l.counters.settledUserBytes(key.UserID); ok {
			used = settled
		}
	}
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
	l.nowMu.Lock()
	defer l.nowMu.Unlock()
	l.now = now
}

// maxRateEntries bounds the request-schedule table.
//
// A key that falls out of it is admitted again from a clean schedule, which is
// the safe direction to fail: a plan's rate limit is not worth unbounded memory
// on a path where minting keys is how an account attacks the process.
const maxRateEntries = 65536

// rateTable is the per-key request schedule behind MaxRPS, kept in the generic
// cell rate form: one integer per key holding the time its next request is due.
//
// A token bucket is the other way to say the same thing and needs two integers
// and a refill rule; this needs one and no refill. It also has no window
// boundary, so a caller cannot send the whole limit twice around the edge of a
// whole second.
type rateTable struct {
	mu      sync.Mutex
	entries map[string]int64
}

// allow reports whether the key may start one more request.
//
// intervalNs is the spacing its limit implies and burstNs how far ahead of
// schedule a request may run, which is the allowance for a burst at the start
// of a busy period. Both come from the key's own plan, so two keys on different
// plans never share a budget. A zero interval means the limit is above one
// request per nanosecond, which is no limit at all.
func (t *rateTable) allow(name string, intervalNs, burstNs, nowNs int64) bool {
	if intervalNs <= 0 {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]int64)
	}
	due, scheduled := t.entries[name]
	if !scheduled || due < nowNs {
		// A key seen for the first time, or one whose schedule has fallen
		// behind the clock, starts from now: an idle key owes nothing for the
		// time it was quiet.
		due = nowNs
	}
	next := due + intervalNs
	if next-nowNs > burstNs {
		return false
	}
	t.entries[name] = next
	if len(t.entries) > maxRateEntries {
		t.prune(nowNs)
	}
	return true
}

// prune drops every schedule that has already caught up with the clock. When
// that is not enough - every entry is live - the table is emptied rather than
// grown: the worst case is one extra burst per prune, and the alternative is a
// map an attacker can inflate one key at a time.
func (t *rateTable) prune(nowNs int64) {
	for name, due := range t.entries {
		if due <= nowNs {
			delete(t.entries, name)
		}
	}
	if len(t.entries) > maxRateEntries {
		clear(t.entries)
	}
}

// rateKey names the budget a key's requests are charged against. The key's own
// id is the budget, so two keys of one user do not share a rate; a state without
// one falls back to the user, and a state with neither is not rate limited at
// all because there is nothing to charge.
func rateKey(key *keys.KeyState) string {
	if key.KeyID != "" {
		return key.KeyID
	}
	return key.UserID
}
