// Package publicsource collects proxy nodes from public subscription sources.
package publicsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"prism/internal/addrpolicy"
	"prism/internal/netutil"
	"prism/internal/node"
)

const (
	defaultSourceTimeout  = 30 * time.Second
	maxSourceBodyBytes    = 16 << 20
	defaultMaxSourceStale = time.Hour
	maxSourceStaleCeiling = 24 * time.Hour
)

var (
	// ErrEmptyResult means no node could be produced from any source, even
	// after falling back to per-source caches. The previous snapshot is kept.
	ErrEmptyResult = errors.New("publicsource: empty result")
	ErrNoSources   = errors.New("publicsource: no sources configured")
)

// Config controls a Collector.
type Config struct {
	Enabled  bool
	Interval time.Duration
	Sources  []string
	MaxNodes int
	// SourceTimeout caps a single source fetch+parse. Defaults to 30s.
	SourceTimeout time.Duration
	// MaxContentBytes caps the generated subscription payload. 0 means no cap.
	// Keep it below the target instance's PRISM_API_MAX_BODY_BYTES (default
	// 1 MiB).
	MaxContentBytes int
	// OnAttempt is invoked after every refresh attempt, including failures.
	// It is the observability hook for callers such as standalone sync tools.
	OnAttempt func(Attempt)
	// ExtraContent, when set, is invoked once per refresh and its returned text
	// is parsed as one additional in-memory source. Discovery layers (such as
	// GitHub Gist search) use it to contribute node lines without pretending to
	// be an HTTP URL.
	ExtraContent func(ctx context.Context) (string, error)
	// ExtraContentName labels the ExtraContent source in Results. Defaults to
	// "discovery".
	ExtraContentName string
}

// Snapshot is the last successfully published aggregate.
type Snapshot struct {
	Content        string
	SourceCount    int
	CandidateCount int
	UniqueNodes    int
	Results        []SourceResult
	UpdatedAt      time.Time
	// Degraded reports that at least one source failed or only had stale
	// cached nodes while the snapshot was still published.
	Degraded bool
}

// SourceResult describes one source fetch and parse attempt.
type SourceResult struct {
	URL            string
	CandidateCount int
	AcceptedCount  int
	Error          string
	// UsedCache reports that this source failed and cached nodes were reused.
	UsedCache bool
}

// Attempt describes the outcome of one refresh attempt.
type Attempt struct {
	Results        []SourceResult
	CandidateCount int
	UniqueNodes    int
	ContentBytes   int
	Published      bool
	Err            error
	At             time.Time
}

// cachedNodes holds one source's most recent accepted nodes. The set is bounded
// exactly like the live accumulator: a source can never contribute more than
// MaxNodes entries to the published prefix, so keeping its own smallest hashes
// reproduces the cache fallback without storing the whole list.
type cachedNodes struct {
	nodes *topNodes
	at    time.Time
}

// Collector periodically downloads, validates, de-duplicates, and publishes
// proxy nodes from configured public sources.
type Collector struct {
	cfg        Config
	downloader netutil.Downloader
	onSnapshot func(Snapshot) error

	statusMu sync.RWMutex
	status   Snapshot
	attempt  Attempt

	cacheMu sync.Mutex
	cache   map[string]*cachedNodes

	lifecycleMu  sync.Mutex
	running      bool
	stopCh       chan struct{}
	cancel       context.CancelFunc
	lifecycleCtx context.Context
	stopWg       sync.WaitGroup

	refreshMu sync.Mutex
}

func NewCollector(cfg Config, downloader netutil.Downloader, onSnapshot func(Snapshot) error) *Collector {
	cfg.Sources = append([]string(nil), cfg.Sources...)
	if cfg.SourceTimeout <= 0 {
		cfg.SourceTimeout = defaultSourceTimeout
	}
	return &Collector{
		cfg:        cfg,
		downloader: downloader,
		onSnapshot: onSnapshot,
		cache:      make(map[string]*cachedNodes),
	}
}

func (c *Collector) Start() {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.running || !c.cfg.Enabled || (len(c.cfg.Sources) == 0 && c.cfg.ExtraContent == nil) {
		return
	}
	stopCh := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	c.stopCh = stopCh
	c.cancel = cancel
	c.lifecycleCtx = ctx
	c.running = true
	c.stopWg.Add(1)
	go c.run(stopCh, ctx)
}

func (c *Collector) Stop() {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	if !c.running {
		c.lifecycleMu.Unlock()
		return
	}
	c.running = false
	cancel := c.cancel
	stopCh := c.stopCh
	c.lifecycleMu.Unlock()

	// Cancel and close outside the lifecycle lock so an in-flight refresh
	// (which may be inside the snapshot callback) can never deadlock Stop.
	if cancel != nil {
		cancel()
	}
	if stopCh != nil {
		close(stopCh)
	}
	c.stopWg.Wait()

	c.lifecycleMu.Lock()
	c.cancel = nil
	c.stopCh = nil
	c.lifecycleCtx = nil
	c.lifecycleMu.Unlock()
}

func (c *Collector) run(stopCh <-chan struct{}, ctx context.Context) {
	defer c.stopWg.Done()
	_, _ = c.refresh(ctx)
	interval := c.cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			_, _ = c.refresh(ctx)
		}
	}
}

// RefreshNow runs one synchronous refresh using the collector lifecycle context
// when running, or a background context otherwise.
func (c *Collector) RefreshNow() (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("publicsource: nil collector")
	}
	c.lifecycleMu.Lock()
	ctx := c.lifecycleCtx
	c.lifecycleMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return c.refresh(ctx)
}

func (c *Collector) sourceTimeout() time.Duration {
	if c.cfg.SourceTimeout > 0 {
		return c.cfg.SourceTimeout
	}
	return defaultSourceTimeout
}

func (c *Collector) maxSourceStale() time.Duration {
	stale := 3 * c.cfg.Interval
	if stale < defaultMaxSourceStale {
		stale = defaultMaxSourceStale
	}
	if stale > maxSourceStaleCeiling {
		stale = maxSourceStaleCeiling
	}
	return stale
}

// totalSourceCount counts the configured URL sources plus the optional dynamic
// content source, so counters describe everything that was attempted.
func (c *Collector) totalSourceCount() int {
	count := len(c.cfg.Sources)
	if c.cfg.ExtraContent != nil {
		count++
	}
	return count
}

func (c *Collector) refresh(parent context.Context) (Snapshot, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	if len(c.cfg.Sources) == 0 && c.cfg.ExtraContent == nil {
		err := ErrNoSources
		c.recordAttempt(Attempt{Err: err, At: time.Now()})
		return c.Status(), err
	}

	results := make([]SourceResult, 0, c.totalSourceCount())
	// Only nodes that can reach the published prefix are retained (see
	// bounded.go). This is what keeps peak memory proportional to MaxNodes
	// instead of to everything the sources contain.
	allNodes := newTopNodes(c.cfg.MaxNodes)
	cacheLimit := c.cfg.MaxNodes
	candidateCount := 0
	degraded := false

	for _, source := range c.cfg.Sources {
		result := SourceResult{URL: source}
		if err := parent.Err(); err != nil {
			c.recordAttempt(Attempt{Results: cloneResults(results), Err: err, At: time.Now()})
			return c.Status(), err
		}
		if c.downloader == nil {
			result.Error = "downloader is nil"
			c.fallbackToCache(source, &result, allNodes, &degraded)
			results = append(results, result)
			continue
		}

		ctx, cancel := context.WithTimeout(parent, c.sourceTimeout())
		body, err := c.downloader.Download(ctx, source)
		cancel()
		if err != nil {
			result.Error = err.Error()
			c.fallbackToCache(source, &result, allNodes, &degraded)
			results = append(results, result)
			continue
		}
		if len(body) > maxSourceBodyBytes {
			result.Error = fmt.Sprintf("response exceeds %d bytes", maxSourceBodyBytes)
			c.fallbackToCache(source, &result, allNodes, &degraded)
			results = append(results, result)
			continue
		}

		c.ingestSource(source, body, &result, allNodes, cacheLimit, &candidateCount, &degraded)
		results = append(results, result)
	}

	if c.cfg.ExtraContent != nil {
		name := c.cfg.ExtraContentName
		if name == "" {
			name = "discovery"
		}
		result := SourceResult{URL: name}
		content, contentErr := c.cfg.ExtraContent(parent)
		switch {
		case contentErr != nil:
			result.Error = contentErr.Error()
			c.fallbackToCache(name, &result, allNodes, &degraded)
		case len(content) > maxSourceBodyBytes:
			result.Error = fmt.Sprintf("content exceeds %d bytes", maxSourceBodyBytes)
			c.fallbackToCache(name, &result, allNodes, &degraded)
		default:
			c.ingestSource(name, []byte(content), &result, allNodes, cacheLimit, &candidateCount, &degraded)
		}
		results = append(results, result)
	}

	if allNodes.Len() == 0 {
		err := fmt.Errorf("%w: no valid nodes from %d source(s)", ErrEmptyResult, c.totalSourceCount())
		c.recordAttempt(Attempt{Results: cloneResults(results), CandidateCount: candidateCount, Err: err, At: time.Now()})
		return c.Status(), err
	}
	if err := parent.Err(); err != nil {
		c.recordAttempt(Attempt{Results: cloneResults(results), CandidateCount: candidateCount, Err: err, At: time.Now()})
		return c.Status(), err
	}

	// Sorted() yields the same order the previous implementation produced by
	// sorting every key, but only over the retained set.
	entries := allNodes.Sorted()

	// Build the payload while honouring the caller's byte budget so the target
	// Resin API body limit is never exceeded silently.
	rawOutbounds := make([]json.RawMessage, 0, len(entries))
	usedBytes := len(`{"outbounds":[]}`)
	for _, entry := range entries {
		add := len(entry.raw) + 1
		if c.cfg.MaxContentBytes > 0 && usedBytes+add > c.cfg.MaxContentBytes {
			degraded = true
			break
		}
		usedBytes += add
		rawOutbounds = append(rawOutbounds, entry.raw)
	}
	if len(rawOutbounds) == 0 {
		err := fmt.Errorf("%w: content budget too small for any node", ErrEmptyResult)
		c.recordAttempt(Attempt{Results: cloneResults(results), CandidateCount: candidateCount, Err: err, At: time.Now()})
		return c.Status(), err
	}

	contentBytes, err := json.Marshal(struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}{Outbounds: rawOutbounds})
	if err != nil {
		encodeErr := fmt.Errorf("publicsource: encode snapshot: %w", err)
		c.recordAttempt(Attempt{Results: cloneResults(results), CandidateCount: candidateCount, Err: encodeErr, At: time.Now()})
		return c.Status(), encodeErr
	}

	snapshot := Snapshot{
		Content:        string(contentBytes) + "\n",
		SourceCount:    c.totalSourceCount(),
		CandidateCount: candidateCount,
		UniqueNodes:    len(rawOutbounds),
		Results:        cloneResults(results),
		UpdatedAt:      time.Now(),
		Degraded:       degraded,
	}

	if c.onSnapshot != nil {
		if err := parent.Err(); err != nil {
			c.recordAttempt(Attempt{Results: cloneResults(results), CandidateCount: candidateCount, Err: err, At: time.Now()})
			return c.Status(), err
		}
		if err := c.onSnapshot(cloneSnapshot(snapshot)); err != nil {
			cbErr := fmt.Errorf("publicsource: snapshot callback: %w", err)
			c.recordAttempt(Attempt{
				Results:        cloneResults(results),
				CandidateCount: candidateCount,
				UniqueNodes:    len(rawOutbounds),
				Err:            cbErr,
				At:             time.Now(),
			})
			return snapshot, cbErr
		}
	}

	c.statusMu.Lock()
	c.status = cloneSnapshot(snapshot)
	c.attempt = Attempt{
		Results:        cloneResults(results),
		CandidateCount: candidateCount,
		UniqueNodes:    len(rawOutbounds),
		ContentBytes:   len(snapshot.Content),
		Published:      true,
		At:             time.Now(),
	}
	c.statusMu.Unlock()

	if c.cfg.OnAttempt != nil {
		c.cfg.OnAttempt(c.LastAttempt())
	}
	return snapshot, nil
}

// ingestSource parses body as subscription content and merges the accepted
// nodes into allNodes. Counts and cache fallback are recorded on result.
func (c *Collector) ingestSource(source string, body []byte, result *SourceResult, allNodes *topNodes, cacheLimit int, candidateCount *int, degraded *bool) {
	// parseSourceBody falls back to the format normaliser for the public lists
	// that are plain text, HTML or JSON rather than subscriptions.
	parsed, err := parseSourceBody(body)
	if err != nil {
		result.Error = err.Error()
		c.fallbackToCache(source, result, allNodes, degraded)
		return
	}

	result.CandidateCount = len(parsed)
	*candidateCount += len(parsed)

	// Count every distinct node the source offers so AcceptedCount stays exact,
	// but only retain the bounded view of it. The seen set is transient: it is
	// released as soon as this source is done, so peak memory is one source's
	// hash set plus two bounded node sets.
	sourceNodes := newTopNodes(cacheLimit)
	seen := make(map[string]struct{})
	for _, item := range parsed {
		if !validRawOptions(item.RawOptions) {
			continue
		}
		hash := node.HashFromRawOptions(item.RawOptions).Hex()
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		raw := append(json.RawMessage(nil), item.RawOptions...)
		sourceNodes.Add(hash, raw)
		allNodes.Add(hash, raw)
	}
	result.AcceptedCount = len(seen)
	if len(seen) == 0 {
		result.Error = "no valid supported nodes"
		c.fallbackToCache(source, result, allNodes, degraded)
		return
	}

	c.storeCache(source, sourceNodes)
}

// fallbackToCache reuses this source's last good nodes when the current fetch
// failed and the cache is still fresh, so one broken source cannot remove nodes
// that other sources still provide.
func (c *Collector) fallbackToCache(source string, result *SourceResult, into *topNodes, degraded *bool) {
	*degraded = true
	cached, ok := c.loadCache(source, c.maxSourceStale())
	if !ok {
		return
	}
	result.UsedCache = true
	result.AcceptedCount = cached.Len()
	if result.Error != "" {
		result.Error = "using cached nodes: " + result.Error
	}
	cached.Range(func(hash string, raw json.RawMessage) bool {
		into.Add(hash, raw)
		return true
	})
}

func (c *Collector) storeCache(source string, nodes *topNodes) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cache[source] = &cachedNodes{nodes: nodes, at: time.Now()}
}

func (c *Collector) loadCache(source string, maxAge time.Duration) (*topNodes, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := c.cache[source]
	if !ok || entry.nodes.Len() == 0 {
		return nil, false
	}
	if maxAge > 0 && time.Since(entry.at) > maxAge {
		return nil, false
	}
	return entry.nodes, true
}

func (c *Collector) recordAttempt(attempt Attempt) {
	c.statusMu.Lock()
	c.attempt = attempt
	c.statusMu.Unlock()
	if c.cfg.OnAttempt != nil {
		c.cfg.OnAttempt(cloneAttempt(attempt))
	}
}

// Status returns the most recently published snapshot. A failed refresh never
// overwrites it, so Content and the counters always describe the same aggregate.
func (c *Collector) Status() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return cloneSnapshot(c.status)
}

// LastAttempt returns the outcome of the most recent refresh attempt.
func (c *Collector) LastAttempt() Attempt {
	if c == nil {
		return Attempt{}
	}
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return cloneAttempt(c.attempt)
}

func cloneAttempt(attempt Attempt) Attempt {
	attempt.Results = cloneResults(attempt.Results)
	return attempt
}

func cloneResults(results []SourceResult) []SourceResult {
	if results == nil {
		return nil
	}
	return append([]SourceResult(nil), results...)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Results = cloneResults(snapshot.Results)
	return snapshot
}

// maxNodeTargetDepth bounds the walk in validNodeTargets. Node documents are
// shallow (an outbound, or an envelope with main/deps); the bound exists so a
// hostile document cannot turn validation into unbounded recursion.
const maxNodeTargetDepth = 16

// validRawOptions rejects a node that would connect somewhere other than a
// public address.
//
// It walks the WHOLE document instead of looking only at the top level. A node
// can be a chain envelope ({"prism_node":1,"main":{...},"deps":[{...}]}) or an
// endpoints wrapper, and there the real target lives in main/deps — not at the
// top. Checking only the top level meant "no top-level server" was accepted
// unconditionally, so every envelope-shaped node bypassed this filter entirely
// and could point at 127.0.0.1, a private range or a cloud metadata address.
func validRawOptions(raw []byte) bool {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return false
	}
	return validNodeTargets(document, 0)
}

// validNodeTargets walks one decoded document and rejects the first "server"
// value that is not a public host. Every other key is walked recursively, so a
// target cannot hide behind a different nesting level.
func validNodeTargets(value any, depth int) bool {
	if depth > maxNodeTargetDepth {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, entry := range typed {
			if key == "server" {
				host, ok := entry.(string)
				if !ok || !validHost(host) {
					return false
				}
				continue
			}
			if !validNodeTargets(entry, depth+1) {
				return false
			}
		}
		return true
	case []any:
		for _, entry := range typed {
			if !validNodeTargets(entry, depth+1) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// validHost accepts a public IP literal or a dotted public hostname.
//
// The rules live in internal/addrpolicy, which the outbound admission path also
// uses: the two gates must agree, or a node that the collector accepts could be
// refused (or worse, accepted) by the other for a different reason. See
// addrpolicy.HostIsForbiddenLexically for what each rule rejects and why.
func validHost(host string) bool {
	return !addrpolicy.HostIsForbiddenLexically(host)
}

// isForbiddenIP reports whether host is an IP literal that must never be a node
// target.
func isForbiddenIP(host string) bool {
	addr, err := netip.ParseAddr(addrpolicy.NormalizeHost(host))
	if err != nil {
		return false
	}
	return addrpolicy.AddrIsForbidden(addr)
}

func isForbiddenHostname(host string) bool {
	normalized := addrpolicy.NormalizeHost(host)
	if normalized == "" {
		return true
	}
	// A name is forbidden when the shared policy refuses it and it is not an
	// address literal (that case belongs to isForbiddenIP).
	if _, err := netip.ParseAddr(normalized); err == nil {
		return false
	}
	return addrpolicy.HostIsForbiddenLexically(normalized)
}
