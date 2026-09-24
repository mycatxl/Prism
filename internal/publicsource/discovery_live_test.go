package publicsource

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveGistDiscovery runs the real "gist hunting" method against GitHub:
// search public gists for proxy protocol links sorted by most recently updated,
// then download the raw content of the hits and extract node URIs.
//
// It is opt-in because it depends on the public internet and on GitHub's
// willingness to answer, so it must not run in CI:
//
//	PUBLIC_SOURCE_LIVE_GIST_TEST=1 go test ./internal/publicsource/ -run LiveGist -v
func TestLiveGistDiscovery(t *testing.T) {
	if os.Getenv("PUBLIC_SOURCE_LIVE_GIST_TEST") == "" {
		t.Skip("set PUBLIC_SOURCE_LIVE_GIST_TEST=1 to run live gist discovery")
	}

	discovery := NewGistDiscovery(GistDiscoveryConfig{
		Queries:    DefaultGistQueries(),
		MaxPages:   2,
		MaxGists:   40,
		Politeness: -1, // disable the inter-request delay for this one run
	})

	started := time.Now()
	out, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	elapsed := time.Since(started).Round(time.Millisecond)

	trimmed := strings.TrimSpace(out)
	var nodes []string
	if trimmed != "" {
		nodes = strings.Split(trimmed, "\n")
	}
	t.Logf("live discovery: %d nodes from real gists in %s", len(nodes), elapsed)

	if len(nodes) == 0 {
		t.Fatalf("no nodes extracted from public gists; either GitHub is "+
			"rate-limiting this host or the search page markup changed (%s)", elapsed)
	}
	if got := discovery.CachedNodes(); got != len(nodes) {
		t.Fatalf("CachedNodes() = %d, want %d", got, len(nodes))
	}

	seen := make(map[string]struct{}, len(nodes))
	for i, node := range nodes {
		if !nodeURIRe.MatchString(node) {
			t.Fatalf("node %d is not a proxy URI: %q", i, node)
		}
		// The scope boundary of this module: only proxy node URIs may ever be
		// emitted, never credentials, API keys or other secrets found in gists.
		if strings.ContainsAny(node, " \t\"'<>\\`") {
			t.Fatalf("node %d contains whitespace or shell metacharacters: %q", i, node)
		}
		if _, dup := seen[node]; dup {
			t.Fatalf("node %d is a duplicate: %q", i, node)
		}
		seen[node] = struct{}{}
	}

	sample := nodes
	if len(sample) > 3 {
		sample = sample[:3]
	}
	for _, node := range sample {
		t.Logf("sample: %s", truncateForLog(node, 72))
	}

	// A second call within the cache window must reuse the first result.
	if again, err := discovery.Discover(context.Background()); err != nil {
		t.Fatalf("cached Discover() error = %v", err)
	} else if again != out {
		t.Fatal("cached Discover() returned a different result")
	}
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
