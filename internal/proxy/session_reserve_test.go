package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/keys"
	"prism/internal/usage"
)

// reserveBarrierGate is a QuotaGate that holds every request at the gate until
// the test releases it.
//
// Holding them there is what makes the check-then-act window observable: while
// they wait, none of them can register a session, so a request that reads the
// count and registers afterwards reads the count the key held before any of
// them started. A gate that answers immediately would let the requests spread
// out and mask the defect.
type reserveBarrierGate struct {
	limit   int
	arrived chan struct{}
	release chan struct{}
	settle  time.Duration

	mu        sync.Mutex
	observing []int
	refusals  int
	checks    int
}

func newReserveBarrierGate(limit int) *reserveBarrierGate {
	return &reserveBarrierGate{
		limit:   limit,
		arrived: make(chan struct{}, 64),
		release: make(chan struct{}),
		settle:  500 * time.Millisecond,
	}
}

// Check blocks every request until the test releases the gate, then answers the
// way usage.Limiter does: a key that already holds its limit may not hold one
// more.
func (g *reserveBarrierGate) Check(_ *keys.KeyState, concurrent int) (bool, string) {
	select {
	case g.arrived <- struct{}{}:
	default:
	}
	<-g.release

	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks++
	g.observing = append(g.observing, concurrent)
	if concurrent >= g.limit {
		g.refusals++
		return false, "concurrency"
	}
	return true, ""
}

func (g *reserveBarrierGate) Exceeded(*keys.KeyState, int) bool { return false }

// settleThenRelease waits until every request reached the gate, or until no new
// arrival turns up for one settle window, and then lets all of them through.
//
// The wait is what the red light depends on: with the check and the
// registration separated, all of the requests reach their check before any of
// them can register.
func (g *reserveBarrierGate) settleThenRelease(want int, maxWait time.Duration) {
	defer close(g.release)

	deadline := time.Now().Add(maxWait)
	arrived := 0
	for arrived < want && time.Now().Before(deadline) {
		select {
		case <-g.arrived:
			arrived++
		case <-time.After(g.settle):
			return
		}
	}
}

func (g *reserveBarrierGate) summary() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fmt.Sprintf("checks=%d observations=%v refusals=%d", g.checks, g.observing, g.refusals)
}

func (g *reserveBarrierGate) observations() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.observing...)
}

// A key's concurrency limit is decided and acted on as one step: the request
// that is admitted reserves the session the check is about, so a key with
// max_concurrent=1 admits exactly one of the requests that arrive together.
//
// Before the fix the count was read at the check and the session registered
// afterwards, so every request of the burst read a count of zero and all of
// them were admitted - the limit only applied to requests that came one after
// another.
func TestProxyConcurrency_ReservesTheSessionBeforeItAnswers(t *testing.T) {
	t.Run("http requests", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer upstream.Close()

		env := newProxyE2EEnv(t)
		setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
		})

		const requests = 8
		gate := newReserveBarrierGate(1)
		registry := usage.NewConnRegistry()
		fp := NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			AccessKeys: &mutableKeyStore{state: workingKeyState()},
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Metering:   &usage.Sink{Counters: usage.NewCounters()},
			Quota:      gate,
			Sessions:   registry,
		})

		recorders := make([]*httptest.ResponseRecorder, requests)
		var wg sync.WaitGroup
		for i := 0; i < requests; i++ {
			recorders[i] = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, upstream.URL+"/thing", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
			wg.Add(1)
			go func(i int, req *http.Request) {
				defer wg.Done()
				fp.ServeHTTP(recorders[i], req)
			}(i, req)
		}

		gate.settleThenRelease(requests, 5*time.Second)
		wg.Wait()

		admitted, refused := 0, 0
		for _, rec := range recorders {
			switch rec.Code {
			case http.StatusOK:
				admitted++
			case http.StatusTooManyRequests:
				refused++
			default:
				t.Fatalf("a request answered %d (%q), want 200 or 429", rec.Code, rec.Body.String())
			}
		}
		if admitted != 1 {
			t.Fatalf("%d of %d requests were admitted, want exactly 1 (%s)", admitted, requests, gate.summary())
		}
		if refused != requests-1 {
			t.Fatalf("%d requests were refused, want %d (%s)", refused, requests-1, gate.summary())
		}

		// The refusals saw the admitted request's reservation: that is the
		// evidence that the check is made against the session the request is
		// about to hold, not against a count that moves only afterwards.
		observations := gate.observations()
		zeros, ones := 0, 0
		for _, concurrent := range observations {
			switch concurrent {
			case 0:
				zeros++
			case 1:
				ones++
			}
		}
		if zeros != 1 || ones != requests-1 {
			t.Fatalf("the gate saw concurrency %v, want one 0 (the admitted request) and %d 1s (the reservations)", observations, requests-1)
		}

		waitForSessionCount(t, registry, "k-meter", 0)
	})

	t.Run("connect tunnels", func(t *testing.T) {
		env := newProxyE2EEnv(t)
		const requests = 4
		gate := newReserveBarrierGate(1)
		registry := usage.NewConnRegistry()
		fp := NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			AccessKeys: &mutableKeyStore{state: workingKeyState()},
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Metering:   &usage.Sink{Counters: usage.NewCounters()},
			Quota:      gate,
			Sessions:   registry,
		})

		server := httptest.NewServer(fp)
		t.Cleanup(server.Close)
		target := echoTarget(t)

		conns := make([]net.Conn, 0, requests)
		readers := make([]*bufio.Reader, 0, requests)
		for i := 0; i < requests; i++ {
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatalf("dial proxy: %v", err)
			}
			if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				t.Fatalf("SetDeadline: %v", err)
			}
			req := fmt.Sprintf(
				"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
				target, target, basicAuth("plat", testCredential),
			)
			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("write CONNECT: %v", err)
			}
			conns = append(conns, conn)
			readers = append(readers, bufio.NewReader(conn))
		}
		t.Cleanup(func() {
			for _, conn := range conns {
				_ = conn.Close()
			}
		})

		gate.settleThenRelease(requests, 5*time.Second)

		established, refused := 0, 0
		for i, reader := range readers {
			statusLine, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read CONNECT response %d: %v", i, err)
			}
			switch {
			case strings.Contains(statusLine, "200 Connection Established"):
				established++
			case strings.Contains(statusLine, "429"):
				refused++
			default:
				t.Fatalf("CONNECT %d answered %q, want 200 or 429", i, statusLine)
			}
		}
		if established != 1 {
			t.Fatalf("%d of %d CONNECT requests were established, want exactly 1 (%s)", established, requests, gate.summary())
		}
		if refused != requests-1 {
			t.Fatalf("%d CONNECT requests were refused, want %d (%s)", refused, requests-1, gate.summary())
		}

		// The admitted tunnel holds exactly one reservation, and the refusals
		// left none behind.
		waitForSessionCount(t, registry, "k-meter", 1)
		for _, conn := range conns {
			_ = conn.Close()
		}
		waitForSessionCount(t, registry, "k-meter", 0)
	})

	t.Run("a refused tunnel leaves no session behind", func(t *testing.T) {
		// Sequential requests against the real limiter: one tunnel is open and
		// registered, the next request must be refused for the same reason and
		// must not add a registration of its own.
		env := newProxyE2EEnv(t)
		key := workingKeyState()
		key.Plan.MaxConcurrent = 1
		store := &mutableKeyStore{state: key}
		counters := usage.NewCounters()
		registry := usage.NewConnRegistry()
		fp := NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			AccessKeys: store,
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Metering:   &usage.Sink{Counters: counters},
			Quota:      &usage.Gate{Limiter: usage.NewLimiter(counters)},
			Sessions:   registry,
		})
		target := echoTarget(t)

		openConn, openReader := openTunnel(t, fp, target)
		waitForSessionCount(t, registry, "k-meter", 1)

		// The open tunnel must be usable, which is what proves the session was
		// registered for a connection that was actually established.
		payload := []byte("ping")
		if _, err := openTunnelWrite(t, openConn, payload); err != nil {
			t.Fatalf("write into the open tunnel: %v", err)
		}
		echo := make([]byte, len(payload))
		if _, err := io.ReadFull(openReader, echo); err != nil {
			t.Fatalf("read the echo of the open tunnel: %v", err)
		}
		if string(echo) != string(payload) {
			t.Fatalf("echo = %q, want %q", echo, payload)
		}

		server := httptest.NewServer(fp)
		t.Cleanup(server.Close)
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("SetDeadline: %v", err)
		}
		req := fmt.Sprintf(
			"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
			target, target, basicAuth("plat", testCredential),
		)
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write CONNECT: %v", err)
		}
		statusLine, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT status: %v", err)
		}
		if !strings.Contains(statusLine, "429") {
			t.Fatalf("a second tunnel on a key at its concurrency limit answered %q, want 429", statusLine)
		}
		if got := registry.Count("k-meter"); got != 1 {
			t.Fatalf("the refused CONNECT changed the key's session count to %d, want it left at 1", got)
		}

		_ = openConn.Close()
		waitForSessionCount(t, registry, "k-meter", 0)
	})

	t.Run("a socks5 session at the limit is refused", func(t *testing.T) {
		env := newProxyE2EEnv(t)
		key := workingKeyState()
		key.Plan.MaxConcurrent = 1
		store := &mutableKeyStore{state: key}
		counters := usage.NewCounters()
		registry := usage.NewConnRegistry()

		targetLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen target: %v", err)
		}
		defer targetLn.Close()
		go func() {
			for {
				conn, err := targetLn.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					_, _ = io.Copy(conn, conn)
				}()
			}
		}()
		setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, targetLn.Addr().String())
		})

		inbound := NewSocks5Inbound(Socks5InboundConfig{
			ProxyToken: "tok",
			AccessKeys: store,
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Metering:   &usage.Sink{Counters: counters},
			Quota:      &usage.Gate{Limiter: usage.NewLimiter(counters)},
			Sessions:   registry,
		})

		// The first session is established and holds the key's only slot.
		first, firstReader, firstDone := startSocks5Session(t, inbound)
		defer first.Close()
		socks5Establish(t, first, firstReader, testCredential, targetLn.Addr().String(), socks5ReplySucceeded)
		waitForSessionCount(t, registry, "k-meter", 1)

		// The second one must be refused for concurrency, and must leave no
		// registration of its own behind.
		second, secondReader, secondDone := startSocks5Session(t, inbound)
		defer second.Close()
		socks5Establish(t, second, secondReader, testCredential, targetLn.Addr().String(), socks5ReplyGeneralFailure)
		<-secondDone
		if got := registry.Count("k-meter"); got != 1 {
			t.Fatalf("a refused SOCKS5 session left the key holding %d registrations, want the 1 open tunnel", got)
		}

		_ = first.Close()
		<-firstDone
		waitForSessionCount(t, registry, "k-meter", 0)
	})

	t.Run("a reverse request at the limit is refused", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseNow)

		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			_, _ = w.Write([]byte("ok"))
		}))
		defer backend.Close()

		env := newProxyE2EEnv(t)
		setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(backend.URL, "http://"))
		})

		key := workingKeyState()
		key.Plan.MaxConcurrent = 1
		store := &mutableKeyStore{state: key}
		counters := usage.NewCounters()
		registry := usage.NewConnRegistry()
		rp := NewReverseProxy(ReverseProxyConfig{
			ProxyToken:     "tok",
			AccessKeys:     store,
			Router:         env.router,
			Pool:           env.pool,
			PlatformLookup: env.pool,
			Health:         &mockHealthRecorder{},
			Events:         newMockEventEmitter(),
			Metering:       &usage.Sink{Counters: counters},
			Quota:          &usage.Gate{Limiter: usage.NewLimiter(counters)},
			Sessions:       registry,
		})

		path := fmt.Sprintf("/%s/plat:acct/http/%s/slow", testCredential, strings.TrimPrefix(backend.URL, "http://"))
		firstRec := httptest.NewRecorder()
		served := make(chan struct{})
		go func() {
			defer close(served)
			rp.ServeHTTP(firstRec, httptest.NewRequest(http.MethodGet, path, nil))
		}()
		waitForSessionCount(t, registry, "k-meter", 1)

		secondRec := httptest.NewRecorder()
		rp.ServeHTTP(secondRec, httptest.NewRequest(http.MethodGet, path, nil))
		if secondRec.Code != http.StatusTooManyRequests {
			t.Fatalf("a second reverse request on a key at its concurrency limit answered %d, want 429", secondRec.Code)
		}

		releaseNow()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Fatal("the admitted reverse request never finished")
		}
		if firstRec.Code != http.StatusOK {
			t.Fatalf("the admitted reverse request answered %d, want 200", firstRec.Code)
		}
		waitForSessionCount(t, registry, "k-meter", 0)
	})
}

// openTunnelWrite writes into an established tunnel and reports how much of the
// payload was accepted.
func openTunnelWrite(t *testing.T, conn net.Conn, payload []byte) (int, error) {
	t.Helper()
	return conn.Write(payload)
}

// socks5Establish drives one RFC 1929 session up to its CONNECT reply and
// asserts the reply code, so a test can say what the data plane answered without
// spelling out the handshake every time.
func socks5Establish(t *testing.T, conn net.Conn, reader *bufio.Reader, credential, target string, wantReply byte) {
	t.Helper()

	writeAll(t, conn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, conn, socks5UserPassPacket("plat.acct", credential))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want success", got[1])
	}
	writeAll(t, conn, socks5ConnectIPv4Packet(target))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != wantReply {
		t.Fatalf("connect reply: got %v, want %d", reply, wantReply)
	}
}
