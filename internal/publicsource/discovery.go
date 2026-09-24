package publicsource

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// GitHub Gist based discovery.
//
// This implements the public "gist hunting" method: search public gists for
// proxy protocol links, sorted by most recently updated, then verify each hit by
// fetching its raw content and extracting node URIs.
//
// Scope boundary: this module extracts only proxy node URIs. It never extracts
// or reports credentials, API keys or other secrets, even though the same search
// technique could surface them.

const (
	defaultGistSearchBase   = "https://gist.github.com"
	defaultGistRawBase      = "https://gist.githubusercontent.com"
	gistSearchPageLimit     = 4 << 20
	gistRawLimit            = 4 << 20
	maxDiscoveryOutputBytes = 12 << 20
	maxNodeURILength        = 4096
	defaultGistWorkers      = 4
	defaultGistPoliteness   = 50 * time.Millisecond
	defaultGistRequestLimit = 10 * time.Second
	defaultGistTotalBudget  = 25 * time.Second
	minGistCacheTTL         = 5 * time.Minute
	maxGistCacheTTL         = 6 * time.Hour
)

// DefaultGistQueries are the protocol links worth searching for. Plain
// http:// and https:// are intentionally absent: they match almost every gist
// and would drown the signal.
func DefaultGistQueries() []string {
	return []string{"vmess://", "vless://", "trojan://", "ss://", "hysteria2://"}
}

// gistSearchResultRe extracts "/owner/<hex gist id>" links from a search page.
var gistSearchResultRe = regexp.MustCompile(`href="(/[A-Za-z0-9][A-Za-z0-9_.-]*/[0-9a-f]{20,})`)

// nodeURIRe matches proxy node URIs. Longer schemes come first so that e.g.
// "ssr://" is not truncated into "ss://".
var nodeURIRe = regexp.MustCompile("(?i)(?:vmess|vless|trojan|hysteria2|hysteria|hy2|tuic|anytls|socks5h|socks5|socks|ssr|ss)://[^\\s\"'<>\\\\`]{6,}")

// GistDiscoveryConfig configures GistDiscovery. Zero values fall back to
// defaults, so only Queries usually needs to be set.
type GistDiscoveryConfig struct {
	// Queries are the search terms, typically protocol prefixes.
	Queries []string
	// MaxPages caps how many search result pages per query are read.
	MaxPages int
	// MaxGists caps how many gist contents are downloaded per discovery run.
	MaxGists int
	// MaxNodes caps how many node URIs are emitted.
	MaxNodes int
	// RequestTimeout caps one HTTP request.
	RequestTimeout time.Duration
	// TotalBudget caps one whole discovery run.
	TotalBudget time.Duration
	// Workers bounds concurrent gist downloads.
	Workers int
	// Politeness is the delay inserted between gist downloads.
	Politeness time.Duration
	// CacheTTL is how long a discovery result is reused. Defaults to 15m.
	CacheTTL time.Duration
	// Client is an optional HTTP client.
	Client *http.Client
	// SearchBase and RawBase override endpoints (used by tests).
	SearchBase string
	RawBase    string
	// Now returns the current time (used by tests).
	Now func() time.Time
}

// GistDiscovery discovers proxy nodes posted in public GitHub gists.
type GistDiscovery struct {
	cfg GistDiscoveryConfig

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
	cachedN  int
}

// NewGistDiscovery creates a discovery with normalized configuration.
func NewGistDiscovery(cfg GistDiscoveryConfig) *GistDiscovery {
	if len(cfg.Queries) == 0 {
		cfg.Queries = DefaultGistQueries()
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 2
	}
	if cfg.MaxGists <= 0 {
		cfg.MaxGists = 40
	}
	if cfg.MaxNodes <= 0 {
		cfg.MaxNodes = 2000
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultGistRequestLimit
	}
	if cfg.TotalBudget <= 0 {
		cfg.TotalBudget = defaultGistTotalBudget
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultGistWorkers
	}
	if cfg.Politeness < 0 {
		cfg.Politeness = 0
	} else if cfg.Politeness == 0 {
		cfg.Politeness = defaultGistPoliteness
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 15 * time.Minute
	}
	if cfg.CacheTTL < minGistCacheTTL {
		cfg.CacheTTL = minGistCacheTTL
	}
	if cfg.CacheTTL > maxGistCacheTTL {
		cfg.CacheTTL = maxGistCacheTTL
	}
	if strings.TrimSpace(cfg.SearchBase) == "" {
		cfg.SearchBase = defaultGistSearchBase
	} else {
		cfg.SearchBase = strings.TrimRight(strings.TrimSpace(cfg.SearchBase), "/")
	}
	if strings.TrimSpace(cfg.RawBase) == "" {
		cfg.RawBase = defaultGistRawBase
	} else {
		cfg.RawBase = strings.TrimRight(strings.TrimSpace(cfg.RawBase), "/")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.RequestTimeout}
	}
	cfg.Queries = append([]string(nil), cfg.Queries...)
	return &GistDiscovery{cfg: cfg}
}

// Discover returns newline-separated proxy node URIs gathered from public
// gists. The result is cached for CacheTTL. An error is returned only when no
// gist could be inspected at all, so a partially failed run still contributes
// whatever it found.
func (d *GistDiscovery) Discover(ctx context.Context) (string, error) {
	if d == nil {
		return "", errors.New("publicsource: nil gist discovery")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if cached, ok := d.cachedResult(); ok {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, d.cfg.TotalBudget)
	defer cancel()

	candidates, searched, searchFailed := d.collectCandidates(ctx)
	if searched == 0 {
		return "", fmt.Errorf("publicsource: gist search failed for every query (%d failed)", searchFailed)
	}
	if len(candidates) == 0 {
		// An empty search result is still a real answer from GitHub, so cache it.
		// Otherwise every sync cycle would re-search before the cache expires.
		d.storeCache("", 0)
		return "", nil
	}
	if len(candidates) > d.cfg.MaxGists {
		candidates = candidates[:d.cfg.MaxGists]
	}

	nodes, inspected := d.collectNodes(ctx, candidates)
	if inspected == 0 && len(nodes) == 0 {
		return "", fmt.Errorf("publicsource: no gist content could be fetched (%d candidates)", len(candidates))
	}

	out := d.renderNodes(nodes)
	d.storeCache(out, len(nodes))
	return out, nil
}

// collectCandidates returns de-duplicated gist paths, interleaved round-robin so
// that no single query can consume the whole budget.
func (d *GistDiscovery) collectCandidates(ctx context.Context) (paths []string, searched int, failed int) {
	perQuery := make([][]string, 0, len(d.cfg.Queries))
	seen := make(map[string]struct{})

	for _, query := range d.cfg.Queries {
		if ctx.Err() != nil {
			break
		}
		found, err := d.searchQuery(ctx, query)
		if err != nil {
			failed++
			continue
		}
		searched++
		unique := make([]string, 0, len(found))
		for _, path := range found {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			unique = append(unique, path)
		}
		perQuery = append(perQuery, unique)
	}

	for i := 0; ; i++ {
		progressed := false
		for _, list := range perQuery {
			if i < len(list) {
				paths = append(paths, list[i])
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return paths, searched, failed
}

func (d *GistDiscovery) searchQuery(ctx context.Context, query string) ([]string, error) {
	var paths []string
	for page := 1; page <= d.cfg.MaxPages; page++ {
		if ctx.Err() != nil {
			break
		}
		target := fmt.Sprintf("%s/search?q=%s&s=updated&page=%d",
			d.cfg.SearchBase, url.QueryEscape(query), page)
		body, err := d.fetch(ctx, target, gistSearchPageLimit)
		if err != nil {
			if len(paths) > 0 {
				break
			}
			return nil, err
		}
		matches := gistSearchResultRe.FindAllStringSubmatch(string(body), -1)
		before := len(paths)
		for _, m := range matches {
			paths = append(paths, m[1])
		}
		if len(paths) == before {
			break
		}
	}
	return paths, nil
}

// collectNodes downloads gist contents with bounded concurrency and extracts
// node URIs from each.
func (d *GistDiscovery) collectNodes(ctx context.Context, paths []string) (nodes []string, inspected int) {
	type result struct {
		index int
		nodes []string
		ok    bool
	}

	jobs := make(chan int)
	results := make(chan result, len(paths))

	var wg sync.WaitGroup
	var inspectedMu sync.Mutex

	for w := 0; w < d.cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					results <- result{index: idx}
					continue
				}
				target := d.cfg.RawBase + paths[idx] + "/raw"
				body, err := d.fetch(ctx, target, gistRawLimit)
				if err != nil {
					results <- result{index: idx}
					continue
				}
				inspectedMu.Lock()
				inspected++
				inspectedMu.Unlock()
				results <- result{index: idx, nodes: extractNodeURIs(body), ok: true}
				if d.cfg.Politeness > 0 {
					time.Sleep(d.cfg.Politeness)
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i := range paths {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	collected := make([][]string, len(paths))
	for res := range results {
		if res.ok {
			collected[res.index] = res.nodes
		}
	}

	// Preserve candidate order for deterministic output.
	seen := make(map[string]struct{})
	for _, list := range collected {
		for _, node := range list {
			if d.cfg.MaxNodes > 0 && len(nodes) >= d.cfg.MaxNodes {
				return nodes, inspected
			}
			if _, ok := seen[node]; ok {
				continue
			}
			seen[node] = struct{}{}
			nodes = append(nodes, node)
		}
	}
	return nodes, inspected
}

func (d *GistDiscovery) fetch(ctx context.Context, target string, limit int64) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Prism/PublicSourceGistDiscovery")
	req.Header.Set("Accept", "text/html,text/plain,*/*")

	resp, err := d.cfg.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Only the status is reported; the body may contain unrelated content.
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return body, nil
}

func (d *GistDiscovery) renderNodes(nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}
	sort.Strings(nodes)

	var sb strings.Builder
	for _, node := range nodes {
		if sb.Len()+len(node)+1 > maxDiscoveryOutputBytes {
			break
		}
		sb.WriteString(node)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (d *GistDiscovery) cachedResult() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cachedAt.IsZero() {
		return "", false
	}
	if d.cfg.Now().Sub(d.cachedAt) > d.cfg.CacheTTL {
		return "", false
	}
	return d.cached, true
}

func (d *GistDiscovery) storeCache(content string, nodes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cached = content
	d.cachedAt = d.cfg.Now()
	d.cachedN = nodes
}

// CachedNodes reports the node count of the last cached discovery.
func (d *GistDiscovery) CachedNodes() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cachedN
}

// extractNodeURIs pulls proxy node URIs out of gist content. Content that is a
// base64-wrapped subscription is decoded first, because that is a common way to
// post node lists.
func extractNodeURIs(body []byte) []string {
	seen := make(map[string]struct{})
	var nodes []string

	add := func(text string) {
		for _, match := range nodeURIRe.FindAllString(text, -1) {
			trimmed := trimNodeURI(match)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			nodes = append(nodes, trimmed)
		}
	}

	text := string(body)
	add(text)

	if len(nodes) == 0 {
		if decoded, ok := decodeBase64Subscription(text); ok {
			add(decoded)
		}
	}
	return nodes
}

// trimNodeURI removes trailing punctuation that commonly follows a link in
// prose or JSON, and rejects implausibly long matches.
func trimNodeURI(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:!?)]}\u3002\uff0c\uff1b\uff1a")
	if len(trimmed) > maxNodeURILength {
		return ""
	}
	// Drop a match that lost its payload (for example "ss://" alone).
	if len(trimmed) <= len("ss://") {
		return ""
	}
	return trimmed
}

// decodeBase64Subscription decodes a whole-content base64 subscription blob.
func decodeBase64Subscription(text string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, text)
	if len(compact) < 16 {
		return "", false
	}
	for _, decoder := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := decoder.DecodeString(compact)
		if err != nil || len(decoded) == 0 {
			continue
		}
		if !isMostlyText(decoded) {
			continue
		}
		return string(decoded), true
	}
	return "", false
}

func isMostlyText(data []byte) bool {
	printable := 0
	for _, b := range data {
		if b == '\n' || b == '\r' || b == '\t' || (b >= 0x20 && b < 0x7f) {
			printable++
		}
	}
	return printable*100/len(data) >= 85
}
