package publicsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type downloaderFunc func(context.Context, string) ([]byte, error)

func (f downloaderFunc) Download(ctx context.Context, source string) ([]byte, error) {
	return f(ctx, source)
}

func TestCollectorRefreshParsesDeduplicatesAndFilters(t *testing.T) {
	responses := map[string]string{
		// Prism's subscription parser imports socks5:// and socks5h:// URI lines but
		// not socks4:// / socks4a:// (upstream Resin's parser does), so the
		// scheme-qualified SOCKS line below uses socks5 to stay importable.
		"source-a": "1.2.3.4:80\n1.2.3.4:80\n10.0.0.1:80\n127.0.0.1:8080\n[2001:4860:4860::8888]:443\nsocks5://8.8.8.8:1080\n",
		"source-b": `{"proxies":[{"name":"http","type":"http","server":"93.184.216.35","port":8080},{"name":"dup","type":"http","server":"1.2.3.4","port":80},{"name":"socks","type":"socks5","server":"93.184.216.36","port":1080}]}`,
	}
	collector := NewCollector(Config{
		Enabled:  true,
		Sources:  []string{"source-a", "source-b"},
		MaxNodes: 0,
	}, downloaderFunc(func(_ context.Context, source string) ([]byte, error) {
		return []byte(responses[source]), nil
	}), nil)

	snapshot, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	if snapshot.SourceCount != 2 || snapshot.CandidateCount < 7 {
		t.Fatalf("unexpected source counters: %+v", snapshot)
	}
	if snapshot.UniqueNodes != 5 || strings.Count(snapshot.Content, `"server"`) != 5 {
		t.Fatalf("unexpected unique node count: snapshot=%d content=%q", snapshot.UniqueNodes, snapshot.Content)
	}
	if snapshot.Degraded {
		t.Fatalf("clean refresh must not be marked degraded: %+v", snapshot)
	}
	for _, forbidden := range []string{"10.0.0.1", "127.0.0.1"} {
		if strings.Contains(snapshot.Content, forbidden) {
			t.Fatalf("private node %q was not filtered: %q", forbidden, snapshot.Content)
		}
	}
	if !strings.Contains(snapshot.Content, "2001:4860:4860::8888") || !strings.Contains(snapshot.Content, "8.8.8.8") {
		t.Fatalf("expected IPv6 and SOCKS nodes: %q", snapshot.Content)
	}
	if collector.LastAttempt().Err != nil || !collector.LastAttempt().Published {
		t.Fatalf("unexpected attempt: %+v", collector.LastAttempt())
	}
}

func TestCollectorMaxNodesAndDeterministicOutput(t *testing.T) {
	collector := NewCollector(Config{
		Enabled:  true,
		Sources:  []string{"source"},
		MaxNodes: 2,
	}, downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
		return []byte("1.1.1.1:80\n8.8.8.8:80\n9.9.9.9:80\n"), nil
	}), nil)

	first, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("first RefreshNow() error = %v", err)
	}
	second, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("second RefreshNow() error = %v", err)
	}
	if first.Content != second.Content {
		t.Fatalf("output is not deterministic: first=%q second=%q", first.Content, second.Content)
	}
	if first.UniqueNodes != 2 || strings.Count(first.Content, `"server"`) != 2 {
		t.Fatalf("MaxNodes was not applied: %+v", first)
	}
}

// A permanently dead source must never block publishing from healthy sources,
// otherwise the standalone sync tool could never create its subscription.
func TestCollectorPublishesDespitePermanentlyDeadSource(t *testing.T) {
	collector := NewCollector(Config{
		Enabled: true,
		Sources: []string{"healthy", "dead"},
	}, downloaderFunc(func(_ context.Context, source string) ([]byte, error) {
		if source == "dead" {
			return nil, errors.New("404 not found")
		}
		return []byte("8.8.8.8:80\n1.1.1.1:80\n"), nil
	}), nil)

	snapshot, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("RefreshNow() must publish from healthy sources, got error = %v", err)
	}
	if snapshot.UniqueNodes != 2 {
		t.Fatalf("expected healthy nodes to be published: %+v", snapshot)
	}
	if !snapshot.Degraded {
		t.Fatal("snapshot with a failed source must be marked degraded")
	}
	var dead SourceResult
	for _, result := range snapshot.Results {
		if result.URL == "dead" {
			dead = result
		}
	}
	if dead.Error == "" || dead.UsedCache {
		t.Fatalf("unexpected dead source result: %+v", dead)
	}
}

// A source that fails after a previous success must fall back to its cached
// nodes so a transient outage cannot drop nodes from the pool.
func TestCollectorFallsBackToCachedNodes(t *testing.T) {
	fail := false
	collector := NewCollector(Config{
		Enabled: true,
		Sources: []string{"source"},
	}, downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
		if fail {
			return nil, errors.New("temporary outage")
		}
		return []byte("8.8.8.8:80\n1.1.1.1:80\n"), nil
	}), nil)

	before, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("initial refresh error = %v", err)
	}
	fail = true
	after, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("cached refresh must still publish, got %v", err)
	}
	if after.Content != before.Content || after.UniqueNodes != before.UniqueNodes {
		t.Fatalf("cache fallback lost nodes: before=%+v after=%+v", before, after)
	}
	if !after.Degraded {
		t.Fatal("cache-backed snapshot must be marked degraded")
	}
	if result := after.Results[0]; !result.UsedCache {
		t.Fatalf("expected cache usage to be reported: %+v", result)
	}
}

// With no source and no cache there is nothing to publish, so the previous
// snapshot must be preserved and the attempt must report an error.
func TestCollectorEmptyResultKeepsPreviousSnapshot(t *testing.T) {
	collector := NewCollector(Config{
		Enabled: true,
		Sources: []string{"source"},
	}, downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
		return []byte("# no proxies here\n"), nil
	}), nil)

	snapshot, err := collector.RefreshNow()
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("refresh error = %v, want ErrEmptyResult", err)
	}
	if snapshot.Content != "" || collector.Status().Content != "" {
		t.Fatalf("nothing should have been published: %+v", collector.Status())
	}
	if collector.LastAttempt().Err == nil {
		t.Fatal("LastAttempt must record the failure for observability")
	}
}

func TestCollectorRespectsContentByteBudget(t *testing.T) {
	collector := NewCollector(Config{
		Enabled:         true,
		Sources:         []string{"source"},
		MaxContentBytes: 220,
	}, downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
		return []byte("1.1.1.1:80\n8.8.8.8:80\n9.9.9.9:80\n8.8.4.4:80\n1.0.0.1:80\n"), nil
	}), nil)

	snapshot, err := collector.RefreshNow()
	if err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	if len(snapshot.Content) > 220 {
		t.Fatalf("content exceeds budget: %d bytes", len(snapshot.Content))
	}
	if snapshot.UniqueNodes == 0 || snapshot.UniqueNodes >= 5 {
		t.Fatalf("expected partial node set within budget: %+v", snapshot)
	}
	if !snapshot.Degraded {
		t.Fatal("budget-truncated snapshot must be marked degraded")
	}
	var payload struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(snapshot.Content), &payload); err != nil {
		t.Fatalf("truncated content must stay valid JSON: %v (content=%q)", err, snapshot.Content)
	}
	if len(payload.Outbounds) != snapshot.UniqueNodes {
		t.Fatalf("UniqueNodes (%d) must match payload (%d)", snapshot.UniqueNodes, len(payload.Outbounds))
	}
}

func TestCollectorOnAttemptReportsFailures(t *testing.T) {
	var mu sync.Mutex
	var attempts []Attempt
	collector := NewCollector(Config{
		Enabled: true,
		Sources: []string{"source"},
		OnAttempt: func(attempt Attempt) {
			mu.Lock()
			attempts = append(attempts, attempt)
			mu.Unlock()
		},
	}, downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("boom")
	}), nil)

	if _, err := collector.RefreshNow(); err == nil {
		t.Fatal("expected refresh failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 1 || attempts[0].Err == nil {
		t.Fatalf("OnAttempt must report the failure: %+v", attempts)
	}
	if attempts[0].Published {
		t.Fatalf("failed attempt must not be marked published: %+v", attempts[0])
	}
}

func TestCollectorStartStopIsSafe(t *testing.T) {
	collector := NewCollector(Config{
		Enabled:  true,
		Sources:  []string{"source"},
		Interval: time.Hour,
	}, downloaderFunc(func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return []byte("8.8.8.8:80\n"), nil
		}
	}), nil)

	collector.Start()
	collector.Start()
	deadline := time.Now().Add(time.Second)
	for collector.Status().UniqueNodes == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	collector.Stop()
	collector.Stop()
	if collector.Status().UniqueNodes != 1 {
		t.Fatalf("collector did not complete initial refresh: %+v", collector.Status())
	}
}

// Stop must not block on a slow in-flight snapshot callback.
func TestCollectorStopCancelsInFlightRefresh(t *testing.T) {
	blocking := make(chan struct{})
	started := make(chan struct{})
	collector := NewCollector(Config{
		Enabled:  true,
		Sources:  []string{"source"},
		Interval: time.Hour,
	}, downloaderFunc(func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-blocking:
			return []byte("8.8.8.8:80\n"), nil
		}
	}), nil)

	collector.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start refreshing")
	}
	done := make(chan struct{})
	go func() {
		collector.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked on in-flight refresh")
	}
}
