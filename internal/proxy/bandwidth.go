package proxy

import (
	"sync"
	"time"

	"prism/internal/keys"
)

// Bandwidth limiting.
//
// MaxBandwidthBps was written by the console, stored, copied into the key
// snapshot and published to the data plane, and read by nothing: a plan that
// advertised a megabyte a second moved as fast as the link allowed. This file
// is the reader.
//
// The shape is the one the request limiter uses, counted in bytes instead of
// requests: one integer per key holding when the next byte is due, so a key
// costs a map entry rather than a token counter and no refill goroutine runs. A
// key starts with one second of credit, which is what lets a small response
// leave immediately instead of paying for its bytes in advance.

const (
	// bandwidthBurstNs is the credit a key starts with and the most it can fall
	// behind. One second is long enough that a page load does not pay up front
	// and short enough that a throttled transfer is visibly throttled rather
	// than merely slower on average.
	bandwidthBurstNs = int64(time.Second)

	// bandwidthTableMax bounds the schedule table. A key that falls out of it
	// starts again from the full burst, which admits at most one extra second
	// of traffic - the safe direction, because the alternative is a map that
	// grows with every key an account mints.
	bandwidthTableMax = 65536

	// bandwidthSleepStep bounds how long one throttle sleeps before it looks at
	// the connection again. A transfer closed mid-throttle must not hold its
	// goroutine for the whole remainder.
	bandwidthSleepStep = 100 * time.Millisecond
)

// bandwidthTable is the per-key byte schedule.
type bandwidthTable struct {
	mu  sync.Mutex
	due map[string]int64
}

// sharedBandwidth is the table the data plane uses.
//
// It is one table rather than one per connection because the limit belongs to
// the key: two tunnels of the same key have to share the rate, or a client that
// opens more connections buys more bandwidth than its plan allows.
var sharedBandwidth = &bandwidthTable{}

// reserve accounts n bytes of the named key's budget and reports how long the
// caller must wait before the network is asked for more.
//
// rateBps is the plan's limit. A zero rate, and a zero byte count, wait for
// nothing, which is the path every deployment without bandwidth limits takes.
func (t *bandwidthTable) reserve(name string, n, rateBps, nowNs int64) time.Duration {
	if t == nil || rateBps <= 0 || n <= 0 || name == "" {
		return 0
	}
	cost := n * int64(time.Second) / rateBps
	if cost <= 0 {
		cost = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.due == nil {
		t.due = make(map[string]int64)
	}
	due, scheduled := t.due[name]
	if !scheduled || due < nowNs-bandwidthBurstNs {
		// A key seen for the first time, or one idle for longer than the credit
		// it may hold, starts from the full second of credit.
		due = nowNs - bandwidthBurstNs
	}
	due += cost
	t.due[name] = due
	if len(t.due) > bandwidthTableMax {
		t.pruneLocked(nowNs)
	}
	if delay := due - nowNs; delay > 0 {
		return time.Duration(delay)
	}
	return 0
}

// pruneLocked drops the schedules that have already caught up with the clock.
// When that is not enough - every entry is live - the table is emptied rather
// than grown, which costs at most one extra burst.
func (t *bandwidthTable) pruneLocked(nowNs int64) {
	for name, due := range t.due {
		if due <= nowNs {
			delete(t.due, name)
		}
	}
	if len(t.due) > bandwidthTableMax {
		clear(t.due)
	}
}

// bandwidthKey names the budget a connection's bytes are charged against. Two
// keys of one user are two budgets, exactly as they are for the request rate.
func bandwidthKey(key *keys.KeyState) string {
	if key == nil {
		return ""
	}
	if key.KeyID != "" {
		return key.KeyID
	}
	return key.UserID
}

// hasBandwidthLimit reports whether the key's plan throttles at all.
func hasBandwidthLimit(key *keys.KeyState) bool {
	return key != nil && key.Plan.MaxBandwidthBps > 0 && bandwidthKey(key) != ""
}

// throttleTo applies the key's bandwidth limit to this connection and returns
// it, so a call site can wrap in one expression.
//
// A plan without a limit leaves every field zero and costs the read and write
// paths one comparison each.
func (c *countingConn) throttleTo(key *keys.KeyState) *countingConn {
	if c == nil || !hasBandwidthLimit(key) {
		return c
	}
	c.bandwidth = sharedBandwidth
	c.bandwidthRateBps = key.Plan.MaxBandwidthBps
	c.bandwidthKey = bandwidthKey(key)
	return c
}

// throttleBytes waits for the bytes that just moved to come due.
//
// It runs after the transfer rather than before: the first chunk of a response
// is already on the socket by the time this can stop the next one, and paying a
// delay up front would add latency to every request that turns out to be small.
// The second of starting credit is what makes that trade exact rather than
// generous.
func (c *countingConn) throttleBytes(n int) {
	if c == nil || n <= 0 || c.bandwidth == nil || c.bandwidthRateBps <= 0 || c.bandwidthKey == "" {
		return
	}
	c.settleBandwidth(c.bandwidth.reserve(
		c.bandwidthKey, int64(n), c.bandwidthRateBps, time.Now().UnixNano()))
}

// settleBandwidth sleeps for delay in steps, so a connection closed mid-throttle
// releases its goroutine within one step instead of after the whole remainder.
func (c *countingConn) settleBandwidth(delay time.Duration) {
	for delay > 0 {
		if c.closed.Load() {
			return
		}
		step := delay
		if step > bandwidthSleepStep {
			step = bandwidthSleepStep
		}
		time.Sleep(step)
		delay -= step
	}
}
