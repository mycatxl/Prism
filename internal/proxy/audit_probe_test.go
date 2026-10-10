package proxy

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for F3: the wrapper reverse.go:424 installs on a 101
// upgrade response body must stay transparent for half-close, and its byte
// counters must be safe under the two-goroutine copy net/http/httputil runs.
//
// closeWriterProbe is a minimal stream that advertises the optional half-close
// capability every other wrapper in this package forwards.
type closeWriterProbe struct {
	net.Conn
	mu              sync.Mutex
	closeWriteCalls int
}

func (c *closeWriterProbe) CloseWrite() error {
	c.mu.Lock()
	c.closeWriteCalls++
	c.mu.Unlock()
	return nil
}

func (c *closeWriterProbe) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeWriteCalls
}

// discardStream is a working bidirectional stream: writes are counted, reads
// return EOF. It exists so the counting wrapper is exercised against a real
// implementation rather than a nil embedded interface.
type discardStream struct {
	mu    sync.Mutex
	bytes int
}

func (d *discardStream) Read([]byte) (int, error) { return 0, io.EOF }

func (d *discardStream) Write(p []byte) (int, error) {
	d.mu.Lock()
	d.bytes += len(p)
	d.mu.Unlock()
	return len(p), nil
}

func (d *discardStream) Close() error { return nil }

// TestAudit_UpgradeStreamWrapperKeepsHalfClose pins the contract reverse.go:424
// relies on:
//
//	upgradedStreamCounter = newCountingReadWriteCloser(rwc)
//	resp.Body = upgradedStreamCounter
//
// net/http/httputil's switchProtocolCopier does
//
//	if wc, ok := c.backend.(interface{ CloseWrite() error }); ok {
//	    errc <- wc.CloseWrite(); return
//	}
//	errc <- errCopyDone
//
// so a wrapper that loses CloseWrite turns a clean half-close into errCopyDone,
// and handleUpgradeResponse's "err := <-errc; if err == nil { err = <-errc }"
// returns while the opposite direction is still copying - the deferred
// conn.Close()/backConnCloseCh then tears down both connections.
//
// Every other wrapper in this package forwards CloseWrite (counting_conn.go:110,
// connCloseNotifier:156, tlsLatencyConn:71, prebufferedConn/closeHookConn in
// cmd/prism/inbound_demux.go). This wrapper must too.
func TestAudit_UpgradeStreamWrapperKeepsHalfClose(t *testing.T) {
	// A concrete stream that does support half-close, as the transport's
	// readWriteCloserBody does.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	inner := &halfCloseStream{Conn: server}

	var body io.ReadWriteCloser = inner
	wrapped := newCountingReadWriteCloser(body)

	// The type assertion httputil performs on the upgrade backend.
	closeWriter, ok := interface{}(wrapped).(interface{ CloseWrite() error })
	if !ok {
		t.Errorf("the 101 upgrade body wrapper lost CloseWrite: httputil's "+
			"switchProtocolCopier falls back to errCopyDone and closes both "+
			"connections while the upstream stream is still open (wrapped type %T)",
			wrapped)
		return
	}
	if err := closeWriter.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if inner.calls() != 1 {
		t.Errorf("the inner stream saw %d CloseWrite calls, want 1", inner.calls())
	}
}

// TestAudit_UpgradeStreamWrapperHalfCloseSemantics pins the two delegation
// rules of the wrapper's half-close pair:
//
//   - a backend that supports half-close sees the call, so a real shutdown
//     failure is still reported to httputil;
//   - a backend that does not must be reported as success. Returning
//     errHalfCloseUnsupported here would look like a fix while keeping the
//     defect: httputil reads any non-nil error as "copy finished" and tears the
//     tunnel down.
func TestAudit_UpgradeStreamWrapperHalfCloseSemantics(t *testing.T) {
	t.Run("supported backend is delegated to", func(t *testing.T) {
		backend := &closeWriterProbe{}
		wrapped := newCountingReadWriteCloser(backend)
		closeWriter, ok := interface{}(wrapped).(interface{ CloseWrite() error })
		if !ok {
			t.Fatalf("the wrapper does not implement CloseWrite (type %T)", wrapped)
		}
		if err := closeWriter.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
		if got := backend.calls(); got != 1 {
			t.Fatalf("the backend saw %d CloseWrite calls, want 1", got)
		}
	})

	t.Run("unsupported backend reports success", func(t *testing.T) {
		backend := &discardStream{}
		wrapped := newCountingReadWriteCloser(backend)
		closeWriter, ok := interface{}(wrapped).(interface{ CloseWrite() error })
		if !ok {
			t.Fatalf("the wrapper does not implement CloseWrite (type %T)", wrapped)
		}
		if err := closeWriter.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite on a backend without half-close support = %v, want nil: "+
				"httputil sends errCopyDone on any non-nil error and closes both connections", err)
		}
		closeReader, ok := interface{}(wrapped).(interface{ CloseRead() error })
		if !ok {
			t.Fatalf("the wrapper does not implement CloseRead (type %T)", wrapped)
		}
		if err := closeReader.CloseRead(); err != nil {
			t.Fatalf("CloseRead on a backend without half-close support = %v, want nil", err)
		}
	})

	t.Run("nil wrapper reports success", func(t *testing.T) {
		var wrapped *countingReadWriteCloser
		closeWriter, ok := interface{}(wrapped).(interface{ CloseWrite() error })
		if !ok {
			t.Fatalf("the wrapper does not implement CloseWrite (type %T)", wrapped)
		}
		if err := closeWriter.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite on a nil wrapper = %v, want nil", err)
		}
		closeReader, ok := interface{}(wrapped).(interface{ CloseRead() error })
		if !ok {
			t.Fatalf("the wrapper does not implement CloseRead (type %T)", wrapped)
		}
		if err := closeReader.CloseRead(); err != nil {
			t.Fatalf("CloseRead on a nil wrapper = %v, want nil", err)
		}
	})
}

// TestAudit_UpgradeCopySurvivesClientHalfClose replays httputil's upgrade copy
// against the wrapper with a real TCP pair: the client finishes its request and
// half-closes while the upstream is still streaming its response. Before the
// fix the failed CloseWrite assertion made the copier report errCopyDone, the
// emulated handleUpgradeResponse returned on that first value, and its deferred
// teardown discarded the rest of the upstream stream.
func TestAudit_UpgradeCopySurvivesClientHalfClose(t *testing.T) {
	const requestPayload = "upgrade-request"
	partOne := strings.Repeat("a", 4096)
	partTwo := strings.Repeat("b", 4096)

	// Proxy-side upstream connection, standing in for the 101 response body.
	upstreamLn := listenLoopback(t)
	upstreamProxyConn := acceptFrom(t, upstreamLn)
	upstreamPeer, err := net.Dial("tcp", upstreamLn.Addr().String())
	if err != nil {
		t.Fatalf("dial upstream: %v", err)
	}
	defer upstreamPeer.Close()
	upstreamConn := <-upstreamProxyConn
	if upstreamConn == nil {
		t.Fatal("accepting the upstream side of the proxy connection failed")
	}
	defer upstreamConn.Close()
	backend := newCountingReadWriteCloser(upstreamConn)

	// Proxy-side client connection, standing in for the hijacked conn.
	clientLn := listenLoopback(t)
	clientProxyConn := acceptFrom(t, clientLn)
	clientPeer, err := net.Dial("tcp", clientLn.Addr().String())
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer clientPeer.Close()
	userConn := <-clientProxyConn
	if userConn == nil {
		t.Fatal("accepting the client side of the proxy connection failed")
	}
	defer userConn.Close()

	deadline := time.Now().Add(10 * time.Second)
	for _, conn := range []net.Conn{upstreamConn, userConn, upstreamPeer, clientPeer} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatalf("SetDeadline: %v", err)
		}
	}

	requestReceived := make(chan struct{})
	sendPartTwo := make(chan struct{})
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		buf := make([]byte, len(requestPayload))
		if _, err := io.ReadFull(upstreamPeer, buf); err != nil {
			t.Errorf("upstream read request: %v", err)
			return
		}
		if string(buf) != requestPayload {
			t.Errorf("upstream request payload: got %q, want %q", string(buf), requestPayload)
			return
		}
		close(requestReceived)
		if _, err := upstreamPeer.Write([]byte(partOne)); err != nil {
			t.Errorf("upstream write part one: %v", err)
			return
		}
		<-sendPartTwo
		// The proxy may already have torn the backend down; a write error is
		// then the expected consequence, not a test failure.
		_, _ = upstreamPeer.Write([]byte(partTwo))
	}()

	// The copier httputil runs, including its errc protocol.
	errCopyDoneProbe := errCopyDoneSentinel{}
	errc := make(chan error, 1)
	go func() {
		if _, copyErr := io.Copy(backend, userConn); copyErr != nil {
			errc <- copyErr
			return
		}
		if wc, ok := interface{}(backend).(interface{ CloseWrite() error }); ok {
			errc <- wc.CloseWrite()
			return
		}
		errc <- errCopyDoneProbe
	}()
	go func() {
		if _, copyErr := io.Copy(userConn, backend); copyErr != nil {
			errc <- copyErr
			return
		}
		if wc, ok := interface{}(userConn).(interface{ CloseWrite() error }); ok {
			errc <- wc.CloseWrite()
			return
		}
		errc <- errCopyDoneProbe
	}()

	if _, err := clientPeer.Write([]byte(requestPayload)); err != nil {
		t.Fatalf("client write request: %v", err)
	}
	<-requestReceived

	clientTCP, ok := clientPeer.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn type: got %T, want *net.TCPConn", clientPeer)
	}
	if err := clientTCP.CloseWrite(); err != nil {
		t.Fatalf("client half-close: %v", err)
	}

	// handleUpgradeResponse: the first non-nil value makes it return, and its
	// deferred teardown then closes both connections.
	if first := <-errc; first != nil {
		_ = userConn.Close()
		_ = backend.Close()
		t.Errorf("the upgrade copier reported %v on the client half-close "+
			"(errCopyDone=%v), which makes httputil tear down both connections",
			first, first == errCopyDoneProbe)
	}

	received := make([]byte, len(partOne))
	if _, err := io.ReadFull(clientPeer, received); err != nil {
		t.Fatalf("client read part one: %v", err)
	}
	if string(received) != partOne {
		t.Fatalf("client read part one: got %d bytes of %q, want %d bytes of %q",
			len(received), string(received[:min(len(received), 32)]), len(partOne), partOne[:32])
	}

	close(sendPartTwo)
	received = make([]byte, len(partTwo))
	if _, err := io.ReadFull(clientPeer, received); err != nil {
		t.Fatalf("client read part two after the half-close: %v", err)
	}
	if string(received) != partTwo {
		t.Fatalf("client read part two: got %d bytes, want %d", len(received), len(partTwo))
	}
	<-upstreamDone
}

// errCopyDoneSentinel mirrors net/http/httputil's unexported errCopyDone: the
// error the copier sends when the backend cannot half-close.
type errCopyDoneSentinel struct{}

func (errCopyDoneSentinel) Error() string { return "hijacked connection copy complete" }

// listenLoopback starts a loopback listener that the test closes on cleanup.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// acceptFrom accepts one connection in the background and reports it on the
// returned channel, so the test can keep its own ordering.
func acceptFrom(t *testing.T, ln net.Listener) <-chan net.Conn {
	t.Helper()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()
	return accepted
}

// halfCloseStream is a net.Conn that records CloseWrite, standing in for the
// transport body httputil hands to the upgrade copier.
type halfCloseStream struct {
	net.Conn
	mu     sync.Mutex
	writes int
}

func (h *halfCloseStream) CloseWrite() error {
	h.mu.Lock()
	h.writes++
	h.mu.Unlock()
	return nil
}

func (h *halfCloseStream) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writes
}

// TestAudit_UpgradeStreamWrapperCountsRace drives the counting wrapper the way
// httputil does - one goroutine copying each direction - while the proxy's own
// goroutine reads the totals, exactly as reverse.go:481-484 does after
// ServeHTTP returns. The counters must be atomic: with plain int64 fields this
// test reports a DATA RACE under -race.
func TestAudit_UpgradeStreamWrapperCountsRace(t *testing.T) {
	stream := &discardStream{}
	wrapped := newCountingReadWriteCloser(stream)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			_, _ = wrapped.Write([]byte("x"))
		}
	}()

	// The proxy reads the totals while the copy is still in flight.
	observed := 0
	for i := 0; i < 20000; i++ {
		observed = int(wrapped.TotalWrite())
	}
	wg.Wait()
	t.Logf("TotalWrite observed during the copy = %d, final = %d",
		observed, wrapped.TotalWrite())
	if final := wrapped.TotalWrite(); final != 20000 {
		t.Fatalf("TotalWrite after the copy = %d, want 20000", final)
	}
}
