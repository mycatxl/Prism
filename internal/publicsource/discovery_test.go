package publicsource

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractNodeURIsPicksNodesAndIgnoresNoise(t *testing.T) {
	body := []byte(strings.Join([]string{
		"// some unrelated code",
		`const url = "https://example.com/not-a-node";`,
		"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6NDQzfQ==",
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp#tag",
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388#name",
		"trojan://secret@example.net:443",
		"api_key = \"sk-not-a-node\"",
		`notes: (trojan://x@y.example:8443).`,
	}, "\n"))

	nodes := extractNodeURIs(body)
	joined := strings.Join(nodes, " ")

	for _, want := range []string{"vmess://", "vless://", "ss://", "trojan://x@y.example:8443"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %#v", want, nodes)
		}
	}
	if strings.Contains(joined, "example.com/not-a-node") {
		t.Fatalf("plain https URL must not be treated as a node: %#v", nodes)
	}
	if strings.Contains(joined, "sk-not-a-node") {
		t.Fatalf("credentials must never be extracted: %#v", nodes)
	}
	for _, node := range nodes {
		if strings.HasSuffix(node, ".") {
			t.Fatalf("trailing punctuation not trimmed: %q", node)
		}
	}
}

func TestExtractNodeURIsDecodesBase64Subscription(t *testing.T) {
	inner := "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.org:443#node\n" +
		"trojan://pw@example.net:8443#two\n"
	blob := base64.StdEncoding.EncodeToString([]byte(inner))

	nodes := extractNodeURIs([]byte(blob))
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes from base64 subscription, got %#v", nodes)
	}
}

func TestExtractNodeURIsPrefersDirectNodesOverBase64(t *testing.T) {
	body := []byte("trojan://direct@example.net:443\n" + base64.StdEncoding.EncodeToString([]byte("ss://x@1.2.3.4:8388")))
	nodes := extractNodeURIs(body)
	if len(nodes) != 1 || !strings.HasPrefix(nodes[0], "trojan://") {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}
}

func TestTrimNodeURIRejectsEmptyPayload(t *testing.T) {
	if got := trimNodeURI("ss://"); got != "" {
		t.Fatalf("empty payload must be rejected, got %q", got)
	}
	if got := trimNodeURI("trojan://a@b.example:443"); got != "trojan://a@b.example:443" {
		t.Fatalf("valid node must survive, got %q", got)
	}
}

func TestGistDiscoveryEndToEnd(t *testing.T) {
	const idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Query().Get("q") == "vmess://" {
			fmt.Fprintf(w, `<a href="/owner/%s">gist</a><a href="/other/%s">gist</a>`, idA, idB)
			return
		}
		fmt.Fprint(w, `<p>no results</p>`)
	})
	mux.HandleFunc("/owner/"+idA+"/raw", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "vless://uuid@example.com:443#a\ntrojan://pw@example.net:8443#b\n")
	})
	mux.HandleFunc("/other/"+idB+"/raw", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "nothing to see here\n")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://", "vless://"},
		MaxPages:   1,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})

	out, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 nodes, got %#v", lines)
	}
	if discovery.CachedNodes() != 2 {
		t.Fatalf("CachedNodes() = %d, want 2", discovery.CachedNodes())
	}
}

func TestGistDiscoveryCachesResults(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `<p>none</p>`)
	}))
	defer server.Close()

	now := time.Now()
	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   1,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
		CacheTTL:   10 * time.Minute,
		Now:        func() time.Time { return now },
	})

	if _, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("first Discover() error = %v", err)
	}
	afterFirst := hits
	if _, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover() error = %v", err)
	}
	if hits != afterFirst {
		t.Fatalf("cached discovery must not refetch: first=%d now=%d", afterFirst, hits)
	}

	now = now.Add(11 * time.Minute)
	if _, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("third Discover() error = %v", err)
	}
	if hits == afterFirst {
		t.Fatal("expired cache must trigger a refetch")
	}
}

func TestGistDiscoveryRespectsMaxGists(t *testing.T) {
	// PRISM-DEVIATION: the upstream test appends to a shared slice from inside
	// the httptest handler. That handler runs on one goroutine per request and
	// discovery fetches gists concurrently, so `go test -race` reports a data
	// race inside the test itself. A mutex removes the race; the expectation is
	// unchanged.
	var (
		mu        sync.Mutex
		requested []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			fmt.Fprintf(w, `<a href="/owner/%032x">g</a>`, i+1)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
		fmt.Fprint(w, "trojan://pw@example.net:8443\n")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   1,
		MaxGists:   3,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	if _, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	mu.Lock()
	fetched := len(requested)
	mu.Unlock()
	if fetched != 3 {
		t.Fatalf("MaxGists not enforced: fetched %d contents", fetched)
	}
}

func TestGistDiscoveryRespectsMaxNodes(t *testing.T) {
	const id = "cccccccccccccccccccccccccccccccc"
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<a href="/owner/%s">g</a>`, id)
	})
	mux.HandleFunc("/owner/"+id+"/raw", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 50; i++ {
			fmt.Fprintf(w, "trojan://pw%d@example.net:8443\n", i)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   1,
		MaxNodes:   5,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	out, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 5 {
		t.Fatalf("MaxNodes not enforced: got %d nodes", got)
	}
}

func TestGistDiscoveryErrorsWhenSearchFailsEverywhere(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   1,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	if _, err := discovery.Discover(context.Background()); err == nil {
		t.Fatal("expected an error when every search fails")
	}
}

func TestGistDiscoveryToleratesUnfetchableGists(t *testing.T) {
	const id = "dddddddddddddddddddddddddddddddd"
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<a href="/owner/%s">g</a>`, id)
	})
	mux.HandleFunc("/owner/"+id+"/raw", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   1,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	// Search worked, so this must not be a hard error: the caller then falls back
	// to cached nodes instead of dropping the discovery source entirely.
	out, err := discovery.Discover(context.Background())
	if err == nil {
		t.Fatalf("expected an error when no gist content was retrievable, got %q", out)
	}
	if !strings.Contains(err.Error(), "no gist content") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGistDiscoveryInterleavesQueries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, `<a href="/%s/%032x">g</a>`, strings.TrimSuffix(q, "://"), i+1)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"aa://", "bb://"},
		MaxPages:   1,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	paths, searched, failed := discovery.collectCandidates(context.Background())
	if searched != 2 || failed != 0 {
		t.Fatalf("searched=%d failed=%d", searched, failed)
	}
	if len(paths) != 6 {
		t.Fatalf("expected 6 candidates, got %d: %#v", len(paths), paths)
	}
	// Round-robin: the two queries must alternate so neither starves the other.
	if !strings.Contains(paths[0], "/aa/") || !strings.Contains(paths[1], "/bb/") {
		t.Fatalf("queries were not interleaved: %#v", paths[:2])
	}
}

func TestGistDiscoverySendsUpdatedSortAndPagination(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		fmt.Fprint(w, `<p>none</p>`)
	}))
	defer server.Close()

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    []string{"vmess://"},
		MaxPages:   3,
		SearchBase: server.URL,
		RawBase:    server.URL,
		Client:     server.Client(),
		Politeness: 0,
	})
	if _, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected the empty page to stop pagination, got %#v", queries)
	}
	if !strings.Contains(queries[0], "s=updated") {
		t.Fatalf("discovery must sort by recently updated: %q", queries[0])
	}
	if !strings.Contains(queries[0], "page=1") {
		t.Fatalf("discovery must page explicitly: %q", queries[0])
	}
}

func TestNewGistDiscoveryAppliesDefaults(t *testing.T) {
	d := NewGistDiscovery(GistDiscoveryConfig{})
	if len(d.cfg.Queries) != len(DefaultGistQueries()) {
		t.Fatalf("queries not defaulted: %#v", d.cfg.Queries)
	}
	if d.cfg.MaxPages != 2 || d.cfg.MaxGists != 40 || d.cfg.MaxNodes != 2000 {
		t.Fatalf("caps not defaulted: %+v", d.cfg)
	}
	if d.cfg.SearchBase != defaultGistSearchBase || d.cfg.RawBase != defaultGistRawBase {
		t.Fatalf("endpoints not defaulted: %+v", d.cfg)
	}
	if d.cfg.CacheTTL != 15*time.Minute {
		t.Fatalf("cache TTL not defaulted: %v", d.cfg.CacheTTL)
	}
}

func TestDefaultGistQueriesExcludePlainHTTP(t *testing.T) {
	for _, q := range DefaultGistQueries() {
		if strings.HasPrefix(q, "http://") || strings.HasPrefix(q, "https://") {
			t.Fatalf("plain HTTP queries would match almost every gist: %q", q)
		}
	}
}
