package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/platform"
)

func TestRequestLogTargetURLPrivacy(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://user:password@example.com/a%2Fb?key=query-secret#fragment-secret", "https://example.com/a%2Fb"},
		{"https://example.com/path?", "https://example.com/path"},
		{"/relative?token=secret#fragment", "/relative"},
		{"http://[invalid?token=secret", redactedLogValue},
		{"https:opaque-secret", redactedLogValue},
		{"", ""},
	} {
		lifecycle := newRequestLifecycle(NoOpEventEmitter{}, nil, ProxyTypeForward, false)
		lifecycle.setTarget("example.com", tc.raw)
		if got := lifecycle.log.TargetURL; got != tc.want {
			t.Errorf("logged URL: got %q, want %q", got, tc.want)
		}
	}
	if got := sanitizeLogURL("https://example.com/" + strings.Repeat("x", 10000) + "?token=secret"); len(got) > maxLogURLBytes+len("[truncated]") || strings.Contains(got, "secret") {
		t.Fatal("long logged URLs must remain bounded and omit query credentials")
	}
}

func TestRequestLogHeaderPrivacyBeforeTruncation(t *testing.T) {
	header := http.Header{
		"authorization":       {"Bearer authorization-secret"}, // Also cover non-canonical input.
		"Proxy-Authorization": {"Basic proxy-secret"},
		"Cookie":              {"session=cookie-secret"},
		"Set-Cookie":          {"session=response-secret"},
		"X-Api-Key":           {"apikey-secret"},
		"X_Auth_Key":          {"authkey-secret"},
		"X-Session-Id":        {"session-secret"},
		"X-Custom-Identity":   {"custom-secret"},
		"Referer":             {"https://user:password@example.com/path?token=referer-secret"},
		"Location":            {"/next?token=redirect-secret#fragment-secret"},
		"X-Request-Id":        {"trace-id"},
	}
	original := header.Clone()
	var originalWire bytes.Buffer
	if err := header.Write(&originalWire); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0, 15, 60, 300, 4096} {
		payload, total, truncated := captureHeadersWithLimit(header, limit, "x-custom-identity")
		if strings.Contains(string(payload), "-secret") || strings.Contains(string(payload), "password") {
			t.Fatalf("sensitive header value escaped redaction at limit %d: %q", limit, payload)
		}
		if truncated != (total > len(payload)) {
			t.Fatalf("incorrect truncation metadata at limit %d", limit)
		}
	}
	safe := string(captureRequestHeaders(header, "x-custom-identity"))
	if !strings.Contains(safe, "X-Request-Id: trace-id") ||
		!strings.Contains(safe, "Referer: https://example.com/path") ||
		!strings.Contains(safe, "Location: /next") {
		t.Fatalf("non-sensitive diagnostic fields were lost: %q", safe)
	}
	if !reflect.DeepEqual(header, original) || headerWireLen(header) != int64(originalWire.Len()) {
		t.Fatal("log capture changed forwarded headers or traffic accounting")
	}
}

func TestRequestLogUpstreamErrorURLPrivacy(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("roundtrip: %w", &url.Error{Op: "Get", URL: "https://user:password@example.com/path?token=secret", Err: syscall.ECONNREFUSED}),
		&url.Error{Op: "Parse", URL: "/relative?token=secret\nmalformed", Err: syscall.ECONNREFUSED},
		fmt.Errorf("dial https://user:password@example.com/path?token=secret: %w", syscall.ECONNREFUSED),
	} {
		detail := summarizeUpstreamError(err)
		if strings.Contains(detail.Message, "secret") || strings.Contains(detail.Message, "password") || strings.Contains(detail.Message, "malformed") {
			t.Fatalf("URL leaked into upstream error: %q", detail.Message)
		}
		if detail.Kind != "connection_refused" || detail.Errno != "ECONNREFUSED" {
			t.Fatalf("redaction lost error classification: %+v", detail)
		}
	}
	if got := summarizeUpstreamError(errors.New("ordinary diagnostic")).Message; got != "ordinary diagnostic" {
		t.Fatalf("ordinary diagnostic lost: %q", got)
	}
}

func TestReverseProxy_E2ELogPrivacyPreservesForwardingAndLease(t *testing.T) {
	for _, behavior := range []platform.ReverseProxyEmptyAccountBehavior{
		platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
		platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule,
	} {
		t.Run(string(behavior), func(t *testing.T) {
			env := newProxyE2EEnv(t)
			plat, ok := env.pool.GetPlatformByName("plat")
			if !ok {
				t.Fatal("missing test platform")
			}
			plat.ReverseProxyEmptyAccountBehavior = string(behavior)
			plat.ReverseProxyFixedAccountHeaders = []string{"X-Custom-Identity"}
			emitter := newMockEventEmitter()
			const account = "private-business-account"
			const authorization = "Bearer private-business-token"
			const cookie = "session=private-response-cookie"
			upstreamRequests := make(chan *http.Request, 8)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamRequests <- r.Clone(r.Context())
				w.Header().Set("Set-Cookie", cookie)
				w.Header().Set("Location", "/next?token=private-redirect")
				w.Header().Set("X-Custom-Identity", account)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			host := strings.TrimPrefix(upstream.URL, "http://")
			rp := NewReverseProxy(ReverseProxyConfig{
				ProxyToken: "test-proxy-key", Router: env.router, Pool: env.pool, PlatformLookup: env.pool,
				Matcher: BuildAccountMatcher([]model.AccountHeaderRule{{URLPrefix: "*", Headers: []string{"X-Custom-Identity"}}}),
				Events: ConfigAwareEventEmitter{
					Base:                         emitter,
					ReverseProxyLogDetailEnabled: func() bool { return true },
				},
			})
			send := func(pathAccount, overrideAccount string) RequestLogEntry {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/test-proxy-key/plat:%s/http/%s/items?token=private-query", pathAccount, host), nil)
				req.Header.Set("Authorization", authorization)
				req.Header.Set("X-Custom-Identity", account)
				if overrideAccount != "" {
					req.Header.Set("X-Prism-Account", overrideAccount)
				}
				response := httptest.NewRecorder()
				rp.ServeHTTP(response, req)
				if response.Code != http.StatusNoContent {
					t.Fatalf("proxy status: %d, body: %s", response.Code, response.Body.String())
				}
				if response.Header().Get("Set-Cookie") != cookie || response.Header().Get("Location") != "/next?token=private-redirect" {
					t.Fatal("response redaction affected the client's actual headers")
				}
				forwarded := <-upstreamRequests
				if forwarded.Header.Get("Authorization") != authorization || forwarded.Header.Get("X-Custom-Identity") != account || forwarded.URL.RawQuery != "token=private-query" {
					t.Fatal("log privacy changed the upstream request")
				}
				if forwarded.Header.Get("X-Prism-Account") != "" {
					t.Fatal("internal account override leaked to upstream")
				}
				select {
				case entry := <-emitter.logCh:
					for _, field := range []string{entry.TargetURL, string(entry.ReqHeaders), string(entry.RespHeaders)} {
						if strings.Contains(field, "private-") {
							t.Fatalf("sensitive value leaked in log: %q", field)
						}
					}
					if entry.TargetURL != upstream.URL+"/items" {
						t.Fatalf("logged target: %q", entry.TargetURL)
					}
					if entry.EgressBytes < int64(len(authorization)+len(account)) {
						t.Fatal("traffic accounting used shortened log values")
					}
					return entry
				case <-time.After(time.Second):
					t.Fatal("missing request log")
					return RequestLogEntry{}
				}
			}
			first, second := send("", ""), send("", "")
			if !strings.HasPrefix(first.Account, "header-hmac-v1:") || first.Account != second.Account || strings.Contains(first.Account, account) {
				t.Fatal("header accounts need a stable, non-plaintext log identity")
			}
			if env.router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: account}) == nil {
				t.Fatal("header account redaction changed the routing lease key")
			}
			if env.router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: first.Account}) != nil {
				t.Fatal("log pseudonym was used as a routing account")
			}
			if got := logHeaderAccount(account, "different-proxy-key"); got == first.Account {
				t.Fatal("log pseudonyms must be keyed per installation")
			}
			if got := send("named-session", "").Account; got != "named-session" {
				t.Fatalf("explicit session name changed: %q", got)
			}
			if got := send("named-session", "override-session").Account; got != "override-session" {
				t.Fatalf("explicit account override changed: %q", got)
			}
		})
	}
}
