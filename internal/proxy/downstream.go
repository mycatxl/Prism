package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// A transport context only stops upstream I/O. The server can still be blocked
// reading an upload or writing to a client that stopped reading. Interrupt that
// I/O on cancellation, and take ownership of upgrades at Hijack, before even
// the 101 headers are flushed.
type downstreamWriter struct {
	http.ResponseWriter
	ctx      context.Context
	mu       sync.Mutex
	hijacked net.Conn
}

func watchDownstream(ctx context.Context, w http.ResponseWriter) (http.ResponseWriter, func()) {
	wrapped := &downstreamWriter{ResponseWriter: w, ctx: ctx}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		wrapped.mu.Lock()
		conn := wrapped.hijacked
		wrapped.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now())
		// Give a refusal produced by cancellation time to leave the server,
		// while bounding an already-blocked downstream write.
		_ = controller.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	})
	return wrapped, func() {
		if stop() {
			return
		}
		<-done
		wrapped.mu.Lock()
		hijacked := wrapped.hijacked != nil
		wrapped.mu.Unlock()
		if !hijacked {
			// Join the callback before clearing its deadlines so they cannot
			// escape into another request on a reusable HTTP connection.
			controller := http.NewResponseController(w)
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func (w *downstreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *downstreamWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *downstreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	w.hijacked = conn
	w.mu.Unlock()
	// Cancellation may have run before Hijack reset the server's deadlines.
	if w.ctx.Err() != nil {
		_ = conn.Close()
	}
	return conn, rw, nil
}
