package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/usage"
)

// virtualBandwidthClock replaces the package clock and sleep so a throttled
// transfer can be measured without waiting for it. The clock feeds itself: every
// sleep advances it, which is what the real schedule sees too, so a copy that
// pays 3 seconds of delay ends up with a schedule that is 3 seconds further
// along - the same arithmetic, just not in real time.
type virtualBandwidthClock struct {
	mu    sync.Mutex
	now   int64
	slept time.Duration
	steps int
}

func installVirtualBandwidthClock(t *testing.T) *virtualBandwidthClock {
	t.Helper()
	clock := &virtualBandwidthClock{}
	prevNow, prevSleep := bandwidthNow, bandwidthSleep
	bandwidthNow = func() int64 {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		return clock.now
	}
	bandwidthSleep = func(d time.Duration) {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		clock.now += int64(d)
		clock.slept += d
		clock.steps++
	}
	// The table is shared process-wide, so a key left behind by another test
	// would hand this one a started burst it did not earn.
	sharedBandwidth.mu.Lock()
	sharedBandwidth.due = nil
	sharedBandwidth.mu.Unlock()
	t.Cleanup(func() {
		bandwidthNow, bandwidthSleep = prevNow, prevSleep
		sharedBandwidth.mu.Lock()
		sharedBandwidth.due = nil
		sharedBandwidth.mu.Unlock()
	})
	return clock
}

// throttledSleepFloor is the waiting a body of this size must pay at this rate:
// its whole cost minus the second of starting credit every key begins with.
//
// The slack covers the way the schedule rounds: each chunk's cost is an integer
// nanosecond count truncated per chunk, so several chunks pay a nanosecond less
// than the whole body's cost. The assertion is about seconds because the claim
// is about seconds; it is not a stopwatch.
func throttledSleepFloor(bodySize, rateBps int64) time.Duration {
	const slack = time.Millisecond
	return time.Duration(bodySize)*time.Second/time.Duration(rateBps) - time.Duration(bandwidthBurstNs) - slack
}

func (c *virtualBandwidthClock) stats() (time.Duration, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept, c.steps
}

// MaxBandwidthBps reached the console, the key snapshot and the data plane and
// stopped there: only the CONNECT/SOCKS5 tunnels wrapped their socket in a
// throttled connection, so a plan's limit simply did not exist for HTTP
// proxying. These tests copy a body that is several seconds over the limit
// through each HTTP path and assert the time was paid.
func TestForwardProxy_ResponseBodyIsThrottled(t *testing.T) {
	const (
		rateBps  = 1 << 20
		bodySize = 3 << 20
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3145728")
		_, _ = w.Write(bytes.Repeat([]byte("r"), bodySize))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	for _, tc := range []struct {
		name    string
		rateBps int64
	}{
		{name: "limited", rateBps: rateBps},
		{name: "unlimited", rateBps: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := installVirtualBandwidthClock(t)

			key := workingKeyState()
			key.KeyID = "k-bandwidth-" + tc.name
			key.Plan.MaxBandwidthBps = tc.rateBps
			store := &mutableKeyStore{state: key}
			fp, _, _, _ := newQuotaForwardProxy(t, env, store)

			req := httptest.NewRequest(http.MethodGet, upstream.URL+"/big", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
			rec := httptest.NewRecorder()
			fp.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			// The bytes still arrive in full: throttling paces a copy, it does
			// not truncate one.
			if rec.Body.Len() != bodySize {
				t.Fatalf("body = %d bytes, want %d", rec.Body.Len(), bodySize)
			}
			slept, steps := clock.stats()
			if tc.rateBps == 0 {
				if slept != 0 || steps != 0 {
					t.Fatalf("unlimited plan slept %v in %d steps; it must cost the copy one comparison", slept, steps)
				}
				return
			}
			// 3 MiB at 1 MiB/s is 3 s of cost against the 1 s starting credit,
			// so the copy must wait out the remaining 2 s.
			want := throttledSleepFloor(bodySize, rateBps)
			if slept < want {
				t.Fatalf("throttled copy slept %v, want at least %v", slept, want)
			}
			if steps == 0 {
				t.Fatal("no pacing steps were taken")
			}
		})
	}
}

// The upload direction is a separate copy with its own wrapper; a fix that
// covered only the response would pass the test above and still let a key
// upload at line rate.
func TestForwardProxy_RequestBodyIsThrottled(t *testing.T) {
	const (
		rateBps  = 1 << 20
		bodySize = 3 << 20
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		if n != bodySize {
			t.Errorf("upstream received %d bytes, want %d", n, bodySize)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	clock := installVirtualBandwidthClock(t)
	key := workingKeyState()
	key.KeyID = "k-bandwidth-upload"
	key.Plan.MaxBandwidthBps = rateBps
	store := &mutableKeyStore{state: key}
	fp, _, _, _ := newQuotaForwardProxy(t, env, store)

	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/upload", bytes.NewReader(bytes.Repeat([]byte("u"), bodySize)))
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	slept, _ := clock.stats()
	want := throttledSleepFloor(bodySize, rateBps)
	if slept < want {
		t.Fatalf("throttled upload slept %v, want at least %v", slept, want)
	}
}

// The reverse proxy copies the bodies itself through httputil, on a third set of
// wrappers. Its own limit is checked here so a rewire that lets the reverse path
// through unthrottled cannot hide behind the forward tests.
func TestReverseProxy_ResponseBodyIsThrottled(t *testing.T) {
	const (
		rateBps  = 1 << 20
		bodySize = 3 << 20
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3145728")
		_, _ = w.Write(bytes.Repeat([]byte("r"), bodySize))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	clock := installVirtualBandwidthClock(t)
	key := workingKeyState()
	key.KeyID = "k-bandwidth-reverse"
	key.Plan.MaxBandwidthBps = rateBps
	counters := usage.NewCounters()
	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		AccessKeys:     keyVerifierStub{credential: testCredential, state: key},
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         newMockEventEmitter(),
		Metering:       &usage.Sink{Counters: counters},
		Quota:          &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:       usage.NewConnRegistry(),
	})

	req := httptest.NewRequest(http.MethodGet,
		"http://example.com/"+testCredential+"/plat:acct/http/example.com/big", nil)
	req = req.WithContext(context.WithValue(req.Context(), http.ServerContextKey, &http.Server{}))
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != bodySize {
		t.Fatalf("body = %d bytes, want %d", rec.Body.Len(), bodySize)
	}
	slept, _ := clock.stats()
	want := throttledSleepFloor(bodySize, rateBps)
	if slept < want {
		t.Fatalf("throttled reverse copy slept %v, want at least %v", slept, want)
	}
}
