package main

// Coverage for the config helpers and the /status endpoint of public-source-sync.
//
// These were the only unreached code in this command: every parsing helper that
// turns an environment variable into a typed value decides whether a mistyped
// deployment fails loudly or silently falls back to a default, so each one is
// pinned here for both the accepted and the rejected form.
//
// The /status tests also pin the claims their own doc comments make: it reports
// counters, never node data, and never a source's credentials.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/publicsource"
)

// --- gist queries -----------------------------------------------------------

func TestParseQueriesDefaultsWhenUnset(t *testing.T) {
	got, err := parseQueries("")
	if err != nil {
		t.Fatalf("parseQueries(\"\") error = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("an unset query list must fall back to the built-in gist queries")
	}
	if got2, err := parseQueries("   "); err != nil || len(got2) != len(got) {
		t.Fatalf("whitespace-only must behave like unset: got %v, %v", got2, err)
	}
}

func TestParseQueriesTrimsAndDropsBlanks(t *testing.T) {
	got, err := parseQueries(`["vmess://", "  trojan://  ", ""]`)
	if err != nil {
		t.Fatalf("parseQueries error = %v", err)
	}
	want := []string{"vmess://", "trojan://"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseQueriesRejects(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"not JSON", "vmess://"},
		{"only blanks", `["", "   "]`},
		{"not an array", `{"q":"vmess://"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseQueries(tc.raw); err == nil {
				t.Fatalf("parseQueries(%q) = %v, want an error", tc.raw, got)
			}
		})
	}
}

// --- typed env helpers ------------------------------------------------------

func TestParseDurationEnv(t *testing.T) {
	const key = "PUBLIC_SOURCE_TEST_DURATION"
	fallback := 90 * time.Second

	t.Setenv(key, "")
	if got, err := parseDurationEnv(key, fallback); err != nil || got != fallback {
		t.Fatalf("unset: got %v, %v; want %v", got, err, fallback)
	}

	t.Setenv(key, "45s")
	if got, err := parseDurationEnv(key, fallback); err != nil || got != 45*time.Second {
		t.Fatalf("valid: got %v, %v; want 45s", got, err)
	}

	t.Setenv(key, "not-a-duration")
	got, err := parseDurationEnv(key, fallback)
	if err == nil {
		t.Fatalf("invalid: got %v, want an error", got)
	}
	if !strings.Contains(err.Error(), key) {
		t.Fatalf("the error must name the variable, got %q", err)
	}
}

func TestParseIntEnv(t *testing.T) {
	const key = "PUBLIC_SOURCE_TEST_INT"

	t.Setenv(key, "")
	if got, err := parseIntEnv(key, 7); err != nil || got != 7 {
		t.Fatalf("unset: got %v, %v; want 7", got, err)
	}

	t.Setenv(key, "42")
	if got, err := parseIntEnv(key, 7); err != nil || got != 42 {
		t.Fatalf("valid: got %v, %v; want 42", got, err)
	}

	// A negative value parses as an int; rejecting it (if needed) belongs to the
	// caller's range check, so this only pins that the parse itself succeeds.
	t.Setenv(key, "-3")
	if got, err := parseIntEnv(key, 7); err != nil || got != -3 {
		t.Fatalf("negative: got %v, %v; want -3", got, err)
	}

	t.Setenv(key, "12abc")
	if _, err := parseIntEnv(key, 7); err == nil {
		t.Fatal("a non-numeric value must be an error, never a silent fallback")
	}
}

func TestParseBoolEnv(t *testing.T) {
	const key = "PUBLIC_SOURCE_TEST_BOOL"

	t.Setenv(key, "")
	if got, err := parseBoolEnv(key, true); err != nil || !got {
		t.Fatalf("unset: got %v, %v; want the fallback true", got, err)
	}

	t.Setenv(key, "false")
	if got, err := parseBoolEnv(key, true); err != nil || got {
		t.Fatalf("false: got %v, %v; want false", got, err)
	}

	t.Setenv(key, "TRUE")
	if got, err := parseBoolEnv(key, false); err != nil || !got {
		t.Fatalf("TRUE: got %v, %v; want true", got, err)
	}

	t.Setenv(key, "yes")
	if _, err := parseBoolEnv(key, false); err == nil {
		t.Fatal("\"yes\" is not a Go bool and must be rejected loudly")
	}
}

func TestEnvOr(t *testing.T) {
	const key = "PUBLIC_SOURCE_TEST_ENVOR"

	t.Setenv(key, "explicit")
	if got := envOr(key, "fallback"); got != "explicit" {
		t.Fatalf("got %q, want explicit", got)
	}

	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Fatalf("unset: got %q", got)
	}

	t.Setenv(key, "   ")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Fatalf("whitespace-only must be treated as unset, got %q", got)
	}
}

// TestEnvCompatPrecedence pins the compatibility contract with upstream Resin:
// the Prism spelling wins, the legacy spelling is still accepted, and the
// returned source names which one supplied the value so a log line can say so.
func TestEnvCompatPrecedence(t *testing.T) {
	const modern = "PUBLIC_SOURCE_TEST_PRISM_URL"
	const legacy = "PUBLIC_SOURCE_TEST_RESIN_URL"

	t.Setenv(modern, "")
	t.Setenv(legacy, "")
	if value, source := envCompat(modern, legacy); value != "" || source != modern {
		t.Fatalf("neither set: got (%q, %q), want (\"\", %q)", value, source, modern)
	}

	t.Setenv(legacy, "http://legacy.example")
	if value, source := envCompat(modern, legacy); value != "http://legacy.example" || source != legacy {
		t.Fatalf("legacy only: got (%q, %q), want (%q)", value, source, legacy)
	}

	t.Setenv(modern, "http://modern.example")
	if value, source := envCompat(modern, legacy); value != "http://modern.example" || source != modern {
		t.Fatalf("both set: got (%q, %q); the modern spelling must win", value, source)
	}

	// The legacy spelling may be whitespace-padded in a .env file.
	t.Setenv(legacy, "   ")
	if value, source := envCompat(modern, legacy); value != "http://modern.example" || source != modern {
		t.Fatalf("padded legacy: got (%q, %q)", value, source)
	}
}

func TestPresetLabel(t *testing.T) {
	if got := presetLabel(nil); got != "none" {
		t.Fatalf("empty: got %q, want none", got)
	}
	if got := presetLabel([]string{"http", "nodes"}); got != "http+nodes" {
		t.Fatalf("got %q, want http+nodes", got)
	}
}

// --- /status and the method gate --------------------------------------------

func TestSnapshotServerStatusReportsCountersOnly(t *testing.T) {
	collector := newServeTestCollector(t)
	handler := newSnapshotServer(collector, "/sub")
	// Let the collector publish once so the counters are non-trivial.
	if _, err := collector.RefreshNow(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, serveStatusPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/status = %d, want 200", rec.Code)
	}

	var payload struct {
		Nodes       int    `json:"nodes"`
		Bytes       int    `json:"bytes"`
		SourceCount int    `json:"source_count"`
		Degraded    bool   `json:"degraded"`
		LastError   string `json:"last_error"`
		Sources     []struct {
			URL  string `json:"url"`
			Over int    `json:"candidates"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("status body is not the documented JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.Nodes == 0 {
		t.Fatal("/status must report the published node count")
	}

	// The whole point of the endpoint is that it is safe to scrape: it reports
	// counters, never node data. The fixture list contains these literals.
	body := rec.Body.String()
	for _, node := range []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"} {
		if strings.Contains(body, node) {
			t.Fatalf("/status leaked node data (%s):\n%s", node, body)
		}
	}
}

func TestSnapshotServerMethodGate(t *testing.T) {
	handler := newSnapshotServer(newServeTestCollector(t), "/sub")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/sub", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /sub = %d, want 405", method, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
			t.Fatalf("%s: Allow header = %q, want it to advertise GET", method, allow)
		}
	}
}

func TestSnapshotServerHealthAndNotFound(t *testing.T) {
	handler := newSnapshotServer(newServeTestCollector(t), "/sub")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, serveHealthPath, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("%s = %d %q, want 200 ok", serveHealthPath, rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
}

// TestRedactSourceURL pins the redaction used by the refresh log and /status.
//
// A source may be a private subscription with a token in the query or userinfo in
// the authority; the config only checks the scheme. /status is unauthenticated, so
// neither place may echo those parts.
func TestRedactSourceURL(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"plain", "https://example.com/list.txt", "https://example.com/list.txt"},
		{"query token dropped", "https://example.com/sub?token=SECRET", "https://example.com/sub"},
		{"userinfo dropped", "https://user:SECRET@example.com/sub", "https://example.com/sub"},
		{"both dropped", "https://u:p@example.com/sub?token=SECRET&x=1#frag", "https://example.com/sub"},
		{"path kept", "http://feed.example/a/b/c.txt", "http://feed.example/a/b/c.txt"},
		{"port kept", "https://example.com:8443/sub", "https://example.com:8443/sub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSourceURL(tc.raw)
			if got != tc.want {
				t.Fatalf("redactSourceURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for _, secret := range []string{"SECRET", "token=", "user:", "u:p"} {
				if strings.Contains(got, secret) {
					t.Fatalf("redactSourceURL(%q) = %q still contains %q", tc.raw, got, secret)
				}
			}
		})
	}
	// A value that is not a URL must not be echoed back either.
	if got := redactSourceURL("not a url"); strings.Contains(got, "not a url") {
		t.Fatalf("unparsable input echoed back: %q", got)
	}
}

// TestSnapshotServerStatusRedactsSourceURLs checks the end-to-end guarantee: the
// unauthenticated /status payload must not contain a source's credentials.
func TestSnapshotServerStatusRedactsSourceURLs(t *testing.T) {
	collector := publicsource.NewCollector(publicsource.Config{
		Enabled:  true,
		Interval: time.Hour,
		Sources:  []string{"https://user:SECRETPW@feed.example/sub?token=SECRETTOKEN"},
		MaxNodes: 10,
	}, fakeDownloader{body: "1.2.3.4:8080\n"}, nil)
	if _, err := collector.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow: %v", err)
	}

	rec := httptest.NewRecorder()
	newSnapshotServer(collector, "/sub").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, serveStatusPath, nil))
	body := rec.Body.String()
	for _, secret := range []string{"SECRETPW", "SECRETTOKEN", "token="} {
		if strings.Contains(body, secret) {
			t.Fatalf("/status leaked %q:\n%s", secret, body)
		}
	}
	// The host is still reported so an operator can tell sources apart.
	if !strings.Contains(body, "feed.example") {
		t.Fatalf("/status must still identify the source host:\n%s", body)
	}
}
