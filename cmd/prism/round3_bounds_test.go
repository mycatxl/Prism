package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"prism/internal/api"
	"prism/internal/model"
)

type round3QueueListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func (l *round3QueueListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *round3QueueListener) Close() error { close(l.done); return nil }
func (*round3QueueListener) Addr() net.Addr { return connChannelAddr("round3") }

func TestRound3ListenerBoundsAdmissionsAndReleasesSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &round3QueueListener{conns: make(chan net.Conn, 2), done: make(chan struct{})}
		for range 2 {
			server, client := net.Pipe()
			defer client.Close()
			base.conns <- server
		}
		listener := limitInboundConnections(base, 1)
		defer listener.Close()
		first, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		accepted := make(chan net.Conn, 1)
		go func() { c, _ := listener.Accept(); accepted <- c }()
		synctest.Wait()
		select {
		case <-accepted:
			t.Fatal("accepted beyond socket cap")
		default:
		}
		_ = first.Close()
		_ = first.Close()
		second := <-accepted
		if second == nil {
			t.Fatal("slot not released")
		}
		errors := make(chan error, 1)
		go func() { _, err := listener.Accept(); errors <- err }()
		synctest.Wait()
		_ = listener.Close()
		if err := <-errors; err != net.ErrClosed {
			t.Fatalf("shutdown did not unblock admission: %v", err)
		}
		_ = second.Close()
		if len(listener.slots) != 0 {
			t.Fatal("socket slots leaked")
		}
	})
}

func TestRound3MuxRefusalsCloseUnfinishedBodies(t *testing.T) {
	for _, target := range []string{"http://example.com/", "/bad-token/plat/account/https/example.com/", "/api/v1/system/info"} {
		handler := newEndpointInboundMux(func() model.Endpoint { return model.Endpoint{} }, "", nil, nil, nil, nil, nil)
		server := httptest.NewServer(handler)
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(conn, "POST "+target+" HTTP/1.1\r\nHost: example.com\r\nContent-Length: 1\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		_ = conn.Close()
		server.Close()
		if err != nil {
			t.Fatalf("%s: refusal blocked on body: %v", target, err)
		}
		if !resp.Close {
			t.Fatalf("%s: refusal retained socket", target)
		}
	}
}

func TestRound3HTTPWireRejectsMalformedHeaders(t *testing.T) {
	cases := []struct {
		name, headers string
		status        int
	}{
		{"conflicting lengths", "Content-Length: 1\r\nContent-Length: 2\r\n", 400},
		{"invalid transfer encoding", "Transfer-Encoding: chunked, identity\r\n", 501},
		{"null value", "X-Test: a\x00b\r\n", 400},
		{"invalid field", "Bad Header: x\r\n", 400},
		{"oversized field", "X-Test: " + strings.Repeat("x", api.MaxHeaderBytes+8192) + "\r\n", 431},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := api.NewListenerServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("malformed request reached handler") }))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: example.com\r\n"+tc.headers+"\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestRound3ChunkedFramingOverridesContentLengthWithoutDesync(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/first" && (r.Header.Get("Content-Length") != "" || r.ContentLength != -1 || len(r.TransferEncoding) != 1 || r.TransferEncoding[0] != "chunked") {
			t.Error("conflicting framing reached handler")
		}
		seen <- r.URL.Path + ":" + string(body)
		w.WriteHeader(204)
	}))
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(conn, "POST /first HTTP/1.1\r\nHost: example.com\r\nContent-Length: 1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\nGET /second HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	reader := bufio.NewReader(conn)
	for range 2 {
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	}
	if first, second := <-seen, <-seen; first != "/first:abc" || second != "/second:" {
		t.Fatalf("framing desynchronized: %q %q", first, second)
	}
}

func TestRound3IdleConnectionAllowsProgressAndClosesSilentPeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer client.Close()
		closed := 0
		conn := newIdleConn(newCloseHookConn(server, func() { closed++ }), time.Minute)
		defer conn.Close()
		for range 3 {
			time.Sleep(40 * time.Second)
			go func() { _, _ = client.Write([]byte("x")) }()
			if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
				t.Fatalf("progressing stream cut: %v", err)
			}
		}
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if closed != 1 {
			t.Fatalf("silent socket not released: closes=%d", closed)
		}
		if _, err := client.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("silent socket remains open: %v", err)
		}
	})
}

func TestRound3IdleUploadCancelsHandlerAndReleasesSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bounded := limitInboundConnections(listener, 1)
	bounded.idleTimeout = 50 * time.Millisecond
	finished := make(chan error, 1)
	server := api.NewListenerServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { _, err := io.ReadAll(r.Body); finished <- err }))
	go func() { _ = server.Serve(bounded) }()
	defer server.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nContent-Length: 1\r\n\r\n")
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("silent upload read succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent upload remained active")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://"+listener.Addr().String()+"/", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("idle socket retained admission slot: %v", err)
	}
	_ = resp.Body.Close()
	if err := <-finished; err != nil {
		t.Fatalf("next upload failed: %v", err)
	}
	_ = server.Close()
}
