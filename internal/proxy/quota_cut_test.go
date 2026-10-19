package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/usage"
)

// An upload the allowance cuts before the upstream ever answered used to be
// indistinguishable from a client that hung up: classifyUpstreamError reports
// context.Canceled as "not an upstream fault", the handler returned silently,
// and net/http completed the exchange as 200 with an empty body while the
// request log recorded net_ok=true. Both halves of that are wrong - the client
// paid for an upload that was refused, and the audit trail says it succeeded.
//
// These tests pin the answer the client gets (the quota error, not 200) and the
// verdict that reaches the request log (net_ok=false, named error).
func TestForwardProxy_QuotaCutBeforeResponseIsReported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the cut lands in the request copy, not in a
		// half-read connection: the upload is what crosses the allowance.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	store := &mutableKeyStore{state: key}
	fp, emitter, _, _ := newQuotaForwardProxy(t, env, store)

	body := bytes.Repeat([]byte("z"), midStreamBodyLen)
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/upload", bytes.NewReader(body))
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("quota-cut upload answered as %d with body %q; the refusal must not look like success",
			rec.Code, rec.Body.String())
	}
	if rec.Code != ErrQuotaExceeded.HTTPCode {
		t.Fatalf("status = %d, want %d", rec.Code, ErrQuotaExceeded.HTTPCode)
	}
	if got := rec.Header().Get("X-Prism-Error"); got != ErrQuotaExceeded.PrismError {
		t.Fatalf("X-Prism-Error = %q, want %q", got, ErrQuotaExceeded.PrismError)
	}

	event := waitForRequestLog(t, emitter)
	if event.NetOK {
		t.Fatalf("request log recorded net_ok=true for a request the allowance refused: %+v", event)
	}
	if event.PrismError != ErrQuotaExceeded.PrismError {
		t.Fatalf("PrismError = %q, want %q", event.PrismError, ErrQuotaExceeded.PrismError)
	}
	if event.UpstreamStage != "forward_roundtrip_quota" {
		t.Fatalf("UpstreamStage = %q", event.UpstreamStage)
	}
}

func TestReverseProxy_QuotaCutBeforeResponseIsReported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	counters := usage.NewCounters()
	emitter := newMockEventEmitter()
	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		AccessKeys:     keyVerifierStub{credential: testCredential, state: key},
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         emitter,
		Metering:       &usage.Sink{Counters: counters},
		Quota:          &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:       usage.NewConnRegistry(),
	})

	body := bytes.Repeat([]byte("z"), midStreamBodyLen)
	req := httptest.NewRequest(http.MethodPost,
		"http://example.com/"+testCredential+"/plat:acct/http/example.com/upload", bytes.NewReader(body))
	// httputil.ReverseProxy refuses to run without a server in the context.
	req = req.WithContext(context.WithValue(req.Context(), http.ServerContextKey, &http.Server{}))
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("quota-cut upload answered as %d with body %q; the refusal must not look like success",
			rec.Code, rec.Body.String())
	}
	if rec.Code != ErrQuotaExceeded.HTTPCode {
		t.Fatalf("status = %d, want %d", rec.Code, ErrQuotaExceeded.HTTPCode)
	}
	if got := rec.Header().Get("X-Prism-Error"); got != ErrQuotaExceeded.PrismError {
		t.Fatalf("X-Prism-Error = %q, want %q", got, ErrQuotaExceeded.PrismError)
	}

	event := waitForRequestLog(t, emitter)
	if event.NetOK {
		t.Fatalf("request log recorded net_ok=true for a request the allowance refused: %+v", event)
	}
	if event.PrismError != ErrQuotaExceeded.PrismError {
		t.Fatalf("PrismError = %q, want %q", event.PrismError, ErrQuotaExceeded.PrismError)
	}
	if event.UpstreamStage != "reverse_roundtrip_quota" {
		t.Fatalf("UpstreamStage = %q", event.UpstreamStage)
	}
}

// The other half of the same branch: a client that hangs up is owed nothing and
// must keep the old quiet behaviour. Without this case the fix above could be
// "always answer 429 on cancellation" and nothing would notice.
func TestForwardProxy_ClientCancelStaysSilent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	store := &mutableKeyStore{state: key}
	fp, emitter, _, _ := newQuotaForwardProxy(t, env, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone before the round trip starts
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/gone", nil).WithContext(ctx)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Prism-Error"); got != "" {
		t.Fatalf("client cancel wrote X-Prism-Error=%q; nobody is left to read it", got)
	}
	event := waitForRequestLog(t, emitter)
	if !event.NetOK {
		t.Fatalf("client cancel must stay net_ok=true, got %+v", event)
	}
	if event.PrismError != "" {
		t.Fatalf("client cancel must not be blamed on the proxy, got PrismError=%q", event.PrismError)
	}
}
