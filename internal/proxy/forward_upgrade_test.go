package proxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// A ws:// connection through the forward proxy was impossible: "Upgrade" is in
// hopByHopHeaders, so the request reached upstream as an ordinary GET and the
// upstream answered 200 (the browser then failed the handshake), and even a
// manually produced 101 had its Upgrade/Connection headers stripped by
// copyEndToEndHeaders before the single io.Copy ran one direction and returned.
//
// This test performs a real handshake through a real server and then talks in
// both directions, which is the only way to falsify all three of those at once.
func TestForwardProxy_WebSocketUpgradeIsRelayedBothWays(t *testing.T) {
	upstreamErr := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The upgrade the client asked for must arrive intact; this is the
		// half that used to be stripped on the way out.
		if got := r.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
			upstreamErr <- errUpgradeHeaders("Upgrade header not forwarded: " + got)
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		if !headerHasToken(r.Header, "Connection", "upgrade") {
			upstreamErr <- errUpgradeHeaders("Connection: Upgrade not forwarded")
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Hop-By-Hop") != "" {
			upstreamErr <- errUpgradeHeaders("connection-listed header leaked upstream")
			http.Error(w, "leak", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstreamErr <- err
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n")
		if err := buf.Flush(); err != nil {
			upstreamErr <- err
			return
		}
		// Both directions: read what the client sends through the proxy, answer
		// on the same connection.
		payload := make([]byte, 5)
		if _, err := io.ReadFull(buf, payload); err != nil {
			upstreamErr <- err
			return
		}
		if string(payload) != "ping!" {
			upstreamErr <- errUpgradeHeaders("upstream read " + string(payload))
			return
		}
		_, _ = buf.WriteString("pong!")
		_ = buf.Flush()
		// The stream stays open until the client ends it: a relay that returned
		// as soon as the 101 was written would drop the echo above, and one that
		// copied a single direction would never see this EOF.
		_, _ = io.Copy(io.Discard, buf)
		upstreamErr <- nil
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})
	key := workingKeyState()
	fp, emitter, _, _ := newQuotaForwardProxy(t, env, &mutableKeyStore{state: key})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	conn, err := net.Dial("tcp", proxySrv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	host := strings.TrimPrefix(upstream.URL, "http://")
	request := "GET " + upstream.URL + "/ws HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Connection: Upgrade, X-Hop-By-Hop\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"X-Hop-By-Hop: should-not-leave-this-hop\r\n" +
		"Proxy-Authorization: " + basicAuth("plat", testCredential) + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	reader := bufio.NewReader(conn)
	tp := textproto.NewReader(reader)
	statusLine, err := tp.ReadLine()
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if statusLine != "HTTP/1.1 101 Switching Protocols" {
		t.Fatalf("status line = %q, want 101", statusLine)
	}
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		t.Fatalf("read response headers: %v", err)
	}
	if got := header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Fatalf("response Upgrade = %q; the switch headers must reach the client", got)
	}
	if !headerHasToken(http.Header(header), "Connection", "upgrade") {
		t.Fatalf("response Connection = %q, want Upgrade", header.Get("Connection"))
	}

	if _, err := conn.Write([]byte("ping!")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != "pong!" {
		t.Fatalf("echo = %q, want %q", string(echo), "pong!")
	}

	// The client ends the switched stream; the relay then closes the upstream
	// side and the request finishes, which is what emits the log entry.
	_ = conn.Close()
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatalf("upstream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never saw the end of the stream")
	}

	// The relayed exchange is a request like any other: it lands in the request
	// log with the status the client was given.
	event := waitForRequestLog(t, emitter)
	if event.HTTPStatus != http.StatusSwitchingProtocols {
		t.Fatalf("logged status = %d, want 101", event.HTTPStatus)
	}
}

type errUpgradeHeaders string

func (e errUpgradeHeaders) Error() string { return string(e) }

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}

// The re-add must be limited to requests that actually ask for a switch:
// otherwise the proxy would hand any client the next hop's connection
// semantics, which is the reason Upgrade is on the hop-by-hop list at all.
func TestPrepareForwardOutboundRequest_ReAddsUpgradeOnlyWhenAsked(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	plain.Header.Set("Connection", "Upgrade")
	plain.Header.Set("Upgrade", "") // a Connection token with no protocol
	out := prepareForwardOutboundRequest(plain)
	if got := out.Header.Get("Upgrade"); got != "" {
		t.Fatalf("Upgrade = %q without a protocol name", got)
	}
	if got := out.Header.Get("Connection"); got != "" {
		t.Fatalf("Connection = %q without a protocol name", got)
	}

	upgrade := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	upgrade.Header.Set("Connection", "keep-alive, Upgrade")
	upgrade.Header.Set("Upgrade", " websocket ")
	out = prepareForwardOutboundRequest(upgrade)
	if got := out.Header.Get("Upgrade"); got != "websocket" {
		t.Fatalf("Upgrade = %q, want websocket", got)
	}
	if got := out.Header.Values("Connection"); len(got) != 1 || got[0] != "Upgrade" {
		t.Fatalf("Connection = %v, want [Upgrade]", got)
	}
	if got := upgrade.Header.Get("Upgrade"); got != " websocket " {
		t.Fatalf("the inbound request was mutated: Upgrade = %q", got)
	}
}
