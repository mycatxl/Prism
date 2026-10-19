package proxy

import (
	"net"
	"sync/atomic"
	"time"
)

// trafficFlushThreshold is the byte count at which a countingConn emits
// a traffic delta mid-stream. This ensures realtime throughput sampling
// and bucket aggregation see traffic during long-lived connections, not only
// at close. Fixed constant — not configurable.
const trafficFlushThreshold int64 = 32768 // 32 KB

// trafficFlushInterval bounds how long sub-threshold pending bytes can remain
// unreported on keep-alive connections. Exposed as var for unit tests.
var trafficFlushInterval = time.Second

// MetricsEventSink receives traffic and connection lifecycle events from the
// proxy layer. Implemented by metrics.Manager (wired in main.go).
// This interface is defined here (in the proxy package) to avoid an import
// cycle between proxy and metrics.
type MetricsEventSink interface {
	// OnTrafficDelta reports a global traffic byte count delta.
	OnTrafficDelta(ingressBytes, egressBytes int64)
	// OnConnectionLifecycle reports a connection open/close event.
	OnConnectionLifecycle(direction ConnectionDirection, op ConnectionOp)
}

// countingConn wraps a net.Conn, counting bytes read/written.
// Flushes a traffic delta every trafficFlushThreshold bytes
// and on Close (for the remainder).
//
// The optional attribution names the access key the bytes belong to. It is set
// only on connections dedicated to one session - the tunnel paths, where the
// upstream connection is dialled per request. The pooled HTTP transports must
// leave it unset: their connections are reused across requests, so attributing
// one to a key would bill the next request's bytes to the wrong tenant.
type countingConn struct {
	net.Conn
	sink        MetricsEventSink
	attribution *keyAttribution

	// bandwidth is the key's byte schedule, installed only when its plan
	// limits bandwidth. A nil gate is the unlimited path and costs the read
	// and write paths one comparison each.
	bandwidth *bandwidthGate

	pendingRead  atomic.Int64
	pendingWrite atomic.Int64
	closed       atomic.Bool
	flushArmed   atomic.Bool
}

// keyAttribution names the key a connection's traffic belongs to, plus the sink
// that records it. It is a pointer so the counting path can test one field.
type keyAttribution struct {
	metering   MeteringSink
	keyID      string
	userID     string
	platformID string
}

func newCountingConn(conn net.Conn, sink MetricsEventSink) *countingConn {
	return &countingConn{
		Conn: conn,
		sink: sink,
	}
}

// newAttributedCountingConn wraps a connection whose traffic belongs to one
// access key, so the bytes are recorded under that key as they flow.
//
// This is what makes a long download stoppable: the counter flushes every
// 32 KB, so a quota that is crossed mid-transfer is visible while the transfer
// is still running, rather than only when it ends.
func newAttributedCountingConn(conn net.Conn, sink MetricsEventSink, attributed *keyAttribution) *countingConn {
	return &countingConn{
		Conn:        conn,
		sink:        sink,
		attribution: attributed,
	}
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		total := c.pendingRead.Add(int64(n))
		if total >= trafficFlushThreshold {
			c.flushPendingTraffic()
		} else {
			c.armDeferredFlush()
		}
		c.throttleBytes(n)
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		total := c.pendingWrite.Add(int64(n))
		if total >= trafficFlushThreshold {
			c.flushPendingTraffic()
		} else {
			c.armDeferredFlush()
		}
		c.throttleBytes(n)
	}
	return n, err
}

func (c *countingConn) armDeferredFlush() {
	if c.closed.Load() {
		return
	}
	if !c.flushArmed.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(trafficFlushInterval, func() {
		c.flushArmed.Store(false)
		if c.closed.Load() {
			return
		}
		c.flushPendingTraffic()
	})
}

func (c *countingConn) flushPendingTraffic() {
	pendR := c.pendingRead.Swap(0)
	pendW := c.pendingWrite.Swap(0)
	if pendR > 0 || pendW > 0 {
		if c.sink != nil {
			c.sink.OnTrafficDelta(pendR, pendW)
		}
		c.reportAttributed(pendR, pendW)
	}
}

// reportAttributed records the delta under this connection's key, if it has
// one.
//
// It runs on the flush path - once per 32 KB or once a second, not per byte -
// so it can afford the bookkeeping a quota needs, and the flush interval is
// what bounds how late an over-quota connection can be noticed.
func (c *countingConn) reportAttributed(ingress, egress int64) {
	attribution := c.attribution
	if attribution == nil || attribution.metering == nil || attribution.keyID == "" {
		return
	}
	attribution.metering.OnKeyTrafficDelta(
		attribution.keyID,
		attribution.userID,
		attribution.platformID,
		ingress,
		egress,
	)
}

func (c *countingConn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil // already closed — idempotent
	}
	// Flush remaining bytes.
	c.flushPendingTraffic()
	if c.sink != nil {
		c.sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionClose)
	}
	return c.Conn.Close()
}

func (c *countingConn) CloseWrite() error {
	return closeWriteErr(c.Conn)
}

func (c *countingConn) CloseRead() error {
	return closeReadErr(c.Conn)
}

// countingListener wraps a net.Listener, emitting connection lifecycle events
// on Accept (open) and on each connection's Close.
type countingListener struct {
	net.Listener
	sink MetricsEventSink
}

// NewCountingListener wraps a listener with connection lifecycle tracking.
func NewCountingListener(ln net.Listener, sink MetricsEventSink) net.Listener {
	if sink == nil {
		return ln
	}
	return &countingListener{Listener: ln, sink: sink}
}

func (cl *countingListener) Accept() (net.Conn, error) {
	conn, err := cl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	cl.sink.OnConnectionLifecycle(ConnectionInbound, ConnectionOpen)
	return &connCloseNotifier{Conn: conn, sink: cl.sink}, nil
}

// connCloseNotifier emits a connection close event on Close.
type connCloseNotifier struct {
	net.Conn
	sink   MetricsEventSink
	closed atomic.Bool
}

func (c *connCloseNotifier) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.sink.OnConnectionLifecycle(ConnectionInbound, ConnectionClose)
	}
	return c.Conn.Close()
}

func (c *connCloseNotifier) CloseWrite() error {
	return closeWriteErr(c.Conn)
}

func (c *connCloseNotifier) CloseRead() error {
	return closeReadErr(c.Conn)
}
