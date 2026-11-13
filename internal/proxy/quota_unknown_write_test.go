package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/keys"
	"prism/internal/state"
	"prism/internal/usage"
)

// A recovered writer panic cannot tell the caller how much the peer accepted.
// Keep the SQLite liability across recovery instead of refunding it as zero.
func TestPanickingWriterPreservesUnknownDurableBytes(t *testing.T) {
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	journal, err := usage.NewJournal(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &usage.Gate{Limiter: usage.NewLimiter(usage.NewCounters()), Journal: journal}
	key := &keys.KeyState{KeyID: "test-key", UserID: "test-user", Plan: keys.PlanSnapshot{TrafficBytes: 1000}}
	q := newStreamQuota(gate, key, 0, nil)
	var peer bytes.Buffer
	func() {
		defer func() {
			if recover() == nil {
				t.Error("writer panic was swallowed")
			}
		}()
		_, _ = q.write(make([]byte, 100), true, func(p []byte) (int, error) {
			_, _ = peer.Write(p[:73])
			panic("writer interrupted before returning accepted count")
		})
	}()
	rows, err := engine.PendingUsage()
	if err != nil || peer.Len() != 73 || len(rows) != 1 || rows[0].Bytes != 100 {
		t.Fatal("unknown write was settled or its durable liability was lost")
	}
	if !journal.Blocked(key.UserID) {
		t.Fatal("unknown write did not immediately close affected access")
	}
	if n, _ := gate.ReserveTraffic(key, "", false, 1); n != 0 {
		t.Fatal("unknown write admitted another reservation")
	}
	restarted, err := usage.NewJournal(engine, nil)
	if err != nil || !restarted.Blocked(key.UserID) {
		t.Fatal("unknown write liability did not survive journal reconstruction")
	}
}

type interruptedHeaderTransport struct{}

func (interruptedHeaderTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("transport interrupted after starting request")
}

func TestPanickingTransportPreservesUnknownDurableHeaderBytes(t *testing.T) {
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	journal, err := usage.NewJournal(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &usage.Gate{Limiter: usage.NewLimiter(usage.NewCounters()), Journal: journal}
	key := &keys.KeyState{KeyID: "test-key", UserID: "test-user", Plan: keys.PlanSnapshot{TrafficBytes: 1000}}
	transport := quotaTransport{base: interruptedHeaderTransport{}, quota: newStreamQuota(gate, key, 0, nil), committed: func() bool { return true }}
	request, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test", "interrupted-header")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("transport panic was swallowed")
			}
		}()
		_, _ = transport.RoundTrip(request)
	}()
	rows, err := engine.PendingUsage()
	if err != nil || len(rows) != 1 || rows[0].Bytes != headerWireLen(request.Header) || !journal.Blocked(key.UserID) {
		t.Fatal("interrupted transport erased unknown header liability or left access open")
	}
}

type interruptedUpgradeConn struct {
	net.Conn
	accepted bytes.Buffer
	panics   bool
}

func (c *interruptedUpgradeConn) Write(p []byte) (int, error) {
	n, _ := c.accepted.Write(p[:min(7, len(p))])
	if c.panics {
		panic("handshake writer interrupted")
	}
	return n, io.ErrUnexpectedEOF
}

type interruptedUpgradeWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w interruptedUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestInterruptedUpgradePreservesUnknownDurableHeaderBytes(t *testing.T) {
	for _, phase := range []string{"write", "flush", "before-write"} {
		for _, panics := range []bool{false, true} {
			name := phase + "/error"
			if panics {
				name = phase + "/panic"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
				if err != nil {
					t.Fatal(err)
				}
				defer closer.Close()
				journal, err := usage.NewJournal(engine, nil)
				if err != nil {
					t.Fatal(err)
				}
				counters := usage.NewCounters()
				gate := &usage.Gate{Limiter: usage.NewLimiter(counters), Journal: journal}
				key := &keys.KeyState{KeyID: "upgrade-key", UserID: "upgrade-user", Plan: keys.PlanSnapshot{TrafficBytes: 100000}}
				client, peer := net.Pipe()
				defer client.Close()
				defer peer.Close()
				conn := &interruptedUpgradeConn{Conn: client, panics: panics}
				upstream, upstreamPeer := net.Pipe()
				defer upstream.Close()
				defer upstreamPeer.Close()
				resp := &http.Response{StatusCode: http.StatusSwitchingProtocols, ProtoMajor: 1, ProtoMinor: 1,
					Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"test"}}, Body: upstream}
				if phase == "write" {
					resp.Header.Set("X-Large-Header", strings.Repeat("x", 8192))
				}
				headerBytes := headerWireLen(resp.Header)
				var writer http.ResponseWriter = interruptedUpgradeWriter{httptest.NewRecorder(), conn}
				if phase == "before-write" {
					writer = httptest.NewRecorder() // no Hijacker: no bytes can have been handed out
				}
				func() {
					defer func() {
						if recovered := recover(); (recovered != nil) != (panics && phase != "before-write") {
							t.Error("unexpected handshake panic outcome")
						}
					}()
					new(ForwardProxy).serveForwardUpgrade(writer, resp, forwardUpgradeState{
						quota: newStreamQuota(gate, key, 0, nil), lifecycle: &requestLifecycle{},
					})
				}()
				rows, err := engine.PendingUsage()
				if err != nil {
					t.Fatal(err)
				}
				if phase == "before-write" {
					if len(rows) != 0 || conn.accepted.Len() != 0 || counters.UserBytes(key.UserID) != 0 || journal.Blocked(key.UserID) {
						t.Fatal("unattempted handshake retained a liability")
					}
					return
				}
				if conn.accepted.Len() != 7 || len(rows) != 1 || rows[0].Bytes != headerBytes || !journal.Blocked(key.UserID) {
					t.Fatal("partially transmitted handshake lost its unknown liability or left access open")
				}
				if n, _ := gate.ReserveTraffic(key, "", false, 1); n != 0 {
					t.Fatal("interrupted handshake admitted another reservation")
				}
			})
		}
	}
}
