package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"prism/internal/platform"
	"prism/internal/routing"
)

// platformWithBehavior builds the minimal platform needed to exercise account
// resolution without touching the network.
func platformWithBehavior(behavior platform.ReverseProxyEmptyAccountBehavior) *platform.Platform {
	plat := platform.NewPlatform("plat-id", "plat", nil, nil)
	plat.ReverseProxyEmptyAccountBehavior = string(behavior)
	return plat
}

// TestResolveReverseAccountAcceptsBothAccountHeaders covers deviation X4 at the
// account-resolution level: both header spellings are accepted, the Prism
// header wins, and header > path account > extraction rule still holds.
func TestResolveReverseAccountAcceptsBothAccountHeaders(t *testing.T) {
	parsed := &parsedPath{PlatformName: "plat", Account: "path-account", Protocol: "http", Host: "example.com"}

	tests := []struct {
		name         string
		prismHeader  string
		resinHeader  string
		parsed       *parsedPath
		plat         *platform.Platform
		wantAccount  string
		wantExtract  bool
		extractRules []string
	}{
		{
			name:        "resin header is accepted",
			resinHeader: "resin-account",
			parsed:      parsed,
			wantAccount: "resin-account",
		},
		{
			name:        "prism header is accepted",
			prismHeader: "prism-account",
			parsed:      parsed,
			wantAccount: "prism-account",
		},
		{
			name:        "prism header wins over resin header",
			prismHeader: "prism-account",
			resinHeader: "resin-account",
			parsed:      parsed,
			wantAccount: "prism-account",
		},
		{
			name:        "header wins over the path account",
			resinHeader: "resin-account",
			parsed:      parsed,
			wantAccount: "resin-account",
		},
		{
			name:        "path account is used without headers",
			parsed:      parsed,
			wantAccount: "path-account",
		},
		{
			name:   "empty headers fall back to the extraction rule",
			parsed: &parsedPath{PlatformName: "plat", Protocol: "http", Host: "example.com"},
			plat: platformWithBehavior(
				platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
			),
			extractRules: []string{"X-Account-Id"},
			wantAccount:  "extracted-account",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/tok/plat:path-account/http/example.com/", nil)
			if tc.prismHeader != "" {
				req.Header.Set("X-Prism-Account", tc.prismHeader)
			}
			if tc.resinHeader != "" {
				req.Header.Set("X-Resin-Account", tc.resinHeader)
			}
			if len(tc.extractRules) > 0 {
				req.Header.Set("X-Account-Id", tc.wantAccount)
			}

			account, _, extractionFailed := resolveReverseAccount(tc.parsed, req, tc.plat, tc.extractRules)
			if account != tc.wantAccount {
				t.Fatalf("account = %q, want %q", account, tc.wantAccount)
			}
			if extractionFailed != tc.wantExtract {
				t.Fatalf("extractionFailed = %v, want %v", extractionFailed, tc.wantExtract)
			}
		})
	}
}

// TestReverseProxyAccountHeaderCompat drives the whole reverse-proxy path with
// X-Resin-Account and X-Prism-Account: the chosen account reaches routing as a
// lease and neither header is forwarded upstream.
func TestReverseProxyAccountHeaderCompat(t *testing.T) {
	env := newReverseE2EEnv(t)

	var mu sync.Mutex
	upstreamHeaders := make([]http.Header, 0, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamHeaders = append(upstreamHeaders, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat:path-account/http/%s/", host)

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Events:         NoOpEventEmitter{},
	})

	requests := []struct {
		name        string
		prismHeader string
		resinHeader string
		wantLease   string
	}{
		{name: "resin header", resinHeader: "resin-account", wantLease: "resin-account"},
		{name: "prism header", prismHeader: "prism-account", wantLease: "prism-account"},
		{
			name:        "both headers",
			prismHeader: "prism-account",
			resinHeader: "resin-account",
			wantLease:   "prism-account",
		},
	}

	for _, tc := range requests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.prismHeader != "" {
				req.Header.Set("X-Prism-Account", tc.prismHeader)
			}
			if tc.resinHeader != "" {
				req.Header.Set("X-Resin-Account", tc.resinHeader)
			}
			rec := httptest.NewRecorder()
			rp.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf(
					"status = %d, want 200 (body=%q, prismErr=%q, resinErr=%q)",
					rec.Code, rec.Body.String(),
					rec.Header().Get("X-Prism-Error"), rec.Header().Get("X-Resin-Error"),
				)
			}

			leased := false
			env.router.RangeLeases("plat-id", func(account string, _ routing.Lease) bool {
				if account == tc.wantLease {
					leased = true
				}
				return true
			})
			if !leased {
				t.Fatalf("no lease for account %q after the request", tc.wantLease)
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if len(upstreamHeaders) != len(requests) {
		t.Fatalf("upstream saw %d requests, want %d", len(upstreamHeaders), len(requests))
	}
	for i, header := range upstreamHeaders {
		if got := header.Get("X-Prism-Account"); got != "" {
			t.Fatalf("request %d: X-Prism-Account leaked upstream: %q", i, got)
		}
		if got := header.Get("X-Resin-Account"); got != "" {
			t.Fatalf("request %d: X-Resin-Account leaked upstream: %q", i, got)
		}
	}
}

// TestWriteProxyErrorCarriesResinHeader pins the X4 response-header contract.
func TestWriteProxyErrorCarriesResinHeader(t *testing.T) {
	for _, pe := range []*ProxyError{ErrAuthFailed, ErrAuthRequired, ErrPlatformNotFound} {
		rec := httptest.NewRecorder()
		writeProxyError(rec, pe)

		prism := rec.Header().Get("X-Prism-Error")
		resin := rec.Header().Get("X-Resin-Error")
		if prism != pe.PrismError {
			t.Fatalf("X-Prism-Error = %q, want %q", prism, pe.PrismError)
		}
		if resin != prism {
			t.Fatalf("X-Resin-Error = %q, want %q (same as X-Prism-Error)", resin, prism)
		}
	}

	// Deviation X4: the challenge realm stays "Prism"; upstream tests that
	// assert realm="Resin" are adjusted for this release line.
	rec := httptest.NewRecorder()
	writeProxyError(rec, ErrAuthRequired)
	challenge := rec.Header().Get("Proxy-Authenticate")
	if challenge != `Basic realm="Prism"` {
		t.Fatalf("Proxy-Authenticate = %q, want %q", challenge, `Basic realm="Prism"`)
	}
}

// TestReverseProxyErrorCarriesResinHeader checks a real reverse-proxy error
// response, not just the helper.
func TestReverseProxyErrorCarriesResinHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/denied", host)

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:        "tok",
		Events:            NoOpEventEmitter{},
		ProxyBypassRules:  []string{"127.*"},
		DirectDenyPrivate: true,
	})

	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("X-Prism-Error"); got != "DIRECT_TARGET_DENIED" {
		t.Fatalf("X-Prism-Error = %q, want DIRECT_TARGET_DENIED", got)
	}
	if got := rec.Header().Get("X-Resin-Error"); got != "DIRECT_TARGET_DENIED" {
		t.Fatalf("X-Resin-Error = %q, want DIRECT_TARGET_DENIED", got)
	}
}
