package proxy

import (
	"net"
	"sync/atomic"
	"time"
)

// tlsLatencyConn wraps a net.Conn to measure the round trip between the first
// write and the first read on that connection.
//
// The name is narrower than the behaviour: for a TLS tunnel the first write is
// the Client Hello and the first read the Server Hello, so the sample really is
// the handshake latency. For a plaintext tunnel (an HTTP CONNECT that forwards
// cleartext, SOCKS5) the same interval is the time to first byte of the
// application exchange. The wrapper cannot tell the two apart - prepareConnectTunnel
// only knows "host:port", and a CONNECT request never states whether the target
// speaks TLS - so the samples are recorded under one label either way.
//
// Both quantities are "one round trip plus server processing" for the same node
// and domain, so the TD-EWMA ordering they feed stays meaningful; the caveat is
// that a node serving both kinds of traffic mixes two distributions in one
// table. Distinguishing them would need first-byte sniffing (a TLS record starts
// 0x16 0x03) with the timer started on demand, which is not worth it unless a
// bimodal latency distribution is actually observed.
//
// State machine (atomic uint32):
//
//	0 = init (waiting for first write)
//	1 = handshake_started (Client Hello written, waiting for first read)
//	2 = done (Server Hello received, latency captured)
type tlsLatencyConn struct {
	net.Conn
	state     uint32 // atomic
	startTime int64  // atomic, UnixNano
	onLatency func(time.Duration)
}

func newTLSLatencyConn(c net.Conn, onLatency func(time.Duration)) *tlsLatencyConn {
	return &tlsLatencyConn{
		Conn:      c,
		onLatency: onLatency,
	}
}

func (c *tlsLatencyConn) Write(b []byte) (int, error) {
	// Fast path: handshake already started or done.
	if atomic.LoadUint32(&c.state) != 0 {
		return c.Conn.Write(b)
	}

	n, err := c.Conn.Write(b)

	// Capture first write (Client Hello) — start timer.
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 0, 1) {
			atomic.StoreInt64(&c.startTime, time.Now().UnixNano())
		}
	}
	return n, err
}

func (c *tlsLatencyConn) Read(b []byte) (int, error) {
	// Fast path: not in handshake phase.
	if atomic.LoadUint32(&c.state) != 1 {
		return c.Conn.Read(b)
	}

	n, err := c.Conn.Read(b)

	// Capture first read (Server Hello) — stop timer.
	if n > 0 && err == nil {
		if atomic.CompareAndSwapUint32(&c.state, 1, 2) {
			startNano := atomic.LoadInt64(&c.startTime)
			if startNano > 0 {
				latency := time.Duration(time.Now().UnixNano() - startNano)
				if c.onLatency != nil {
					go c.onLatency(latency)
				}
			}
		}
	}
	return n, err
}

func (c *tlsLatencyConn) CloseWrite() error {
	return closeWriteErr(c.Conn)
}

func (c *tlsLatencyConn) CloseRead() error {
	return closeReadErr(c.Conn)
}
