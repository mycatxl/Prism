package proxy

import (
	"io"
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
//
// The limit belongs to the key, not to a connection, so every path that moves
// a key's bytes has to charge the same table: the tunnel paths through the
// counting connection they wrap their upstream socket with, and the HTTP paths
// (forward and reverse) through a bandwidthGate installed on the body copies,
// which is the only place a pooled transport's bytes pass through this package.

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

// bandwidthNow and bandwidthSleep are the clock and the wait a throttle uses.
//
// They are variables so a test can prove the pacing without waiting for it: the
// mid-transfer assertions in bandwidth_http_test.go count the virtual seconds a
// limited copy paid and compare them with an unlimited baseline, which cannot
// be stated with a real clock without making the test either slow or flaky. The
// same seam style is used for trafficFlushInterval (see counting_conn.go).
var (
	bandwidthNow   = func() int64 { return time.Now().UnixNano() }
	bandwidthSleep = time.Sleep
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
// opens more connections buys more bandwidth than its plan allows. The HTTP
// paths charge the same table for the same reason.
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

// bandwidthGate is one transfer's share of a key's byte budget.
//
// It exists so the rate can be charged from anywhere the bytes of a transfer
// pass through, not only from a net.Conn wrapper: an HTTP request's bodies are
// read and written by net/http's transport and by httputil's copy loops, on
// pooled connections that outlive the request and therefore cannot carry the
// key. The body wrappers below install this gate on exactly those copies, and it
// charges the same sharedBandwidth table the tunnels charge.
//
// stopped reports that the transfer is over, so a wait still in progress can
// stop within one bandwidthSleepStep instead of holding its goroutine to the
// end. A tunnel gate checks its connection's Close; an HTTP gate checks the
// request context, because net/http owns the connection and it survives the
// request.
type bandwidthGate struct {
	table   *bandwidthTable
	name    string
	rateBps int64
	stopped func() bool
}

// newBandwidthGate returns the gate the key's plan asks for, or nil when it
// asks for nothing: an unlimited plan must cost the copy paths one comparison,
// exactly as an unlimited plan costs a tunnel one comparison.
func newBandwidthGate(key *keys.KeyState, stopped func() bool) *bandwidthGate {
	if !hasBandwidthLimit(key) {
		return nil
	}
	return &bandwidthGate{
		table:   sharedBandwidth,
		name:    bandwidthKey(key),
		rateBps: key.Plan.MaxBandwidthBps,
		stopped: stopped,
	}
}

// throttle waits for the bytes that just moved to come due.
//
// It runs after the transfer rather than before: the first chunk of a response
// is already on the socket by the time this can stop the next one, and paying a
// delay up front would add latency to every request that turns out to be small.
// The second of starting credit is what makes that trade exact rather than
// generous.
func (g *bandwidthGate) throttle(n int) {
	if g == nil || n <= 0 || g.table == nil || g.rateBps <= 0 || g.name == "" {
		return
	}
	g.settle(g.table.reserve(g.name, int64(n), g.rateBps, bandwidthNow()))
}

// settle sleeps for delay in steps, so a transfer closed mid-throttle releases
// its goroutine within one step instead of after the whole remainder.
func (g *bandwidthGate) settle(delay time.Duration) {
	for delay > 0 {
		if g.stopped != nil && g.stopped() {
			return
		}
		step := delay
		if step > bandwidthSleepStep {
			step = bandwidthSleepStep
		}
		bandwidthSleep(step)
		delay -= step
	}
}

// throttleTo applies the key's bandwidth limit to this connection and returns
// it, so a call site can wrap in one expression.
//
// A plan without a limit leaves the field nil and costs the read and write paths
// one comparison each.
func (c *countingConn) throttleTo(key *keys.KeyState) *countingConn {
	if c == nil {
		return c
	}
	c.bandwidth = newBandwidthGate(key, c.isClosed)
	return c
}

// isClosed is the gate's early-release probe for a connection.
func (c *countingConn) isClosed() bool {
	return c != nil && c.closed.Load()
}

// throttleBytes charges the bytes a connection just moved.
func (c *countingConn) throttleBytes(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.bandwidth.throttle(n)
}

// bandwidthReadCloser paces a body this package copies itself: a forward
// proxy's request and response bodies and a reverse proxy's copies of the same.
//
// The chunk is charged after it moved, exactly as the connection wrapper
// charges a tunnel's bytes, so a body that turns out to be small never pays in
// advance for a limit it never reached.
type bandwidthReadCloser struct {
	io.ReadCloser
	gate *bandwidthGate
}

func (c *bandwidthReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		c.gate.throttle(n)
	}
	return n, err
}

// throttleBody installs the gate on a body, or returns the body untouched when
// the plan does not limit bandwidth.
func throttleBody(body io.ReadCloser, gate *bandwidthGate) io.ReadCloser {
	if gate == nil || body == nil {
		return body
	}
	return &bandwidthReadCloser{ReadCloser: body, gate: gate}
}
