package checks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// testOutbound is a controllable adapter.Outbound: by default it dials the
// destination directly, so an httptest server on 127.0.0.1 is reachable without
// leaving the loopback interface. A fixed dialErr makes every dial fail with a
// chosen classification.
type testOutbound struct {
	dialErr error
	mu      sync.Mutex
	dials   int
}

func (o *testOutbound) Type() string           { return "test" }
func (o *testOutbound) Tag() string            { return "test" }
func (o *testOutbound) Network() []string      { return []string{"tcp", "udp"} }
func (o *testOutbound) Dependencies() []string { return nil }

func (o *testOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	o.mu.Lock()
	o.dials++
	o.mu.Unlock()
	if o.dialErr != nil {
		return nil, o.dialErr
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, destination.String())
}

func (o *testOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("test outbound: listen packet not supported")
}

var _ adapter.Outbound = (*testOutbound)(nil)

// stubEnabled is an EnabledSource whose per-check overrides are the map.
type stubEnabled struct{ values map[string]bool }

func (s stubEnabled) CheckEnabled(checkID string) (bool, bool) {
	enabled, ok := s.values[checkID]
	return enabled, ok
}

// fakeClock is an injectable clock so reload staleness can be driven exactly.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func boolPtr(value bool) *bool { return &value }

// fixtureRuleYAML is a minimal valid request rule with a single "home" step.
// regionBlock, when not empty, is appended verbatim.
func fixtureRuleYAML(id, url, regionBlock string) string {
	return fmt.Sprintf(`id: %s
version: 2
name: Fixture %s
category: other
enabled: true
ttl: 2h
timeout: 5s
calibrated: fixture
steps:
  - id: home
    request:
      method: GET
      url: %s
outcomes:
  - when: {step: home, status_in: [200], body_contains: ready}
    outcome: available
  - when: {step: home, status_in: [403]}
    outcome: blocked
  - when: {step: home, error: any}
    outcome: error
default: unknown
%s`, id, id, url, regionBlock)
}

// engineFS builds the built-in filesystem of an engine: every entry becomes
// builtin/<name>.
func engineFS(files map[string]string) fs.FS {
	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys["builtin/"+name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

// minimalEngineFS is the smallest built-in set a clean engine needs.
func minimalEngineFS() fs.FS {
	return engineFS(map[string]string{"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", "")})
}

func writeUserRule(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write user rule %s: %v", name, err)
	}
}

// -----------------------------------------------------------------------------
// Small pure helpers
// -----------------------------------------------------------------------------

func TestUserRuleDir(t *testing.T) {
	if got := UserRuleDir(""); got != "" {
		t.Fatalf("UserRuleDir(%q) = %q, want empty", "", got)
	}
	if got := UserRuleDir("   "); got != "" {
		t.Fatalf("UserRuleDir(blank) = %q, want empty", got)
	}
	want := "/var/lib/prism" + string(os.PathSeparator) + "checks.d"
	if got := UserRuleDir("/var/lib/prism"); got != want {
		t.Fatalf("UserRuleDir = %q, want %q", got, want)
	}
}

func TestCheckSettingID(t *testing.T) {
	if got := CheckSettingID("netflix"); got != "check:netflix" {
		t.Fatalf("CheckSettingID = %q, want %q", got, "check:netflix")
	}
}

func TestStripeIndex(t *testing.T) {
	keys := []string{"", "a", "node-1", strings.Repeat("x", 200), "日本語ノード"}
	seen := make(map[uint32]struct{}, len(keys))
	for _, key := range keys {
		index := stripeIndex(key)
		if index >= 64 {
			t.Fatalf("stripeIndex(%q) = %d, want < 64", key, index)
		}
		if again := stripeIndex(key); again != index {
			t.Fatalf("stripeIndex(%q) not deterministic: %d then %d", key, index, again)
		}
		seen[index] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("distinct keys collapsed onto %d stripe(s)", len(seen))
	}
}

func TestClassifyDialError(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"nil error", context.Background(), nil, ""},
		{"expired context", expired, errors.New("dial tcp 10.0.0.1:443: i/o timeout"), "timeout"},
		{"timeout text", context.Background(), errors.New("dial tcp 10.0.0.1:443: i/o timeout"), "timeout"},
		{"wrapped deadline", context.Background(), fmt.Errorf("Get %q: %w", "https://x", context.DeadlineExceeded), "timeout"},
		{"connection refused", context.Background(), errors.New("dial tcp 127.0.0.1:9: connect: connection refused"), "refused"},
		{"refused short text", context.Background(), errors.New("refused"), "refused"},
		{"tls handshake", context.Background(), errors.New("tls: handshake failure"), "tls"},
		{"certificate", context.Background(), errors.New("x509: certificate signed by unknown authority"), "tls"},
		{"dns", context.Background(), errors.New("dns: no such host"), "any"},
		{"canceled context is not a timeout", canceled, errors.New("operation was canceled"), "any"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDialError(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("classifyDialError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRedirectHostMatches(t *testing.T) {
	cases := []struct {
		name     string
		location string
		needle   string
		want     bool
	}{
		{"exact host", "https://www.netflix.com/jp-en/title/1", "www.netflix.com", true},
		{"case insensitive", "https://WWW.Netflix.COM/x", "www.netflix.com", true},
		{"port stripped from location", "https://example.com:8443/path", "example.com", true},
		{"port in needle never matches", "https://example.com:8443/path", "example.com:8443", false},
		{"scheme-less location", "example.com/path", "example.com", true},
		{"query delimiter", "https://example.com?x=1", "example.com", true},
		{"fragment delimiter", "https://example.com#frag", "example.com", true},
		{"protocol relative is not parsed", "//example.com/path", "example.com", false},
		{"different host", "https://other.example/x", "example.com", false},
		{"empty location", "", "example.com", false},
		{"empty needle", "https://example.com/", "", false},
		{"ipv6 literal is unsupported", "https://[2001:db8::1]/x", "[2001:db8::1]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectHostMatches(tc.location, tc.needle); got != tc.want {
				t.Fatalf("redirectHostMatches(%q, %q) = %v, want %v", tc.location, tc.needle, got, tc.want)
			}
		})
	}
}

func TestStepDetail(t *testing.T) {
	outcome := stepResult{
		ID: "home", Status: http.StatusMovedPermanently, Connected: true,
		Duration: 1500 * time.Millisecond, BodyBytes: 12, Truncated: true,
		Unavailable: "CHECK_REQUEST_FAILED",
		Header:      http.Header{"Location": []string{"https://example.test/next"}},
	}
	detail := stepDetail(outcome)
	if detail.Status != http.StatusMovedPermanently || !detail.Connected {
		t.Fatalf("detail = %+v, want status 301 and connected", detail)
	}
	if detail.Ms != 1500 || detail.BodyBytes != 12 || !detail.Truncated {
		t.Fatalf("detail = %+v, want ms 1500, 12 bytes, truncated", detail)
	}
	if detail.Location != "https://example.test/next" {
		t.Fatalf("detail.Location = %q, want the Location header", detail.Location)
	}
	if detail.Unavailable != "CHECK_REQUEST_FAILED" {
		t.Fatalf("detail.Unavailable = %q", detail.Unavailable)
	}

	bare := stepDetail(stepResult{ID: "x", ErrKind: "timeout"})
	if bare.Location != "" || bare.Error != "timeout" {
		t.Fatalf("bare detail = %+v, want no Location and error timeout", bare)
	}
}

func TestNodeClient(t *testing.T) {
	client := nodeClient(&testOutbound{}, 7*time.Second)
	if client.Timeout != 7*time.Second {
		t.Fatalf("client.Timeout = %v, want 7s", client.Timeout)
	}
	if client.CheckRedirect == nil {
		t.Fatal("nodeClient CheckRedirect is nil, want ErrUseLastResponse")
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(req, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", client.Transport)
	}
	if !transport.DisableKeepAlives {
		t.Fatal("transport.DisableKeepAlives = false, want true")
	}
}

// -----------------------------------------------------------------------------
// matches / matchesStep
// -----------------------------------------------------------------------------

func TestMatchesStepMatchers(t *testing.T) {
	header := http.Header{
		"Content-Type": []string{"text/html; charset=utf-8"},
		"X-Region":     []string{"jp-en"},
		"Location":     []string{"https://www.netflix.com/jp-en/title/1"},
	}
	base := stepResult{ID: "home", Status: http.StatusOK, Connected: true, Header: header, Body: `{"countryCode":"JP"} hello`}

	cases := []struct {
		name string
		when When
		step stepResult
		cand OutcomeCase
		want bool
	}{
		{"status_in hit", When{StatusIn: []int{200, 204}}, base, OutcomeCase{}, true},
		{"status_in miss", When{StatusIn: []int{201}}, base, OutcomeCase{}, false},
		{"status_not_in hit", When{StatusNotIn: []int{403, 404}}, base, OutcomeCase{}, true},
		{"status_not_in miss", When{StatusNotIn: []int{200}}, base, OutcomeCase{}, false},
		{"header_contains hit", When{HeaderContains: map[string]string{"Content-Type": "TEXT/HTML"}}, base, OutcomeCase{}, true},
		{"header_contains miss value", When{HeaderContains: map[string]string{"Content-Type": "application/json"}}, base, OutcomeCase{}, false},
		{"header_contains miss header", When{HeaderContains: map[string]string{"X-Absent": "x"}}, base, OutcomeCase{}, false},
		{"header_contains empty needle", When{HeaderContains: map[string]string{"X-Region": ""}}, base, OutcomeCase{}, true},
		{"header_regex hit", When{HeaderRegex: map[string]string{"X-Region": `^[a-z]{2}-en$`}}, base, OutcomeCase{}, true},
		{"header_regex miss", When{HeaderRegex: map[string]string{"X-Region": `^us-`}}, base, OutcomeCase{}, false},
		{"header_regex invalid pattern", When{HeaderRegex: map[string]string{"X-Region": `([a-z`}}, base, OutcomeCase{}, false},
		{"body_contains hit", When{BodyContains: "countryCode"}, base, OutcomeCase{}, true},
		{"body_contains miss", When{BodyContains: "not-found"}, base, OutcomeCase{}, false},
		{"body_not_contains hit", When{BodyNotContain: "captcha"}, base, OutcomeCase{}, true},
		{"body_not_contains miss", When{BodyNotContain: "hello"}, base, OutcomeCase{}, false},
		{"body_regex hit", When{BodyRegex: `"countryCode":"([A-Z]{2})"`}, base, OutcomeCase{bodyRegex: regexp.MustCompile(`"countryCode":"([A-Z]{2})"`)}, true},
		{"body_regex miss", When{BodyRegex: `"countryCode":"([A-Z]{2})"`}, base, OutcomeCase{bodyRegex: regexp.MustCompile(`"nope":"([A-Z]{2})"`)}, false},
		{"body_regex uncompiled candidate is ignored", When{BodyRegex: `never-matches`}, base, OutcomeCase{}, true},
		{"redirect_host hit", When{RedirectHost: "www.netflix.com"}, base, OutcomeCase{}, true},
		{"redirect_host miss", When{RedirectHost: "other.example"}, base, OutcomeCase{}, false},
		{"redirect_host without Location", When{RedirectHost: "www.netflix.com"}, stepResult{ID: "home", Status: 302}, OutcomeCase{}, false},
		{"connected true hit", When{Connected: boolPtr(true)}, base, OutcomeCase{}, true},
		{"connected true miss", When{Connected: boolPtr(true)}, stepResult{ID: "home", Connected: false}, OutcomeCase{}, false},
		{"connected false hit", When{Connected: boolPtr(false)}, stepResult{ID: "home", Connected: false}, OutcomeCase{}, true},
		{"error specific hit", When{Error: "timeout"}, stepResult{ID: "home", ErrKind: "timeout"}, OutcomeCase{}, true},
		{"error specific miss", When{Error: "refused"}, stepResult{ID: "home", ErrKind: "timeout"}, OutcomeCase{}, false},
		{"error any via ErrKind", When{Error: "any"}, stepResult{ID: "home", ErrKind: "tls"}, OutcomeCase{}, true},
		{"error any via Unavailable", When{Error: "any"}, stepResult{ID: "home", Unavailable: "CHECK_REQUEST_FAILED"}, OutcomeCase{}, true},
		{"error any with a clean step", When{Error: "any"}, stepResult{ID: "home", Status: 200, Connected: true}, OutcomeCase{}, false},
		{"empty condition matches", When{}, base, OutcomeCase{}, true},
		{"all fields AND together", When{StatusIn: []int{200}, BodyContains: "hello", Connected: boolPtr(true)}, base, OutcomeCase{}, true},
		{"one field fails the AND", When{StatusIn: []int{200}, BodyContains: "missing"}, base, OutcomeCase{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesStep(tc.when, tc.step, tc.cand); got != tc.want {
				t.Fatalf("matchesStep = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesWithEmptyStepEvaluatesEveryStep(t *testing.T) {
	steps := map[string]stepResult{
		"one": {ID: "one", Status: 200, Connected: true},
		"two": {ID: "two", Status: 404, Connected: false, Body: "has needle"},
	}

	// Any step that satisfies the condition wins, in map order.
	if !matches(OutcomeCase{When: When{BodyContains: "needle"}}, steps) {
		t.Fatal("a condition without a step must match when any step matches")
	}
	if matches(OutcomeCase{When: When{BodyContains: "absent"}}, steps) {
		t.Fatal("no step matches, want false")
	}
	if !matches(OutcomeCase{When: When{Connected: boolPtr(false)}}, steps) {
		t.Fatal("connected:false must match the disconnected step")
	}
	if !matches(OutcomeCase{When: When{StatusNotIn: []int{200}}}, steps) {
		t.Fatal("status_not_in must match the 404 step")
	}
	if matches(OutcomeCase{When: When{StatusIn: []int{500}}}, steps) {
		t.Fatal("no step carries 500, want false")
	}

	// No steps at all: the step-less branch reports false.
	if matches(OutcomeCase{When: When{Connected: boolPtr(false)}}, nil) {
		t.Fatal("an empty step set must not match")
	}
	if matches(OutcomeCase{When: When{BodyContains: ""}}, map[string]stepResult{}) {
		t.Fatal("an empty step set must not match")
	}
}

func TestMatchesWithNamedStep(t *testing.T) {
	steps := map[string]stepResult{
		"one": {ID: "one", Status: 200, Body: "hello"},
		"two": {ID: "two", Status: 404},
	}
	if !matches(OutcomeCase{When: When{Step: "one", BodyContains: "hello"}}, steps) {
		t.Fatal("named step must be evaluated")
	}
	if matches(OutcomeCase{When: When{Step: "two", BodyContains: "hello"}}, steps) {
		t.Fatal("only the named step must be evaluated")
	}
	if matches(OutcomeCase{When: When{Step: "missing", StatusIn: []int{200}}}, steps) {
		t.Fatal("an unknown step must not match")
	}
}

// -----------------------------------------------------------------------------
// Engine construction, reload and metadata
// -----------------------------------------------------------------------------

func TestNewEngineLoadsEmbeddedBuiltins(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	engine := NewEngine(Options{
		Logf: func(format string, args ...any) {
			mu.Lock()
			messages = append(messages, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	})
	if errs := engine.LoadErrors(); len(errs) != 0 {
		t.Fatalf("builtin load errors: %v", errs)
	}
	rules := engine.Rules()
	if len(rules) != 8 {
		t.Fatalf("builtin rule count = %d, want 8", len(rules))
	}
	for _, rule := range rules {
		if rule.Source != "builtin" || rule.Path != "" {
			t.Fatalf("builtin rule %s: source %q path %q", rule.ID, rule.Source, rule.Path)
		}
		if !rule.Enabled {
			t.Fatalf("builtin rule %s is unexpectedly disabled", rule.ID)
		}
	}
	if engine.concurrency != DefaultConcurrencyPerCheck {
		t.Fatalf("default concurrency = %d, want %d", engine.concurrency, DefaultConcurrencyPerCheck)
	}
	if engine.hotReload != DefaultHotReload {
		t.Fatalf("default hot reload = %v, want %v", engine.hotReload, DefaultHotReload)
	}
	if len(messages) != 0 {
		t.Fatalf("unexpected load warnings: %v", messages)
	}
}

func TestNewEngineWithUserDirEmpty(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	if errs := engine.LoadErrors(); len(errs) != 0 {
		t.Fatalf("load errors = %v, want none", errs)
	}
}

func TestRulesMetadataAndUserOverride(t *testing.T) {
	fsys := engineFS(map[string]string{"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", "")})
	engine := NewEngine(Options{BuiltinFS: fsys})
	rules := engine.Rules()
	if len(rules) != 1 {
		t.Fatalf("rule count = %d, want 1", len(rules))
	}
	builtin := rules[0]
	if builtin.ID != "alpha" || builtin.Version != 2 || builtin.Name != "Fixture alpha" || builtin.Category != "other" {
		t.Fatalf("rule metadata = %+v", builtin)
	}
	if builtin.TTL != "2h0m0s" || builtin.Timeout != "5s" {
		t.Fatalf("ttl/timeout = %q/%q, want 2h0m0s/5s", builtin.TTL, builtin.Timeout)
	}
	if len(builtin.Steps) != 1 || builtin.Steps[0] != "home" {
		t.Fatalf("steps = %v, want [home]", builtin.Steps)
	}
	if builtin.Calibrated != "fixture" {
		t.Fatalf("calibrated = %q, want fixture", builtin.Calibrated)
	}
	if builtin.Path != "" {
		t.Fatalf("builtin path = %q, want empty", builtin.Path)
	}

	if _, ok := engine.Rule("alpha"); !ok {
		t.Fatal("Rule(alpha) not found")
	}
	if _, ok := engine.Rule("missing"); ok {
		t.Fatal("Rule(missing) unexpectedly found")
	}

	// A user rule with the same id replaces the built-in in place.
	dir := t.TempDir()
	writeUserRule(t, dir, "alpha.yaml", fixtureRuleYAML("alpha", "https://user.test/", ""))
	overridden := NewEngine(Options{BuiltinFS: fsys, UserDir: dir})
	rules = overridden.Rules()
	if len(rules) != 1 {
		t.Fatalf("override produced %d rules, want 1", len(rules))
	}
	if rules[0].Source != "user" || rules[0].Path == "" {
		t.Fatalf("override = %+v, want a user rule with a path", rules[0])
	}
	rule, ok := overridden.Rule("alpha")
	if !ok || rule.Source != "user" {
		t.Fatalf("Rule(alpha) = %+v, want the user rule", rule)
	}

	// Removing the user rule restores the built-in on the next reload.
	if err := os.Remove(filepath.Join(dir, "alpha.yaml")); err != nil {
		t.Fatalf("remove user rule: %v", err)
	}
	if err := overridden.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	rule, _ = overridden.Rule("alpha")
	if rule.Source != "builtin" {
		t.Fatalf("after removal: source %q, want builtin", rule.Source)
	}
	// Rules() only reports the path of a user rule.
	if reported := overridden.Rules(); len(reported) != 1 || reported[0].Path != "" {
		t.Fatalf("reported rules = %+v, want one builtin rule without a path", reported)
	}
}

func TestReloadMergesAndSortsRules(t *testing.T) {
	fsys := engineFS(map[string]string{
		"zeta.yaml":  fixtureRuleYAML("zeta", "https://example.test/", ""),
		"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", ""),
	})
	dir := t.TempDir()
	writeUserRule(t, dir, "beta.yaml", fixtureRuleYAML("beta", "https://example.test/", ""))

	engine := NewEngine(Options{BuiltinFS: fsys, UserDir: dir})
	ids := make([]string, 0, len(engine.Rules()))
	for _, rule := range engine.Rules() {
		ids = append(ids, rule.ID)
	}
	want := []string{"alpha", "beta", "zeta"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("rule order = %v, want %v", ids, want)
	}
}

func TestReloadRecordsBadRulesWithoutPanicking(t *testing.T) {
	fsys := engineFS(map[string]string{"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", "")})
	dir := t.TempDir()
	writeUserRule(t, dir, "broken.yaml", "id: broken\nthis is not a rule\n")
	writeUserRule(t, dir, "mismatch.yaml", fixtureRuleYAML("different", "https://example.test/", ""))
	writeUserRule(t, dir, "ignored.txt", "not a rule at all")

	var mu sync.Mutex
	var messages []string
	engine := NewEngine(Options{
		BuiltinFS: fsys,
		UserDir:   dir,
		Logf: func(format string, args ...any) {
			mu.Lock()
			messages = append(messages, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	})

	errs := engine.LoadErrors()
	if len(errs) != 2 {
		t.Fatalf("load errors = %v, want 2 (broken + mismatched name)", errs)
	}
	if _, ok := engine.Rule("alpha"); !ok {
		t.Fatal("a bad user rule must not drop the good rules")
	}
	if _, ok := engine.Rule("broken"); ok {
		t.Fatal("a broken rule must not be loaded")
	}
	if _, ok := engine.Rule("different"); ok {
		t.Fatal("a rule whose id differs from the file name must not be loaded")
	}
	if len(messages) != 2 {
		t.Fatalf("log warnings = %v, want 2", messages)
	}

	// LoadErrors returns a copy: mutating it must not corrupt the engine.
	copied := engine.LoadErrors()
	copied[0] = errors.New("mutated")
	if engine.LoadErrors()[0].Error() == "mutated" {
		t.Fatal("LoadErrors did not return a copy")
	}
}

func TestEnabledRuleOverride(t *testing.T) {
	fsys := engineFS(map[string]string{
		"alpha.yaml": strings.Replace(fixtureRuleYAML("alpha", "https://example.test/", ""), "enabled: true", "enabled: false", 1),
		"beta.yaml":  fixtureRuleYAML("beta", "https://example.test/", ""),
	})
	enabled := stubEnabled{values: map[string]bool{"alpha": true, "beta": false}}
	engine := NewEngine(Options{BuiltinFS: fsys, EnabledSource: enabled})

	byID := map[string]bool{}
	for _, rule := range engine.Rules() {
		byID[rule.ID] = rule.Enabled
	}
	if !byID["alpha"] {
		t.Fatal("alpha must be enabled by the override")
	}
	if byID["beta"] {
		t.Fatal("beta must be disabled by the override")
	}

	selected := engine.selectRules(nil)
	if len(selected) != 1 || selected[0].ID != "alpha" {
		t.Fatalf("selectRules = %v, want just alpha", selected)
	}
	// Explicitly asking for a disabled rule yields nothing.
	if got := engine.selectRules([]string{"beta"}); len(got) != 0 {
		t.Fatalf("selectRules(beta) = %v, want empty", got)
	}
	// Without an override the rule's own flag decides.
	plain := NewEngine(Options{BuiltinFS: fsys})
	if got := plain.selectRules([]string{"beta"}); len(got) != 1 || got[0].ID != "beta" {
		t.Fatalf("selectRules(beta) = %v, want beta (enabled in the file)", got)
	}
	if got := plain.selectRules([]string{"alpha"}); len(got) != 0 {
		t.Fatalf("selectRules(alpha) = %v, want empty (disabled in the file)", got)
	}
}

func TestSelectRules(t *testing.T) {
	fsys := engineFS(map[string]string{
		"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", ""),
		"beta.yaml":  fixtureRuleYAML("beta", "https://example.test/", ""),
	})
	engine := NewEngine(Options{BuiltinFS: fsys})

	all := engine.selectRules(nil)
	if len(all) != 2 {
		t.Fatalf("selectRules(nil) = %d rules, want 2", len(all))
	}
	if all[0].ID != "alpha" || all[1].ID != "beta" {
		t.Fatalf("selectRules(nil) order = %s,%s, want alpha,beta", all[0].ID, all[1].ID)
	}
	if got := engine.selectRules([]string{"beta"}); len(got) != 1 || got[0].ID != "beta" {
		t.Fatalf("selectRules(beta) = %v, want just beta", got)
	}
	if got := engine.selectRules([]string{"  beta  "}); len(got) != 1 || got[0].ID != "beta" {
		t.Fatalf("selectRules(trimmed) = %v, want just beta", got)
	}
	if got := engine.selectRules([]string{"unknown"}); len(got) != 0 {
		t.Fatalf("selectRules(unknown) = %v, want empty", got)
	}
	if got := engine.selectRules([]string{"unknown", "alpha"}); len(got) != 1 || got[0].ID != "alpha" {
		t.Fatalf("selectRules(unknown,alpha) = %v, want just alpha", got)
	}
}

func TestReloadIfStale(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	fsys := engineFS(map[string]string{"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", "")})
	engine := NewEngine(Options{BuiltinFS: fsys, UserDir: dir, Now: clock.Now, HotReload: time.Hour})
	if got := len(engine.Rules()); got != 1 {
		t.Fatalf("initial rules = %d, want 1", got)
	}

	writeUserRule(t, dir, "beta.yaml", fixtureRuleYAML("beta", "https://example.test/", ""))

	engine.reloadIfStale()
	if got := len(engine.Rules()); got != 1 {
		t.Fatalf("rules = %d, want 1: a fresh load must not be repeated", got)
	}

	clock.advance(time.Hour)
	engine.reloadIfStale()
	if got := len(engine.Rules()); got != 2 {
		t.Fatalf("rules = %d, want 2: a stale engine must reload", got)
	}

	writeUserRule(t, dir, "gamma.yaml", fixtureRuleYAML("gamma", "https://example.test/", ""))
	engine.reloadIfStale()
	if got := len(engine.Rules()); got != 2 {
		t.Fatalf("rules = %d, want 2: still inside the hot-reload window", got)
	}

	clock.advance(2 * time.Hour)
	engine.reloadIfStale()
	if got := len(engine.Rules()); got != 3 {
		t.Fatalf("rules = %d, want 3 after the window elapsed", got)
	}
}

// -----------------------------------------------------------------------------
// Concurrency gate
// -----------------------------------------------------------------------------

func TestAcquireAndRelease(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys, ConcurrencyPerCheck: 1})

	ctx := context.Background()
	release, ok := engine.acquire(ctx, "alpha")
	if !ok {
		t.Fatal("first acquire must succeed")
	}

	// The single slot is taken: a canceled waiter reports busy instead of blocking.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := engine.acquire(canceled, "alpha"); ok {
		t.Fatal("acquire must fail while the slot is held")
	}

	// Another rule has its own semaphore.
	otherRelease, ok := engine.acquire(ctx, "beta")
	if !ok {
		t.Fatal("a different rule id must have an independent slot")
	}
	otherRelease()

	release()
	fresh, ok := engine.acquire(ctx, "alpha")
	if !ok {
		t.Fatal("the released slot must be reusable")
	}
	fresh()
}

func TestAcquireBoundsConcurrency(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys, ConcurrencyPerCheck: 2})

	ctx := context.Background()
	first, ok := engine.acquire(ctx, "alpha")
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	second, ok := engine.acquire(ctx, "alpha")
	if !ok {
		t.Fatal("second acquire must succeed")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := engine.acquire(canceled, "alpha"); ok {
		t.Fatal("the third acquire must be rejected once the bound is reached")
	}
	first()
	third, ok := engine.acquire(ctx, "alpha")
	if !ok {
		t.Fatal("a released slot must be reusable")
	}
	third()
	second()
}

func TestSetConcurrencyPerCheck(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys, ConcurrencyPerCheck: 1})

	// Hold the only slot, then widen the bound: existing semaphores are dropped.
	held, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("initial acquire must succeed")
	}
	engine.SetConcurrencyPerCheck(3)
	if engine.concurrency != 3 {
		t.Fatalf("concurrency = %d, want 3", engine.concurrency)
	}
	a, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("first acquire after the change must succeed")
	}
	b, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("second acquire after the change must succeed")
	}
	c, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("third acquire after the change must succeed")
	}
	// All three slots are now held, so the send cannot be chosen and a canceled
	// waiter must report busy.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := engine.acquire(canceled, "alpha"); ok {
		t.Fatal("the bound of 3 must still be enforced")
	}
	c()
	a()
	b()
	held()

	// A non-positive value falls back to the default bound.
	engine.SetConcurrencyPerCheck(0)
	if engine.concurrency != DefaultConcurrencyPerCheck {
		t.Fatalf("concurrency = %d, want the default %d", engine.concurrency, DefaultConcurrencyPerCheck)
	}
	engine.SetConcurrencyPerCheck(-4)
	if engine.concurrency != DefaultConcurrencyPerCheck {
		t.Fatalf("negative concurrency = %d, want the default", engine.concurrency)
	}
	x, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("acquire after the default reset must succeed")
	}
	y, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("the default bound allows two concurrent slots")
	}
	x()
	y()

	// A nil engine must not panic.
	var nilEngine *Engine
	nilEngine.SetConcurrencyPerCheck(2)
}

// -----------------------------------------------------------------------------
// Run orchestration
// -----------------------------------------------------------------------------

func TestRunSelectsEnabledRules(t *testing.T) {
	fsys := engineFS(map[string]string{
		"alpha.yaml": fixtureRuleYAML("alpha", "https://example.test/", ""),
		"beta.yaml":  fixtureRuleYAML("beta", "https://example.test/", ""),
	})
	engine := NewEngine(Options{BuiltinFS: fsys})

	results := engine.Run(context.Background(), RunRequest{})
	if len(results) != 2 {
		t.Fatalf("Run = %d results, want 2", len(results))
	}
	for _, result := range results {
		if result.Outcome != OutcomeError || result.ErrorCode != "CHECK_NO_NODE" {
			t.Fatalf("result = %+v, want a clean CHECK_NO_NODE error", result)
		}
		if result.Detail == nil {
			t.Fatal("detail must not be nil")
		}
	}

	selected := engine.Run(context.Background(), RunRequest{RuleIDs: []string{"beta"}})
	if len(selected) != 1 || selected[0].CheckID != "beta" {
		t.Fatalf("Run(beta) = %+v, want just beta", selected)
	}

	if got := engine.Run(context.Background(), RunRequest{RuleIDs: []string{"unknown"}}); got != nil {
		t.Fatalf("Run(unknown) = %+v, want nil", got)
	}
}

func TestRunRecordsEgressAndTTL(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys})

	egress := netip.MustParseAddr("::ffff:203.0.113.7")
	results := engine.Run(context.Background(), RunRequest{Egress: egress})
	if len(results) != 1 {
		t.Fatalf("Run = %d results, want 1", len(results))
	}
	if results[0].EgressIP != "203.0.113.7" {
		t.Fatalf("EgressIP = %q, want the unmapped 203.0.113.7", results[0].EgressIP)
	}
	if results[0].TTL != 2*time.Hour {
		t.Fatalf("TTL = %v, want 2h", results[0].TTL)
	}
	if results[0].Version != 2 {
		t.Fatalf("Version = %d, want 2", results[0].Version)
	}
}

func TestRunCanceledContext(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := engine.Run(ctx, RunRequest{})
	if len(results) != 1 {
		t.Fatalf("Run = %d results, want 1", len(results))
	}
	if results[0].Outcome != OutcomeError || results[0].ErrorCode != "CHECK_CANCELED" {
		t.Fatalf("result = %+v, want CHECK_CANCELED", results[0])
	}
}

func TestRunReportsBusyWhenTheSlotIsHeld(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys, ConcurrencyPerCheck: 1})

	held, ok := engine.acquire(context.Background(), "alpha")
	if !ok {
		t.Fatal("pre-acquire must succeed")
	}
	defer held()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	results := engine.Run(ctx, RunRequest{})
	if len(results) != 1 {
		t.Fatalf("Run = %d results, want 1", len(results))
	}
	if results[0].Outcome != OutcomeError || results[0].ErrorCode != "CHECK_BUSY" {
		t.Fatalf("result = %+v, want CHECK_BUSY", results[0])
	}
}

func TestRunConcurrentlyPerNode(t *testing.T) {
	fsys := minimalEngineFS()
	engine := NewEngine(Options{BuiltinFS: fsys, ConcurrencyPerCheck: 4})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := RunRequest{NodeKey: fmt.Sprintf("node-%d", i%8)}
			results := engine.Run(context.Background(), req)
			if len(results) != 1 || results[0].ErrorCode != "CHECK_NO_NODE" {
				t.Errorf("node %d: results = %+v, want one CHECK_NO_NODE", i, results)
			}
		}(i)
	}
	wg.Wait()
}

// -----------------------------------------------------------------------------
// Step execution
// -----------------------------------------------------------------------------

func TestExecuteStepWithoutHandler(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	outcome := engine.executeStep(context.Background(), Step{ID: "bare"}, &testOutbound{})
	if outcome.Unavailable != "CHECK_RULE_INVALID" {
		t.Fatalf("outcome = %+v, want CHECK_RULE_INVALID", outcome)
	}
}

func TestExecuteTCP(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	t.Run("connected", func(t *testing.T) {
		step := Step{ID: "smtp", TCP: &TCP{Host: "127.0.0.1", Port: port}}
		outcome := engine.executeTCP(context.Background(), step, &testOutbound{})
		if !outcome.Connected || outcome.ErrKind != "" {
			t.Fatalf("outcome = %+v, want connected", outcome)
		}
		if outcome.Duration < 0 {
			t.Fatalf("duration = %v, want >= 0", outcome.Duration)
		}
	})

	t.Run("refused", func(t *testing.T) {
		step := Step{ID: "smtp", TCP: &TCP{Host: "127.0.0.1", Port: port}}
		outbound := &testOutbound{dialErr: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")}
		outcome := engine.executeTCP(context.Background(), step, outbound)
		if outcome.Connected || outcome.ErrKind != "refused" {
			t.Fatalf("outcome = %+v, want a refused error", outcome)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		step := Step{ID: "smtp", TCP: &TCP{Host: "127.0.0.1", Port: port}}
		outbound := &testOutbound{dialErr: errors.New("dial tcp 10.0.0.1:25: i/o timeout")}
		outcome := engine.executeTCP(context.Background(), step, outbound)
		if outcome.ErrKind != "timeout" {
			t.Fatalf("ErrKind = %q, want timeout", outcome.ErrKind)
		}
	})
}

func TestExecuteRequestInvalidURL(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	step := Step{ID: "home", Request: &Request{URL: "http://%zz"}}
	outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
	if outcome.Unavailable != "CHECK_REQUEST_INVALID" {
		t.Fatalf("outcome = %+v, want CHECK_REQUEST_INVALID", outcome)
	}
}

func TestExecuteRequestAgainstServer(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	region := `{"region":"JP"} ready`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.Header().Set("X-Seen", r.Header.Get("X-Test"))
			w.Header().Set("X-UA", r.Header.Get("User-Agent"))
			_, _ = w.Write([]byte(region))
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/big":
			_, _ = w.Write([]byte("0123456789"))
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = w.Write([]byte("done"))
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("late"))
		case "/echo":
			requestBody, _ := io.ReadAll(r.Body)
			_, _ = w.Write(requestBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Run("ok with headers", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{
			Method:  "get",
			URL:     server.URL + "/ready",
			Headers: map[string]string{"X-Test": "abc"},
		}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Status != http.StatusOK || !outcome.Connected {
			t.Fatalf("outcome = %+v, want 200 and connected", outcome)
		}
		if outcome.Body != region || outcome.BodyBytes != len(region) || outcome.Truncated {
			t.Fatalf("body = %q (%d bytes, truncated=%v)", outcome.Body, outcome.BodyBytes, outcome.Truncated)
		}
		if outcome.Header.Get("X-Seen") != "abc" {
			t.Fatalf("X-Seen = %q, want the step header", outcome.Header.Get("X-Seen"))
		}
		if outcome.Header.Get("X-UA") != BrowserUserAgent {
			t.Fatalf("User-Agent = %q, want the browser UA", outcome.Header.Get("X-UA"))
		}
	})

	t.Run("request body is sent", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{
			Method: "POST",
			URL:    server.URL + "/echo",
			Body:   "payload",
		}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Status != http.StatusOK || outcome.Body != "payload" {
			t.Fatalf("outcome = %+v, want the echoed request body", outcome)
		}
	})

	t.Run("non 200 still connected", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/forbidden"}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Status != http.StatusForbidden || !outcome.Connected {
			t.Fatalf("outcome = %+v, want 403 and connected", outcome)
		}
	})

	t.Run("max body bytes truncates", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/big", MaxBodyBytes: 4}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Body != "0123" || outcome.BodyBytes != 4 || !outcome.Truncated {
			t.Fatalf("outcome = %+v, want a 4 byte truncated body", outcome)
		}
		if outcome.Status != http.StatusOK || !outcome.Connected {
			t.Fatalf("outcome = %+v, want 200 and connected", outcome)
		}
	})

	t.Run("default body limit applies", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/ready", MaxBodyBytes: 0}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Truncated || outcome.BodyBytes != len(region) {
			t.Fatalf("outcome = %+v, want the default limit and no truncation", outcome)
		}
	})

	t.Run("redirect not followed", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/start"}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Status != http.StatusFound {
			t.Fatalf("status = %d, want 302", outcome.Status)
		}
		if outcome.Header.Get("Location") != "/final" {
			t.Fatalf("Location = %q, want /final", outcome.Header.Get("Location"))
		}
		// Go's http.Redirect writes a small HTML body of its own; the point is
		// that the redirect target body ("done") never reaches the outcome.
		if strings.Contains(outcome.Body, "done") {
			t.Fatalf("body = %q, must not contain the redirect target", outcome.Body)
		}
		if !outcome.Connected {
			t.Fatal("a 302 response is still a connection")
		}
	})

	t.Run("redirect followed", func(t *testing.T) {
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/start", FollowRedirects: true}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Status != http.StatusOK || outcome.Body != "done" {
			t.Fatalf("outcome = %+v, want the followed 200/done", outcome)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		step := Step{ID: "home", Request: &Request{URL: server.URL + "/slow"}}
		outcome := engine.executeRequest(ctx, step, &testOutbound{})
		if outcome.ErrKind != "timeout" {
			t.Fatalf("ErrKind = %q, want timeout", outcome.ErrKind)
		}
		if outcome.Unavailable != "" {
			t.Fatalf("Unavailable = %q, want empty for a classified error", outcome.Unavailable)
		}
	})

	t.Run("dial failure is classified", func(t *testing.T) {
		outbound := &testOutbound{dialErr: errors.New("dial tcp: connect: connection refused")}
		step := Step{ID: "home", Request: &Request{URL: "http://127.0.0.1:1/"}}
		outcome := engine.executeRequest(context.Background(), step, outbound)
		if outcome.ErrKind != "refused" || outcome.Connected {
			t.Fatalf("outcome = %+v, want a refused error", outcome)
		}
	})

	t.Run("unreadable body", func(t *testing.T) {
		url := startTruncatedBodyServer(t)
		step := Step{ID: "home", Request: &Request{URL: url}}
		outcome := engine.executeRequest(context.Background(), step, &testOutbound{})
		if outcome.Unavailable != "CHECK_BODY_UNREADABLE" {
			t.Fatalf("outcome = %+v, want CHECK_BODY_UNREADABLE", outcome)
		}
	})

	t.Run("unclassified failure", func(t *testing.T) {
		outbound := &testOutbound{dialErr: errors.New("dns: no such host")}
		step := Step{ID: "home", Request: &Request{URL: "http://nonexistent.invalid/"}}
		outcome := engine.executeRequest(context.Background(), step, outbound)
		if outcome.ErrKind != "any" {
			t.Fatalf("ErrKind = %q, want any", outcome.ErrKind)
		}
	})
}

// startTruncatedBodyServer answers with a Content-Length it never satisfies, so
// the client fails while reading the body.
func startTruncatedBodyServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the request first: writing the response before the client has a
		// pending request makes the transport treat it as unsolicited and drop
		// the connection instead of surfacing the short-body read error.
		reader := bufio.NewReader(conn)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line == "\r\n" {
				break
			}
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/plain\r\n\r\nshort"))
	}()
	return "http://" + listener.Addr().String() + "/"
}

// -----------------------------------------------------------------------------
// End-to-end: Run drives the real nodes
// -----------------------------------------------------------------------------

func TestRunRuleAgainstServer(t *testing.T) {
	var body string
	var status int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	set := func(code int, payload string) {
		mu.Lock()
		status = code
		body = payload
		mu.Unlock()
	}

	region := "region:\n  step: home\n  body_regex: '\"region\":\"([A-Z]{2})\"'\n"
	fsys := engineFS(map[string]string{"probe.yaml": fixtureRuleYAML("probe", server.URL+"/home", region)})
	engine := NewEngine(Options{BuiltinFS: fsys})

	t.Run("available", func(t *testing.T) {
		set(http.StatusOK, `{"region":"JP"} ready`)
		results := engine.Run(context.Background(), RunRequest{
			NodeKey:  "node-1",
			Outbound: &testOutbound{},
			Egress:   netip.MustParseAddr("198.51.100.9"),
		})
		if len(results) != 1 {
			t.Fatalf("results = %d, want 1", len(results))
		}
		result := results[0]
		if result.Outcome != OutcomeAvailable {
			t.Fatalf("outcome = %q, want available (detail %+v)", result.Outcome, result.Detail)
		}
		if result.Region != "JP" {
			t.Fatalf("region = %q, want JP", result.Region)
		}
		if result.EgressIP != "198.51.100.9" {
			t.Fatalf("egress = %q, want 198.51.100.9", result.EgressIP)
		}
		if result.ErrorCode != "" {
			t.Fatalf("error code = %q, want empty", result.ErrorCode)
		}
		detail, ok := result.Detail["home"].(stepDetailValue)
		if !ok {
			t.Fatalf("detail[home] type = %T, want stepDetailValue", result.Detail["home"])
		}
		if detail.Status != http.StatusOK || !detail.Connected {
			t.Fatalf("step detail = %+v, want 200 and connected", detail)
		}
		if result.LatencyMs < 0 {
			t.Fatalf("latency = %d, want >= 0", result.LatencyMs)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		set(http.StatusForbidden, "")
		results := engine.Run(context.Background(), RunRequest{Outbound: &testOutbound{}})
		if len(results) != 1 || results[0].Outcome != OutcomeBlocked {
			t.Fatalf("results = %+v, want blocked", results)
		}
		if results[0].Region != "" {
			t.Fatalf("region = %q, want empty", results[0].Region)
		}
	})

	t.Run("error", func(t *testing.T) {
		engineNoNode := NewEngine(Options{BuiltinFS: fsys})
		results := engineNoNode.Run(context.Background(), RunRequest{Outbound: &testOutbound{dialErr: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")}})
		if len(results) != 1 || results[0].Outcome != OutcomeError {
			t.Fatalf("results = %+v, want error", results)
		}
		// A classified transport error drives the "error: any" outcome. The
		// ErrorCode stays empty here: only the *unavailable* infrastructure
		// failures (CHECK_*) fill it, while the dial classification lands in
		// the per-step detail.
		if results[0].ErrorCode != "" {
			t.Fatalf("error code = %q, want empty for a classified dial error", results[0].ErrorCode)
		}
		detail, ok := results[0].Detail["home"].(stepDetailValue)
		if !ok || detail.Error != "refused" {
			t.Fatalf("detail[home] = %+v, want error refused", results[0].Detail["home"])
		}
	})

	t.Run("unavailable step carries the error code", func(t *testing.T) {
		brokenURL := startTruncatedBodyServer(t)
		brokenFS := engineFS(map[string]string{"probe.yaml": fixtureRuleYAML("probe", brokenURL, "")})
		broken := NewEngine(Options{BuiltinFS: brokenFS})
		results := broken.Run(context.Background(), RunRequest{Outbound: &testOutbound{}})
		if len(results) != 1 || results[0].Outcome != OutcomeError {
			t.Fatalf("results = %+v, want the error outcome", results)
		}
		// An *unavailable* step (not a classified dial error) fills ErrorCode.
		if results[0].ErrorCode != "CHECK_BODY_UNREADABLE" {
			t.Fatalf("error code = %q, want CHECK_BODY_UNREADABLE", results[0].ErrorCode)
		}
	})
}

// TestExecuteStepDispatchesToTCP routes a TCP step through executeStep so the
// dispatcher's tcp branch is exercised, not only executeTCP directly.
func TestExecuteStepDispatchesToTCP(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()
	step := Step{ID: "smtp", TCP: &TCP{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}}
	outcome := engine.executeStep(context.Background(), step, &testOutbound{})
	if !outcome.Connected || outcome.ID != "smtp" {
		t.Fatalf("outcome = %+v, want a connected smtp step", outcome)
	}
}

// TestExecuteStepDispatchesToRequest routes a request step through executeStep.
func TestExecuteStepDispatchesToRequest(t *testing.T) {
	engine := NewEngine(Options{BuiltinFS: minimalEngineFS()})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	step := Step{ID: "home", Request: &Request{URL: server.URL + "/"}}
	outcome := engine.executeStep(context.Background(), step, &testOutbound{})
	if outcome.Status != http.StatusOK || outcome.Body != "ok" {
		t.Fatalf("outcome = %+v, want the 200/ok response", outcome)
	}
}

// TestExtractRegionGuards pins the guard branches of extractRegion that sit in
// front of the region matchers, plus the fallback to the executed step.
func TestExtractRegionGuards(t *testing.T) {
	if region := extractRegion(nil, nil); region != "" {
		t.Fatalf("extractRegion(nil) = %q, want empty", region)
	}
	if region := extractRegion(&RegionSpec{}, nil); region != "" {
		t.Fatalf("extractRegion(no matchers) = %q, want empty", region)
	}
	// A spec whose step is absent falls back to whatever step executed.
	spec := &RegionSpec{Step: "missing", compiled: regexp.MustCompile(`"countryCode":"([A-Z]{2})"`)}
	steps := map[string]stepResult{"home": {ID: "home", Body: `{"countryCode":"DE"}`}}
	if region := extractRegion(spec, steps); region != "DE" {
		t.Fatalf("region = %q, want DE from the only executed step", region)
	}
}

func TestRegionFromMatch(t *testing.T) {
	cases := []struct {
		match []string
		want  string
	}{
		{nil, ""},
		{[]string{"whole"}, ""},
		{[]string{"whole", "JP"}, "JP"},
		{[]string{"whole", "jp"}, "JP"},
		{[]string{"whole", " JP "}, "JP"},
		{[]string{"whole", "ABC"}, ""},
		{[]string{"whole", "1A"}, ""},
		{[]string{"whole", ""}, ""},
	}
	for _, tc := range cases {
		if got := regionFromMatch(tc.match); got != tc.want {
			t.Fatalf("regionFromMatch(%v) = %q, want %q", tc.match, got, tc.want)
		}
	}
}

// TestRegexpForTest covers the package's test-only compiled-pattern helper.
func TestRegexpForTest(t *testing.T) {
	compiled, err := regexpForTest(`^a+$`)
	if err != nil {
		t.Fatalf("regexpForTest = %v, want success", err)
	}
	if !compiled.MatchString("aaa") {
		t.Fatal("compiled pattern did not match")
	}
	if _, err := regexpForTest(`([a-z`); err == nil {
		t.Fatal("regexpForTest accepted an invalid pattern")
	}
}
