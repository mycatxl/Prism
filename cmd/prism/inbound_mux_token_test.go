package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// --- behavior of the path-token gate ---

// TestInboundMux_TokenComparisonSemantics pins the behavior of the two token
// decisions the mux owns. The comparison itself is replaced by a constant-time
// helper, so these cases must not move: the helper is an equivalence, not a
// policy change.
func TestInboundMux_TokenComparisonSemantics(t *testing.T) {
	const proxyToken = "0123456789abcdef0123456789abcdef"

	t.Run("correct token reaches the token action", func(t *testing.T) {
		mux := newInboundMux(
			proxyToken,
			tagHandler("forward", http.StatusOK),
			tagHandler("reverse", http.StatusOK),
			tagHandler("api", http.StatusOK),
			tagHandler("token-action", http.StatusOK),
		)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+proxyToken+"/api/v1/Default/actions/inherit-lease", nil))
		if rec.Header().Get("X-Route") != "token-action" {
			t.Fatalf("route = %q, want token-action", rec.Header().Get("X-Route"))
		}
	})

	t.Run("same-length wrong token is rejected", func(t *testing.T) {
		mux := newInboundMux(
			proxyToken,
			tagHandler("forward", http.StatusOK),
			tagHandler("reverse", http.StatusOK),
			tagHandler("api", http.StatusOK),
			tagHandler("token-action", http.StatusOK),
		)
		wrong := "0123456789abcdef0123456789abcdee" // last byte differs, same length
		if len(wrong) != len(proxyToken) {
			t.Fatalf("test bug: wrong token has length %d, want %d", len(wrong), len(proxyToken))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+wrong+"/api/v1/Default/actions/inherit-lease", nil))
		if got := rec.Header().Get("X-Route"); got != "" {
			t.Fatalf("route = %q, want the token action to stay unreachable", got)
		}
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Prism-Error") != "AUTH_FAILED" {
			t.Fatalf("status/header = %d/%q, want 403/AUTH_FAILED", rec.Code, rec.Header().Get("X-Prism-Error"))
		}
	})

	t.Run("different-length wrong token is rejected", func(t *testing.T) {
		mux := newInboundMux(
			proxyToken,
			tagHandler("forward", http.StatusOK),
			tagHandler("reverse", http.StatusOK),
			tagHandler("api", http.StatusOK),
			tagHandler("token-action", http.StatusOK),
		)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/0123456789abcdef/api/v1/Default/actions/inherit-lease", nil))
		if got := rec.Header().Get("X-Route"); got != "" {
			t.Fatalf("route = %q, want the token action to stay unreachable", got)
		}
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Prism-Error") != "AUTH_FAILED" {
			t.Fatalf("status/header = %d/%q, want 403/AUTH_FAILED", rec.Code, rec.Header().Get("X-Prism-Error"))
		}
	})

	t.Run("correct token passes the reverse gate", func(t *testing.T) {
		mux := newInboundMux(
			proxyToken,
			tagHandler("forward", http.StatusOK),
			tagHandler("reverse", http.StatusOK),
			tagHandler("api", http.StatusOK),
			tagHandler("token-action", http.StatusOK),
		)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+proxyToken+"/Default/https/example.com/path", nil))
		if rec.Header().Get("X-Route") != "reverse" {
			t.Fatalf("route = %q, want reverse", rec.Header().Get("X-Route"))
		}
	})

	t.Run("empty proxy token keeps the open semantics", func(t *testing.T) {
		mux := newInboundMux(
			"",
			tagHandler("forward", http.StatusOK),
			tagHandler("reverse", http.StatusOK),
			tagHandler("api", http.StatusOK),
			tagHandler("token-action", http.StatusOK),
		)
		// Any token segment reaches the token action...
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything/api/v1/system/info", nil))
		if rec.Header().Get("X-Route") != "token-action" {
			t.Fatalf("route = %q, want token-action when no proxy token is configured", rec.Header().Get("X-Route"))
		}
		// ...and the reverse gate never rejects (it has nothing to compare).
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything/Default/https/example.com", nil))
		if rec.Header().Get("X-Route") != "reverse" {
			t.Fatalf("route = %q, want reverse when no proxy token is configured", rec.Header().Get("X-Route"))
		}
	})
}

// --- structure of the path-token gate ---

// TestInboundMuxTokenComparisonIsConstantTime is the recurrence guard for.
//
// The token decision lives in the mux (the proxy only sees what the mux lets
// through), and it used to compare the path token with `token != proxyToken`.
// A byte-wise comparison leaks the token through timing, and no behavior test
// can catch that: `==` and a constant-time compare accept and reject exactly
// the same inputs. The only durable guard is reading the source, the same way
// TestNoBareHTTPServerInProductionSources guards the connection bounds.
func TestInboundMuxTokenComparisonIsConstantTime(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	source := readFileString(t, filepath.Join(root, "cmd", "prism", "inbound_mux.go"))
	// Comments are stripped so this test does not match the patterns inside its
	// own documentation.
	code := stripLineComments(source)

	for _, pattern := range []string{"token != proxyToken", "token == proxyToken"} {
		if strings.Contains(code, pattern) {
			t.Errorf("inbound_mux.go compares the path token with %q; use tokenMatches so the comparison stays constant time", pattern)
		}
	}
	if got := strings.Count(code, "subtle.ConstantTimeCompare"); got != 1 {
		t.Errorf("inbound_mux.go has %d subtle.ConstantTimeCompare call(s), want exactly 1 (inside tokenMatches)", got)
	}
	if got := strings.Count(code, "func tokenMatches("); got != 1 {
		t.Errorf("inbound_mux.go defines tokenMatches %d time(s), want exactly 1", got)
	}
	if got := strings.Count(code, "tokenMatches("); got != 3 {
		t.Errorf("inbound_mux.go mentions tokenMatches %d time(s), want 3 (one definition, two decision sites)", got)
	}

	// Both decision functions must go through the helper: a rewrite that
	// reopens only one of the two gates would otherwise pass the counts above.
	for _, name := range []string{"shouldRouteTokenAPI", "shouldRejectReverseProxyByToken"} {
		body := functionBody(t, code, name)
		if !strings.Contains(body, "tokenMatches(") {
			t.Errorf("%s does not use tokenMatches; its token comparison is not constant time", name)
		}
	}
}

// functionBody returns the source of one top-level function.
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found in inbound_mux.go", name)
	}
	rest := src[start:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
