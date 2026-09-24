package checks

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

//go:embed builtin/*.yaml
var builtinFiles embed.FS

// builtinDir is the directory holding the shipped rules.
const builtinDir = "builtin"

// BrowserUserAgent is the shared User-Agent of the built-in rules.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Prism/1.0"

// DefaultHotReload is how often the user rule directory is re-read.
const DefaultHotReload = 60 * time.Second

// DefaultConcurrencyPerCheck bounds the global concurrency of one rule (§5.3).
const DefaultConcurrencyPerCheck = 2

// userRuleDirName is the directory below $PRISM_STATE_DIR holding user rules.
const userRuleDirName = "checks.d"

// UserRuleDir returns the configured user rule directory.
func UserRuleDir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return stateDir + string(os.PathSeparator) + userRuleDirName
}

// SourceRecordsEnabled reports the per-check enabled override stored in
// intel_provider_settings under provider_id "check:<id>" (§5.4).
type SettingID func(checkID string) string

// CheckSettingID is the provider_id used for a check toggle.
func CheckSettingID(checkID string) string { return "check:" + checkID }

// EnabledSource resolves the persisted enabled flag of one check.
type EnabledSource interface {
	CheckEnabled(checkID string) (enabled bool, found bool)
}

// Options configures the engine.
type Options struct {
	// UserDir holds user rules overriding the built-ins.
	UserDir string
	// BuiltinFS overrides the embedded rule set (tests).
	BuiltinFS fs.FS
	// EnabledSource overrides the built-in "enabled" flag per check.
	EnabledSource EnabledSource
	// ConcurrencyPerCheck bounds one rule globally (default 2).
	ConcurrencyPerCheck int
	// HotReload is the minimum interval between user rule reloads.
	HotReload time.Duration
	// Now is the injected clock.
	Now func() time.Time
	// Logf receives load warnings.
	Logf func(format string, args ...any)
}

// Engine holds the loaded rules and runs them.
type Engine struct {
	mu        sync.RWMutex
	rules     map[string]*Rule
	order     []string
	loadErrs  []error
	lastLoad  time.Time
	builtinFS fs.FS

	userDir      string
	enabled      EnabledSource
	hotReload    time.Duration
	now          func() time.Time
	logf         func(string, ...any)
	concurrency  int
	semaphores   map[string]chan struct{}
	semaphoreMu  sync.Mutex
	nodeStripes  [64]sync.Mutex
	clientCreate func(adapter.Outbound) *http.Client
}

// NewEngine loads the built-in rules and the user rules below stateDir.
func NewEngine(opts Options) *Engine {
	builtinFS := opts.BuiltinFS
	if builtinFS == nil {
		builtinFS = builtinFiles
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	hotReload := opts.HotReload
	if hotReload <= 0 {
		hotReload = DefaultHotReload
	}
	concurrency := opts.ConcurrencyPerCheck
	if concurrency <= 0 {
		concurrency = DefaultConcurrencyPerCheck
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	engine := &Engine{
		rules:       make(map[string]*Rule),
		builtinFS:   builtinFS,
		userDir:     opts.UserDir,
		enabled:     opts.EnabledSource,
		hotReload:   hotReload,
		now:         now,
		logf:        logf,
		concurrency: concurrency,
		semaphores:  make(map[string]chan struct{}),
	}
	engine.clientCreate = func(ob adapter.Outbound) *http.Client { return nodeClient(ob, 0) }
	if err := engine.Reload(); err != nil {
		logf("[checks] initial rule load: %v", err)
	}
	return engine
}

// Reload re-reads the built-in and user rules. A user rule with the same id as a
// built-in replaces it (§5.1).
func (e *Engine) Reload() error {
	builtinRules, builtinErrs := LoadFS(e.builtinFS, builtinDir, "builtin")
	userRules, userErrs := LoadDir(e.userDir, "user")

	merged := make(map[string]*Rule, len(builtinRules)+len(userRules))
	order := make([]string, 0, len(builtinRules)+len(userRules))
	for _, rule := range builtinRules {
		merged[rule.ID] = rule
		order = append(order, rule.ID)
	}
	origin := make(map[string]*Rule, len(userRules))
	for _, rule := range userRules {
		if _, exists := merged[rule.ID]; !exists {
			order = append(order, rule.ID)
		}
		merged[rule.ID] = rule
		origin[rule.ID] = rule
	}
	sort.Strings(order)

	errs := append(append([]error{}, builtinErrs...), userErrs...)
	e.mu.Lock()
	e.rules = merged
	e.order = order
	e.loadErrs = errs
	e.lastLoad = e.now().UTC()
	e.mu.Unlock()
	for _, err := range errs {
		e.logf("[checks] %v", err)
	}
	return nil
}

// reloadIfStale refreshes the rules at most once per hot-reload interval.
func (e *Engine) reloadIfStale() {
	e.mu.RLock()
	stale := e.now().UTC().Sub(e.lastLoad) >= e.hotReload
	e.mu.RUnlock()
	if stale {
		_ = e.Reload()
	}
}

// LoadErrors returns the warnings of the last load.
func (e *Engine) LoadErrors() []error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]error, len(e.loadErrs))
	copy(out, e.loadErrs)
	return out
}

// RuleInfo is the metadata of one loaded rule for GET /api/v1/intel/checks.
type RuleInfo struct {
	ID       string `json:"id"`
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
	Source   string `json:"source"`
	// Path is the file a user rule was loaded from; it is empty for a built-in
	// rule and for a rule that failed to load (see LoadErrors).
	Path       string   `json:"path,omitempty"`
	Steps      []string `json:"steps"`
	TTL        string   `json:"ttl"`
	Timeout    string   `json:"timeout"`
	Calibrated string   `json:"calibrated"`
}

// Rule returns one loaded rule.
func (e *Engine) Rule(id string) (*Rule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rule, ok := e.rules[id]
	return rule, ok
}

// Rules lists every loaded rule with its effective enabled state.
func (e *Engine) Rules() []RuleInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]RuleInfo, 0, len(e.order))
	for _, id := range e.order {
		rule := e.rules[id]
		steps := make([]string, 0, len(rule.Steps))
		for _, step := range rule.Steps {
			steps = append(steps, step.ID)
		}
		// Only a user rule reports its file: a built-in rule is identified by
		// its id and its calibration date (§5.4).
		path := ""
		if rule.Source == "user" {
			path = rule.Path
		}
		out = append(out, RuleInfo{
			ID: rule.ID, Version: rule.Version, Name: rule.Name, Category: rule.Category,
			Enabled: e.enabledRule(rule), Source: rule.Source, Path: path, Steps: steps,
			TTL: rule.TTLOr().String(), Timeout: rule.TimeoutOr().String(),
			Calibrated: rule.Calibrated,
		})
	}
	return out
}

// enabledRule resolves the effective enabled flag of one rule.
func (e *Engine) enabledRule(rule *Rule) bool {
	if e.enabled != nil {
		if enabled, found := e.enabled.CheckEnabled(rule.ID); found {
			return enabled
		}
	}
	return rule.Enabled
}

// RunRequest selects what to execute for one node.
type RunRequest struct {
	// NodeKey serialises the checks of one node (only one check at a time).
	NodeKey string
	// Outbound is the node connection; nil makes every rule fail cleanly.
	Outbound adapter.Outbound
	// Egress is the node's own egress address, recorded with the result.
	Egress netip.Addr
	// RuleIDs selects the rules; empty selects every enabled rule.
	RuleIDs []string
}

// Result is the outcome of one check.
type Result struct {
	CheckID   string         `json:"check_id"`
	Version   int            `json:"version"`
	Outcome   string         `json:"outcome"`
	Region    string         `json:"region"`
	EgressIP  string         `json:"egress_ip"`
	LatencyMs int            `json:"latency_ms"`
	TTL       time.Duration  `json:"-"`
	Detail    map[string]any `json:"detail"`
	ErrorCode string         `json:"error_code,omitempty"`
}

// Run executes the selected rules for one node.
func (e *Engine) Run(ctx context.Context, req RunRequest) []Result {
	e.reloadIfStale()

	selected := e.selectRules(req.RuleIDs)
	if len(selected) == 0 {
		return nil
	}
	if req.NodeKey != "" {
		// §5.3: one check per node at a time.
		stripe := &e.nodeStripes[stripeIndex(req.NodeKey)]
		stripe.Lock()
		defer stripe.Unlock()
	}

	results := make([]Result, 0, len(selected))
	for _, rule := range selected {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{
				CheckID: rule.ID, Version: rule.Version, Outcome: OutcomeError,
				Detail: map[string]any{}, ErrorCode: "CHECK_CANCELED",
			})
			continue
		}
		release, ok := e.acquire(ctx, rule.ID)
		if !ok {
			results = append(results, Result{
				CheckID: rule.ID, Version: rule.Version, Outcome: OutcomeError,
				Detail: map[string]any{}, ErrorCode: "CHECK_BUSY",
			})
			continue
		}
		result := e.runRule(ctx, rule, req)
		release()
		results = append(results, result)
	}
	return results
}

// selectRules resolves a selection to enabled rules in stable order.
func (e *Engine) selectRules(ids []string) []*Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*Rule
	if len(ids) == 0 {
		for _, id := range e.order {
			if rule := e.rules[id]; rule != nil && e.enabledRule(rule) {
				out = append(out, rule)
			}
		}
		return out
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	for _, id := range e.order {
		if _, ok := wanted[id]; !ok {
			continue
		}
		if rule := e.rules[id]; rule != nil && e.enabledRule(rule) {
			out = append(out, rule)
		}
	}
	return out
}

// acquire takes the per-rule concurrency slot.
func (e *Engine) acquire(ctx context.Context, ruleID string) (func(), bool) {
	e.semaphoreMu.Lock()
	sem := e.semaphores[ruleID]
	if sem == nil {
		sem = make(chan struct{}, e.concurrency)
		e.semaphores[ruleID] = sem
	}
	e.semaphoreMu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	case <-ctx.Done():
		return nil, false
	}
}

// SetConcurrencyPerCheck updates the global per-rule concurrency bound. The
// existing semaphores are dropped so the change applies to the next run
// (WP09 §5.3, intel_check_concurrency_per_check is hot-reloadable).
func (e *Engine) SetConcurrencyPerCheck(n int) {
	if e == nil {
		return
	}
	if n <= 0 {
		n = DefaultConcurrencyPerCheck
	}
	e.semaphoreMu.Lock()
	e.concurrency = n
	e.semaphores = make(map[string]chan struct{})
	e.semaphoreMu.Unlock()
}

// stripeIndex maps a node key onto a bounded stripe array.
func stripeIndex(key string) uint32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	return hasher.Sum32() % 64
}

// stepResult is what one executed step produced.
type stepResult struct {
	ID          string
	Status      int
	Header      http.Header
	Body        string
	Connected   bool
	ErrKind     string
	Duration    time.Duration
	BodyBytes   int
	Truncated   bool
	Unavailable string
}

// runRule executes every step of one rule and evaluates its outcomes.
func (e *Engine) runRule(ctx context.Context, rule *Rule, req RunRequest) Result {
	result := Result{
		CheckID: rule.ID, Version: rule.Version, Outcome: OutcomeUnknown,
		TTL: rule.TTLOr(), Detail: map[string]any{},
	}
	if req.Egress.IsValid() {
		result.EgressIP = req.Egress.Unmap().String()
	}
	if req.Outbound == nil {
		result.Outcome = OutcomeError
		result.ErrorCode = "CHECK_NO_NODE"
		return result
	}

	start := e.now()
	timeout := rule.TimeoutOr()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	steps := make(map[string]stepResult, len(rule.Steps))
	detail := make(map[string]any, len(rule.Steps))
	failed := ""
	for _, step := range rule.Steps {
		stepCtx, stepCancel := context.WithTimeout(runCtx, timeout)
		outcome := e.executeStep(stepCtx, step, req.Outbound)
		stepCancel()
		steps[step.ID] = outcome
		detail[step.ID] = stepDetail(outcome)
		if outcome.Unavailable != "" && failed == "" {
			failed = outcome.Unavailable
		}
	}
	result.Detail = detail
	result.LatencyMs = int(time.Since(start).Milliseconds())

	result.Outcome = rule.Default
	for _, candidate := range rule.Outcomes {
		if matches(candidate, steps) {
			result.Outcome = candidate.Outcome
			break
		}
	}
	if result.Outcome == OutcomeError && failed != "" {
		result.ErrorCode = failed
	}
	if rule.Region != nil {
		result.Region = extractRegion(rule.Region, steps)
	}
	return result
}

// StepDetail is the bounded per-step summary stored in node_checks.detail_json.
type stepDetailValue struct {
	Status      int    `json:"status,omitempty"`
	Connected   bool   `json:"connected,omitempty"`
	Error       string `json:"error,omitempty"`
	Ms          int    `json:"ms,omitempty"`
	BodyBytes   int    `json:"body_bytes,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Location    string `json:"location,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

func stepDetail(outcome stepResult) stepDetailValue {
	detail := stepDetailValue{
		Status: outcome.Status, Connected: outcome.Connected, Error: outcome.ErrKind,
		Ms: int(outcome.Duration.Milliseconds()), BodyBytes: outcome.BodyBytes,
		Truncated: outcome.Truncated, Unavailable: outcome.Unavailable,
	}
	if outcome.Header != nil {
		detail.Location = outcome.Header.Get("Location")
	}
	return detail
}

// executeStep performs one HTTP or TCP step through the node.
func (e *Engine) executeStep(ctx context.Context, step Step, ob adapter.Outbound) stepResult {
	switch {
	case step.TCP != nil:
		return e.executeTCP(ctx, step, ob)
	case step.Request != nil:
		return e.executeRequest(ctx, step, ob)
	default:
		return stepResult{ID: step.ID, Unavailable: "CHECK_RULE_INVALID"}
	}
}

func (e *Engine) executeTCP(ctx context.Context, step Step, ob adapter.Outbound) stepResult {
	outcome := stepResult{ID: step.ID}
	address := net.JoinHostPort(step.TCP.Host, itoa(step.TCP.Port))
	start := e.now()
	conn, err := ob.DialContext(ctx, "tcp", M.ParseSocksaddr(address))
	outcome.Duration = time.Since(start)
	if err != nil {
		outcome.Connected = false
		outcome.ErrKind = classifyDialError(ctx, err)
		return outcome
	}
	outcome.Connected = true
	_ = conn.Close()
	return outcome
}

func (e *Engine) executeRequest(ctx context.Context, step Step, ob adapter.Outbound) stepResult {
	outcome := stepResult{ID: step.ID}
	request := step.Request
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if request.Body != "" {
		body = strings.NewReader(request.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, request.URL, body)
	if err != nil {
		outcome.Unavailable = "CHECK_REQUEST_INVALID"
		return outcome
	}
	req.Header.Set("User-Agent", BrowserUserAgent)
	for name, value := range request.Headers {
		req.Header.Set(name, value)
	}
	client := nodeClient(ob, 0)
	if request.FollowRedirects {
		client.CheckRedirect = nil
	}
	start := e.now()
	resp, err := client.Do(req)
	outcome.Duration = time.Since(start)
	if err != nil {
		outcome.ErrKind = classifyDialError(ctx, err)
		if outcome.ErrKind == "" {
			outcome.Unavailable = "CHECK_REQUEST_FAILED"
		}
		return outcome
	}
	defer resp.Body.Close()

	limit := request.MaxBodyBytes
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	maxBytes := int64(limit)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		outcome.Unavailable = "CHECK_BODY_UNREADABLE"
		return outcome
	}
	if int64(len(raw)) > maxBytes {
		raw = raw[:maxBytes]
		outcome.Truncated = true
	}
	outcome.Status = resp.StatusCode
	outcome.Header = resp.Header.Clone()
	outcome.Body = string(raw)
	outcome.BodyBytes = len(raw)
	if outcome.ErrKind == "" && outcome.Unavailable == "" {
		outcome.Connected = true
	}
	return outcome
}

// nodeClient builds the HTTP client that always dials through the node.
func nodeClient(ob adapter.Outbound, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// classifyDialError maps a transport error onto the rule vocabulary.
func classifyDialError(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(text, "timeout") || strings.Contains(text, "i/o timeout"):
		return "timeout"
	case strings.Contains(text, "connection refused") || strings.Contains(text, "refused"):
		return "refused"
	case strings.Contains(text, "tls") || strings.Contains(text, "certificate") || strings.Contains(text, "handshake"):
		return "tls"
	default:
		return "any"
	}
}

// matches evaluates one outcome condition against the executed steps.
func matches(candidate OutcomeCase, steps map[string]stepResult) bool {
	when := candidate.When
	if when.Step == "" {
		// A condition without a step only makes sense for the tcp "connected"
		// and "error" checks; evaluate them against every step.
		for _, step := range steps {
			if matchesStep(when, step, candidate) {
				return true
			}
		}
		return len(steps) == 0 && false
	}
	step, ok := steps[when.Step]
	if !ok {
		return false
	}
	return matchesStep(when, step, candidate)
}

func matchesStep(when When, step stepResult, candidate OutcomeCase) bool {
	if len(when.StatusIn) > 0 {
		if !containsInt(when.StatusIn, step.Status) {
			return false
		}
	}
	if len(when.StatusNotIn) > 0 {
		if containsInt(when.StatusNotIn, step.Status) {
			return false
		}
	}
	for name, needle := range when.HeaderContains {
		value := step.Header.Get(name)
		if value == "" || !strings.Contains(strings.ToLower(value), strings.ToLower(needle)) {
			return false
		}
	}
	for name, expr := range when.HeaderRegex {
		re, err := regexp.Compile(expr)
		if err != nil {
			return false
		}
		if !re.MatchString(step.Header.Get(name)) {
			return false
		}
	}
	if when.BodyContains != "" && !strings.Contains(step.Body, when.BodyContains) {
		return false
	}
	if when.BodyNotContain != "" && strings.Contains(step.Body, when.BodyNotContain) {
		return false
	}
	if candidate.bodyRegex != nil && !candidate.bodyRegex.MatchString(step.Body) {
		return false
	}
	if when.RedirectHost != "" {
		location := step.Header.Get("Location")
		if location == "" {
			return false
		}
		if !redirectHostMatches(location, when.RedirectHost) {
			return false
		}
	}
	if when.Connected != nil && step.Connected != *when.Connected {
		return false
	}
	if when.Error != "" {
		if when.Error == "any" {
			if step.ErrKind == "" && step.Unavailable == "" {
				return false
			}
		} else if step.ErrKind != when.Error {
			return false
		}
	}
	return true
}

// redirectHostMatches compares the host of a Location header with a needle.
func redirectHostMatches(location, needle string) bool {
	host := location
	if index := strings.Index(host, "://"); index >= 0 {
		host = host[index+3:]
	}
	if index := strings.IndexAny(host, "/?#"); index >= 0 {
		host = host[:index]
	}
	if index := strings.Index(host, ":"); index >= 0 {
		host = host[:index]
	}
	return strings.EqualFold(host, needle)
}

// extractRegion pulls a two-letter region code out of one step body.
func extractRegion(spec *RegionSpec, steps map[string]stepResult) string {
	if spec == nil || spec.compiled == nil {
		return ""
	}
	step, ok := steps[spec.Step]
	if !ok {
		for _, candidate := range steps {
			step = candidate
			break
		}
	}
	match := spec.compiled.FindStringSubmatch(step.Body)
	if len(match) < 2 {
		return ""
	}
	region := strings.ToUpper(strings.TrimSpace(match[1]))
	if len(region) != 2 {
		return ""
	}
	for _, r := range region {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	return region
}

func containsInt(values []int, value int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// regexpForTest exposes a compiled pattern for tests.
func regexpForTest(pattern string) (*regexp.Regexp, error) { return regexp.Compile(pattern) }

var _ = fmt.Sprintf
