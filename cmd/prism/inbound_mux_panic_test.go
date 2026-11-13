package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/model"
)

// newEndpointInboundMuxTestHandler builds the mux the endpoint listener serves,
// with every dependency a plain handler.
func newEndpointInboundMuxTestHandler(forward, reverse http.Handler) http.Handler {
	endpoint := model.Endpoint{AllowProxy: true, AllowHTTPForward: true, AllowHTTPReverse: true, AllowManagement: true}
	return newEndpointInboundMux(
		func() model.Endpoint { return endpoint },
		"tok",
		forward,
		reverse,
		nil,
		nil,
		nil,
	)
}

// A panic in the forward proxy must answer as a proxy failure, not drop the
// connection: a proxy client reads the status line, and a dropped connection
// looks like a dead proxy rather than one failed request.
func TestInboundMuxRecoversAForwardProxyPanic(t *testing.T) {
	handler := newEndpointInboundMuxTestHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: a target that must not reach the client")
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://example.test/thing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("X-Prism-Error"); got != "PROXY_FAILURE" {
		t.Fatalf("X-Prism-Error = %q, want PROXY_FAILURE", got)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("the panic message reached the client: %q", rec.Body.String())
	}
}

// The reverse proxy is a separate handler on the same mux and needs the same
// treatment.
func TestInboundMuxRecoversAReverseProxyPanic(t *testing.T) {
	handler := newEndpointInboundMuxTestHandler(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	// The first segment has to be key-shaped or the mux refuses the request
	// before the reverse handler ever sees it.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+testMuxAccessKey+"/plat:acct/https/example.test/thing", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("X-Resin-Error"); got != "PROXY_FAILURE" {
		t.Fatalf("X-Resin-Error = %q, want PROXY_FAILURE", got)
	}
}

// http.ErrAbortHandler is how a handler abandons a response it has started;
// answering it with a 502 would write into a connection being torn down.
func TestInboundMuxLetsAnAbortedProxyResponseAbort(t *testing.T) {
	handler := newEndpointInboundMuxTestHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), nil)

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("the abort was swallowed and recovered as %v", recovered)
		}
	}()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/thing", nil))
	t.Fatalf("the handler returned instead of aborting, and answered %d", rec.Code)
}

func TestRecoverProxyPanicDoesNotAppendAfterCommittedResponse(t *testing.T) {
	handler := recoverProxyPanic("committed", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		panic("after headers")
	}))

	rec := httptest.NewRecorder()
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("committed panic recovered as %v, want http.ErrAbortHandler", recovered)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status after committed panic = %d, want 204", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "Proxy request failed") {
			t.Fatal("the recovery layer appended a second proxy failure response")
		}
	}()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/thing", nil))
	t.Fatal("committed panic returned instead of aborting")
}

// A handler that does not panic passes through untouched.
func TestInboundMuxLeavesAServedProxyResponseAlone(t *testing.T) {
	handler := newEndpointInboundMuxTestHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream"))
	}), nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/thing", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "upstream" {
		t.Fatalf("answered %d %q, want 200 \"upstream\"", rec.Code, rec.Body.String())
	}
}
