package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/keys"
	"prism/internal/usage"
)

// startUpgradeEchoUpstream accepts connections the way an upgraded upstream
// does: it reads the request head, answers 101 Switching Protocols and then
// echoes every byte of the switched protocol.
//
// It is deliberately a raw listener rather than an httptest.Server: a server
// handler cannot upgrade a connection without hijacking it itself, and the
// point of the test is the proxy's own upgrade path.
func startUpgradeEchoUpstream(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				upgrade := "HTTP/1.1 101 Switching Protocols\r\n" +
					"Connection: Upgrade\r\n" +
					"Upgrade: websocket\r\n\r\n"
				if _, err := conn.Write([]byte(upgrade)); err != nil {
					return
				}
				buf := make([]byte, 32*1024)
				for {
					n, err := reader.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln
}

// newUpgradeQuotaReverseProxy wires the reverse proxy the way the process does,
// with one counter set, one registry and a real limiter behind the gate.
func newUpgradeQuotaReverseProxy(
	t *testing.T,
	env *proxyE2EEnv,
	key *keys.KeyState,
	counters *usage.Counters,
	registry *usage.ConnRegistry,
	emitter *mockEventEmitter,
) *ReverseProxy {
	t.Helper()

	return NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		AccessKeys:     keyVerifierStub{credential: testCredential, state: key},
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         emitter,
		Metering:       &usage.Sink{Counters: counters},
		Quota:          &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:       registry,
	})
}

// An upgraded (101) stream is subject to the same rule as any other stream: its
// bytes are handed to the key's sink while the stream runs, and a key that runs
// out of allowance has the stream cut while it is still open.
//
// Before the fix the 101 body carried only the read/write counters, so nothing
// was reported until ServeHTTP returned and nothing was enforced at all: a
// websocket could move unlimited traffic on a key that was already over its
// allowance, and the enforcer's sweep had no increment to notice.
func TestReverseProxy_UpgradeStreamIsCutAndReportedMidStream(t *testing.T) {
	upstream := startUpgradeEchoUpstream(t)

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()
	emitter := newMockEventEmitter()
	rp := newUpgradeQuotaReverseProxy(t, env, key, counters, registry, emitter)

	server := httptest.NewServer(rp)
	t.Cleanup(server.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	req := fmt.Sprintf(
		"GET /%s/plat:acct/http/example.com/ws HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n\r\n",
		testCredential,
	)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read upgrade status: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("the upgrade was not established: %q", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read upgrade headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// The client is paced by its own echoes, so the cut is observed while the
	// stream is open instead of racing a client that dumped everything at once.
	chunk := bytes.Repeat([]byte("w"), midStreamChunk)
	echo := make([]byte, len(chunk))
	var written, echoed int64
	var reportedWhileOpen int64
	for written < midStreamBodyLen {
		if _, err := conn.Write(chunk); err != nil {
			break
		}
		written += int64(len(chunk))
		n, err := io.ReadFull(reader, echo)
		echoed += int64(n)
		if reported := counters.UserBytes("u-meter"); reported > reportedWhileOpen {
			reportedWhileOpen = reported
		}
		if err != nil {
			break
		}
	}

	if echoed == 0 {
		t.Fatal("nothing flowed through the upgrade before it ended")
	}
	if echoed >= midStreamBodyLen {
		t.Fatalf("the upgrade carried the whole %d-byte stream: the allowance was not enforced on the upgraded stream", midStreamBodyLen)
	}
	if reportedWhileOpen < midStreamAllowance {
		t.Fatalf("the key was charged %d bytes while the upgrade was open, want at least its %d-byte allowance: the upgraded stream only reports when it ends",
			reportedWhileOpen, midStreamAllowance)
	}
	if reported := counters.UserBytes("u-meter"); reported < midStreamAllowance {
		t.Fatalf("the key is charged %d bytes after the cut, want at least its %d-byte allowance", reported, midStreamAllowance)
	}
	waitForSessionCount(t, registry, "k-meter", 0)
}
