package proxy

import (
	"io"
	"net"
	"testing"
	"time"

	"prism/internal/keys"
)

// bandwidthStubConn is a net.Conn that counts writes and never blocks, so the
// throttle can be exercised without a kernel in the loop.
type bandwidthStubConn struct {
	net.Conn
	writes int
	reads  int
}

func (c *bandwidthStubConn) Write(b []byte) (int, error) { c.writes++; return len(b), nil }

func (c *bandwidthStubConn) Read(b []byte) (int, error) {
	c.reads++
	if len(b) == 0 {
		return 0, io.EOF
	}
	b[0] = 'x'
	return 1, nil
}

func (c *bandwidthStubConn) Close() error { return nil }

func bandwidthTestKey(rate int64) *keys.KeyState {
	return &keys.KeyState{
		KeyID:  "k-bw",
		UserID: "u-bw",
		Plan:   keys.PlanSnapshot{MaxBandwidthBps: rate},
	}
}

// dueFor reads the schedule the table holds for one key.
func dueFor(t *testing.T, table *bandwidthTable, name string) int64 {
	t.Helper()
	table.mu.Lock()
	defer table.mu.Unlock()
	return table.due[name]
}

// A key starts with one second of credit, so a small response leaves at once
// and the limit binds from the second second onwards.
func TestBandwidthTable_ReserveStartsWithOneSecondOfCredit(t *testing.T) {
	table := &bandwidthTable{}
	now := int64(1_700_000_000) * int64(time.Second)

	if delay := table.reserve("k-1", 1000, 1000, now); delay != 0 {
		t.Fatalf("the first second of a 1000 B/s limit must not wait: %d", delay)
	}
	if delay := table.reserve("k-1", 1000, 1000, now); delay != time.Second {
		t.Fatalf("the second second has to wait for the credit it spent: %v", delay)
	}
}

// The budget belongs to the key: one key's bytes do not slow another's down.
func TestBandwidthTable_ReserveIsPerKey(t *testing.T) {
	table := &bandwidthTable{}
	now := int64(1_700_000_000) * int64(time.Second)

	table.reserve("k-1", 4000, 1000, now)
	if delay := table.reserve("k-2", 1000, 1000, now); delay != 0 {
		t.Fatalf("a second key must start with its own credit: %v", delay)
	}
	if delay := table.reserve("k-1", 1000, 1000, now); delay != 4*time.Second {
		t.Fatalf("the first key has spent four seconds of credit: %v", delay)
	}
}

// An idle key owes nothing for the time it was quiet: the credit is capped, not
// accumulated, so a key that was silent for a day does not get a day of burst.
func TestBandwidthTable_ReserveCapsTheCreditAfterIdle(t *testing.T) {
	table := &bandwidthTable{}
	now := int64(1_700_000_000) * int64(time.Second)

	table.reserve("k-1", 500, 1000, now)
	later := now + int64(10*time.Second)
	if delay := table.reserve("k-1", 1000, 1000, later); delay != 0 {
		t.Fatalf("a full second of credit after an idle spell must be free: %v", delay)
	}
}

// A plan without a bandwidth limit installs nothing, so the read and write
// paths keep their one comparison and no table is touched.
func TestCountingConn_ThrottleToIgnoresAnUnlimitedPlan(t *testing.T) {
	conn := newAttributedCountingConn(&bandwidthStubConn{}, nil, nil)
	same := conn.throttleTo(bandwidthTestKey(0))
	if same != conn || conn.bandwidth != nil || conn.bandwidthRateBps != 0 || conn.bandwidthKey != "" {
		t.Fatalf("an unlimited plan must leave the connection untouched: %+v", conn)
	}
}

func TestCountingConn_ThrottleToInstallsThePlan(t *testing.T) {
	conn := newAttributedCountingConn(&bandwidthStubConn{}, nil, nil)
	conn.throttleTo(bandwidthTestKey(2_000_000))
	if conn.bandwidth != sharedBandwidth {
		t.Fatalf("the key's schedule has to be the shared table")
	}
	if conn.bandwidthRateBps != 2_000_000 || conn.bandwidthKey != "k-bw" {
		t.Fatalf("the plan's rate and the key's budget were not installed: %+v", conn)
	}
}

// Both directions of a tunnel are charged: a plan's bandwidth is what the
// session moves, not what it downloads.
func TestCountingConn_ChargesBothDirections(t *testing.T) {
	table := &bandwidthTable{}
	stub := &bandwidthStubConn{}
	conn := &countingConn{Conn: stub, bandwidth: table, bandwidthRateBps: 1_000_000, bandwidthKey: "k-bw"}
	// The clock is not injected into countingConn, so the assertions are about
	// the schedule moving rather than about the delay it returns.
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	afterWrite := dueFor(t, table, "k-bw")
	if afterWrite == 0 {
		t.Fatalf("a write has to be charged to the key")
	}
	if _, err := conn.Read(make([]byte, 16)); err != nil {
		t.Fatalf("read: %v", err)
	}
	afterRead := dueFor(t, table, "k-bw")
	if afterRead <= afterWrite {
		t.Fatalf("a read has to be charged too: %d then %d", afterWrite, afterRead)
	}
}
