package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/model"
)

// The public subscription endpoint (WP11 §4.3) is documented on the primary
// listener — README shows `curl http://127.0.0.1:2260/sub/<token>`. It is served
// by the management handler, so the endpoint mux has to route /sub/* there
// instead of treating it as an unauthenticated reverse-proxy path.

func TestInboundMuxRoutesSubscriptionPathToManagementHandler(t *testing.T) {
	apiCalls := 0
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.Header().Set("X-Route", "api")
		w.WriteHeader(http.StatusOK)
	})
	reverseCalls := 0
	reverse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reverseCalls++
		w.Header().Set("X-Route", "reverse")
		w.WriteHeader(http.StatusOK)
	})
	mux := newInboundMux(
		testProxyAuthToken,
		tagHandler("forward", http.StatusOK),
		reverse,
		apiHandler,
		tagHandler("token-action", http.StatusOK),
	)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/not-a-real-token", nil))

	if rec.Header().Get("X-Route") != "api" {
		t.Fatalf("GET /sub/<token> was routed to %q, want the management handler", rec.Header().Get("X-Route"))
	}
	if apiCalls != 1 || reverseCalls != 0 {
		t.Fatalf("api calls = %d, reverse calls = %d, want 1/0", apiCalls, reverseCalls)
	}
}

func TestInboundMuxSubscriptionPathHonoursAllowManagement(t *testing.T) {
	apiCalls := 0
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusOK)
	})
	mux := newEndpointInboundMux(
		func() model.Endpoint {
			return model.Endpoint{AllowManagement: false, AllowProxy: true, AllowHTTPReverse: true}
		},
		testProxyAuthToken,
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		apiHandler,
		tagHandler("token-action", http.StatusOK),
		nil,
	)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/not-a-real-token", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an endpoint without allow_management", rec.Code)
	}
	if apiCalls != 0 {
		t.Fatalf("the management handler served %d requests, want 0", apiCalls)
	}
}

// A URL minted while the operator used the independent management listener must
// work there too: the admin listener serves the management surface, and /sub is
// part of it.
func TestAdminListenerServesSubscriptionPath(t *testing.T) {
	for _, path := range []string{"/sub/token", "/sub/"} {
		if !isManagementPath(httptest.NewRequest(http.MethodGet, path, nil)) {
			t.Fatalf("%s must be served by the management listener", path)
		}
	}
	if isManagementPath(httptest.NewRequest(http.MethodConnect, "/sub/token", nil)) {
		t.Fatal("CONNECT must never be routed to the management listener")
	}
}
