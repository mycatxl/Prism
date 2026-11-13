package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInboundMuxRejectsLegacyTokenRoutes(t *testing.T) {
	mux := newInboundMux(
		"legacy-token",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	for _, path := range []string{
		"/legacy-token/api/v1/Default/actions/inherit-lease",
		"/legacy-token/Default/https/example.com/path",
		"/anything/api/v1/Default/actions/inherit-lease",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Header().Get("X-Route") != "" {
				t.Fatalf("route = %q, want no downstream route", rec.Header().Get("X-Route"))
			}
			if rec.Code != http.StatusForbidden || rec.Header().Get("X-Prism-Error") != "AUTH_FAILED" {
				t.Fatalf("status/header = %d/%q, want 403/AUTH_FAILED", rec.Code, rec.Header().Get("X-Prism-Error"))
			}
		})
	}
}

func TestInboundMuxRoutesOnlyAccessKeyShapes(t *testing.T) {
	mux := newInboundMux(
		"legacy-token",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "token action", path: "/" + testMuxAccessKey + "/api/v1/Default/actions/inherit-lease", want: "token-action"},
		{name: "reverse", path: "/" + testMuxAccessKey + "/Default/https/example.com/path", want: "reverse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if got := rec.Header().Get("X-Route"); got != tc.want {
				t.Fatalf("route = %q, want %q (status=%d)", got, tc.want, rec.Code)
			}
		})
	}
}

func TestInboundMuxTokenPositionCandidateRequiresAccessKeyShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want bool
	}{
		{name: "valid key", got: testMuxAccessKey, want: true},
		{name: "legacy token", got: "legacy-token", want: false},
		{name: "empty", got: "", want: false},
		{name: "prefix only", got: "pk_not-a-key", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTokenPositionCandidate(tc.got, "legacy-token"); got != tc.want {
				t.Fatalf("candidate(%q) = %v, want %v", tc.got, got, tc.want)
			}
		})
	}
}
