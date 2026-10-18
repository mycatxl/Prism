package main

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// panickingConnHandler stands in for a SOCKS5 inbound whose ServeConnContext
// panics, which is the case a worker goroutine has no other recoverer for.
type panickingConnHandler struct {
	once sync.Once
	hit  chan struct{}
}

func (h *panickingConnHandler) ServeConnContext(context.Context, net.Conn) {
	h.once.Do(func() { close(h.hit) })
	panic("boom in a socks5 session")
}

// A panic while serving an accepted connection must end that one connection,
// not the process. The demux runs handleAcceptedConn on a worker goroutine,
// where net/http's own recovery does not apply.
func TestInboundDemuxSurvivesAPanickingConnHandler(t *testing.T) {
	handler := &panickingConnHandler{hit: make(chan struct{})}

	srv := newInboundDemuxServer(nil, handler)

	client, server := net.Pipe()
	defer client.Close()

	srv.workerWG.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleAcceptedConn(server)
	}()

	// A SOCKS5 greeting, which is what the demux sniffs for to pick this path.
	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write the greeting: %v", err)
	}

	select {
	case <-handler.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("the SOCKS5 handler was never reached")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleAcceptedConn did not return after the panic")
	}

	// The connection the panic was serving is closed, so the client sees the
	// session end instead of hanging on a half-open tunnel. A closed pipe is
	// the answer the test wants; what must not happen is a read that blocks.
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(client); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the client is still waiting: the connection was not closed")
		}
	}
}
