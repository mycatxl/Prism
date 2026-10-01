package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/intel/egress"
)

// panelEgressTestBody mirrors a Cloudflare /cdn-cgi/trace response.
const panelEgressTestBody = "fl=x\nh=1.1.1.1\nip=198.51.100.7\nloc=JP\ncolo=NRT\n"

// waitForFetch waits until the background self-trace attempt has finished,
// successfully or not. The provider records the attempt under its mutex, so this
// is deterministic and adds no requests of its own.
func waitForFetch(t *testing.T, p *PanelEgress) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		done := !p.fetchedAt.IsZero() && !p.fetching
		p.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the panel egress background fetch")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPanelEgressProviderDefaults(t *testing.T) {
	provider := NewPanelEgressProvider()
	if got := provider.url(); got != egress.DefaultTraceURLv4 {
		t.Errorf("url: got %q, want %q", got, egress.DefaultTraceURLv4)
	}
	if got := provider.ttl(); got != PanelEgressTTL {
		t.Errorf("ttl: got %s, want %s", got, PanelEgressTTL)
	}
	if got := provider.timeout(); got != PanelEgressTimeout {
		t.Errorf("timeout: got %s, want %s", got, PanelEgressTimeout)
	}

	// The zero value is usable and a nil provider is safe for the API layer.
	var zero *PanelEgress
	if snapshot := zero.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
		t.Errorf("nil provider snapshot: got %+v, want empty", snapshot)
	}
	var empty PanelEgress
	if got := empty.url(); got != egress.DefaultTraceURLv4 {
		t.Errorf("zero value url: got %q, want %q", got, egress.DefaultTraceURLv4)
	}
}

func TestPanelEgressSnapshotParsesAndCaches(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		if got := r.Header.Get("User-Agent"); got != panelEgressUserAgent {
			t.Errorf("user agent: got %q, want %q", got, panelEgressUserAgent)
		}
		_, _ = w.Write([]byte(panelEgressTestBody))
	}))
	defer server.Close()

	provider := &PanelEgress{URL: server.URL, Client: server.Client(), TTL: time.Hour}

	// The first Snapshot only schedules the fetch; the values arrive later.
	first := provider.Snapshot()
	if first != (PanelEgressSnapshot{}) {
		t.Fatalf("first snapshot: got %+v, want empty", first)
	}
	waitForFetch(t, provider)

	want := PanelEgressSnapshot{Region: "JP", IP: "198.51.100.7"}
	if got := provider.Snapshot(); got != want {
		t.Fatalf("snapshot: got %+v, want %+v", got, want)
	}

	// Inside the TTL every call is served from the cache.
	for i := 0; i < 5; i++ {
		if got := provider.Snapshot(); got != want {
			t.Fatalf("cached snapshot: got %+v, want %+v", got, want)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("trace requests: got %d, want 1", got)
	}
}

func TestPanelEgressSnapshotSingleFlight(t *testing.T) {
	var requests int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		<-release
		_, _ = w.Write([]byte(panelEgressTestBody))
	}))
	defer server.Close()

	provider := &PanelEgress{URL: server.URL, Client: server.Client(), TTL: time.Hour}

	// The handler blocks until release is closed, so all concurrent callers
	// observe the in-flight fetch and must not start their own.
	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if snapshot := provider.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
				t.Errorf("concurrent snapshot: got %+v, want empty", snapshot)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(release)

	waitForFetch(t, provider)
	if got := provider.Snapshot(); got != (PanelEgressSnapshot{Region: "JP", IP: "198.51.100.7"}) {
		t.Fatalf("snapshot after single-flight: got %+v", got)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("trace requests: got %d, want 1", got)
	}
}

func TestPanelEgressSnapshotUnknownOnFailure(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// The clock is injected so the TTL can be crossed without sleeping.
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	provider := &PanelEgress{
		URL:    server.URL,
		Client: server.Client(),
		TTL:    PanelEgressTTL,
		Now:    func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	}

	// A failure never surfaces an error: the snapshot is simply empty.
	if snapshot := provider.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
		t.Fatalf("first snapshot: got %+v, want empty", snapshot)
	}
	waitForFetch(t, provider)
	if snapshot := provider.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
		t.Fatalf("snapshot after failure: got %+v, want empty", snapshot)
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("trace requests inside TTL: got %d, want 1", got)
	}

	// Crossing the TTL retries exactly once more.
	clock.Add(int64(PanelEgressTTL))
	provider.Snapshot()
	waitForFetch(t, provider)
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("trace requests after TTL: got %d, want 2", got)
	}
}

func TestPanelEgressSnapshotUnknownWithoutLoc(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		// ip is present but loc is missing: the result is unknown.
		_, _ = w.Write([]byte("fl=x\nip=198.51.100.7\ncolo=NRT\n"))
	}))
	defer server.Close()

	provider := &PanelEgress{URL: server.URL, Client: server.Client()}
	if snapshot := provider.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
		t.Fatalf("first snapshot: got %+v, want empty", snapshot)
	}
	waitForFetch(t, provider)
	if snapshot := provider.Snapshot(); snapshot != (PanelEgressSnapshot{}) {
		t.Fatalf("snapshot without loc: got %+v, want empty", snapshot)
	}
	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("trace requests: got %d, want 1", got)
	}
}

// A successful observation survives a later failure: the cache keeps the last
// known region instead of degrading to unknown.
func TestPanelEgressSnapshotKeepsLastKnownOnFailure(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(panelEgressTestBody))
	}))
	defer server.Close()

	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	provider := &PanelEgress{
		URL:    server.URL,
		Client: server.Client(),
		Now:    func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	}

	provider.Snapshot()
	waitForFetch(t, provider)
	want := PanelEgressSnapshot{Region: "JP", IP: "198.51.100.7"}
	if got := provider.Snapshot(); got != want {
		t.Fatalf("snapshot: got %+v, want %+v", got, want)
	}

	fail.Store(true)
	clock.Add(int64(PanelEgressTTL))
	provider.Snapshot()
	waitForFetch(t, provider)
	if got := provider.Snapshot(); got != want {
		t.Fatalf("snapshot after failed refresh: got %+v, want %+v", got, want)
	}
}
