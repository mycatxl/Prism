package main

import (
	"net"
	"sync"
	"time"
)

// Bound sockets before creating sniff workers, HTTP goroutines or tunnel
// buffers. Queued connections remain in the OS backlog until a slot is free.
const inboundMaxConnections = 1024
const inboundIdleTimeout = 2 * time.Minute

type boundedListener struct {
	net.Listener
	slots       chan struct{}
	done        chan struct{}
	once        sync.Once
	closeErr    error
	idleTimeout time.Duration
}

func limitInboundConnections(listener net.Listener, limit int) *boundedListener {
	return &boundedListener{Listener: listener, slots: make(chan struct{}, limit), done: make(chan struct{}), idleTimeout: inboundIdleTimeout}
}

func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case l.slots <- struct{}{}:
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	// Keep half-close support through the wrapper, and release exactly once
	// even when shutdown and the owning handler both close the connection.
	return newIdleConn(newCloseHookConn(conn, func() { <-l.slots }), l.idleTimeout), nil
}

func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done); l.closeErr = l.Listener.Close() })
	return l.closeErr
}

// A sliding socket inactivity bound covers uploads, downloads and hijacked
// streams without imposing a total duration on a progressing transfer. It is
// independent of the protocol's stricter header/body/handshake deadlines.
type idleConn struct {
	net.Conn
	mu           sync.Mutex
	timer        *time.Timer
	lastActivity time.Time
	timeout      time.Duration
	closed       bool
}

func newIdleConn(conn net.Conn, timeout time.Duration) *idleConn {
	c := &idleConn{Conn: conn, lastActivity: time.Now(), timeout: timeout}
	c.timer = time.AfterFunc(timeout, c.expire)
	return c
}

func (c *idleConn) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.lastActivity = time.Now()
		c.timer.Reset(c.timeout)
	}
}
func (c *idleConn) expire() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if remaining := c.timeout - time.Since(c.lastActivity); remaining > 0 {
		c.timer.Reset(remaining)
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.Conn.Close()
}
func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}
func (c *idleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}
func (c *idleConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.timer.Stop()
	c.mu.Unlock()
	return c.Conn.Close()
}
func (c *idleConn) CloseWrite() error { return closeWriteErr(c.Conn) }
func (c *idleConn) CloseRead() error  { return closeReadErr(c.Conn) }
