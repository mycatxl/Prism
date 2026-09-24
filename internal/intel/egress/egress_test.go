package egress

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"prism/internal/intel/store"
	"prism/internal/netutil"
	"prism/internal/node"
)

// fakeFetcher serves canned trace bodies per URL and records the call order.
type fakeFetcher struct {
	mu     sync.Mutex
	byURL  map[string][]byte
	errs   map[string]error
	calls  []string
	failV6 bool
	lastUA string
}

func (f *fakeFetcher) FetchWithOptions(_ context.Context, _ node.Hash, url string, _ netutil.OutboundHTTPOptions) ([]byte, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, url)
	if err := f.errs[url]; err != nil {
		return nil, 0, err
	}
	if body, ok := f.byURL[url]; ok {
		return body, time.Millisecond, nil
	}
	return nil, 0, errors.New("no fixture for " + url)
}

func newTestProbe(t *testing.T, fetcher Fetcher, now time.Time) *Probe {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Probe{Fetcher: fetcher, Store: st, Now: func() time.Time { return now }}
}

func TestParseTrace(t *testing.T) {
	trace, err := ParseTrace([]byte("fl=abc\nh=1.1.1.1\nip=198.51.100.7\nloc=JP\ncolo=NRT\nts=1\n"))
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if trace.IP != netip.MustParseAddr("198.51.100.7") || trace.Loc != "JP" || trace.Colo != "NRT" {
		t.Fatalf("trace = %+v", trace)
	}

	if _, err := ParseTrace([]byte("loc=JP\n")); err == nil {
		t.Fatal("expected error for a trace without ip")
	}
	if _, err := ParseTrace([]byte("ip=not-an-ip\n")); err == nil {
		t.Fatal("expected error for an invalid ip")
	}

	// A v6 body unmaps a v4-mapped address.
	trace, err = ParseTrace([]byte("ip=::ffff:203.0.113.9\n"))
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if trace.IP != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("ip = %s, want 203.0.113.9", trace.IP)
	}
}

func TestProbe_RunRecordsBothFamiliesAndHistory(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{byURL: map[string][]byte{
		DefaultTraceURLv4: []byte("ip=198.51.100.10\nloc=JP\ncolo=NRT\n"),
		DefaultTraceURLv6: []byte("ip=2001:db8::10\nloc=JP\ncolo=NRT\n"),
	}}
	probe := newTestProbe(t, fetcher, now)

	hash, err := node.ParseHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseHex: %v", err)
	}
	result, err := probe.Run(context.Background(), hash)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Changed || result.V6Failed {
		t.Fatalf("result = %+v", result)
	}
	if result.Row.IPv4 != "198.51.100.10" || result.Row.IPv6 != "2001:db8::10" {
		t.Fatalf("row = %+v", result.Row)
	}
	if result.Row.Colo != "NRT" || result.Row.Loc != "JP" {
		t.Fatalf("row metadata = %+v", result.Row)
	}
	if result.Row.V4ObservedNs != now.UnixNano() || result.Row.V6CheckedNs != now.UnixNano() {
		t.Fatalf("timestamps = %+v", result.Row)
	}

	// A second run with the same addresses adds no history rows.
	result, err = probe.Run(context.Background(), hash)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Changed {
		t.Fatal("identical addresses reported a change")
	}
	history, err := probe.Store.ListEgressHistory(context.Background(), hash.String(), 20)
	if err != nil {
		t.Fatalf("ListEgressHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %d rows, want 2 (one per family)", len(history))
	}
}

func TestProbe_IPv6FailureIsNotFatal(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		byURL: map[string][]byte{
			DefaultTraceURLv4: []byte("ip=198.51.100.20\nloc=US\ncolo=LAX\n"),
			// IPv6 answers with an IPv4 address: treated as "no IPv6".
			DefaultTraceURLv6: []byte("ip=198.51.100.20\n"),
		},
	}
	probe := newTestProbe(t, fetcher, now)
	hash, _ := node.ParseHex("ffffffffffffffffffffffffffffffff")

	result, err := probe.Run(context.Background(), hash)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.V6Failed || result.Row.IPv6 != "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Row.V6CheckedNs != now.UnixNano() || result.Row.V6ObservedNs != 0 {
		t.Fatalf("row = %+v, want a recorded attempt and no observed v6", result.Row)
	}

	// A hard IPv6 failure behaves the same way.
	fetcher.errs = map[string]error{DefaultTraceURLv6: errors.New("dial tcp6: network is unreachable")}
	result, err = probe.Run(context.Background(), hash)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.V6Failed {
		t.Fatal("a failed v6 request must be reported as V6Failed")
	}
}

func TestProbe_IPv4FailureIsFatal(t *testing.T) {
	probe := newTestProbe(t, &fakeFetcher{
		errs: map[string]error{DefaultTraceURLv4: errors.New("connection refused")},
	}, time.Now())
	hash, _ := node.ParseHex("00000000000000000000000000000001")

	if _, err := probe.Run(context.Background(), hash); !errors.Is(err, ErrNoIPv4) {
		t.Fatalf("err = %v, want ErrNoIPv4", err)
	}
}

func TestProbe_IPv4ChangeAppendsHistory(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{byURL: map[string][]byte{
		DefaultTraceURLv4: []byte("ip=198.51.100.30\nloc=JP\ncolo=NRT\n"),
	}, errs: map[string]error{DefaultTraceURLv6: errors.New("no v6")}}
	probe := newTestProbe(t, fetcher, now)
	hash, _ := node.ParseHex("00000000000000000000000000000002")
	if _, err := probe.Run(context.Background(), hash); err != nil {
		t.Fatalf("Run: %v", err)
	}

	fetcher.byURL[DefaultTraceURLv4] = []byte("ip=203.0.113.30\nloc=JP\ncolo=NRT\n")
	now = now.Add(time.Hour)
	result, err := probe.Run(context.Background(), hash)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Changed || result.Row.IPv4 != "203.0.113.30" {
		t.Fatalf("result = %+v", result)
	}
	history, err := probe.Store.ListEgressHistory(context.Background(), hash.String(), 20)
	if err != nil {
		t.Fatalf("ListEgressHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %d rows, want 2", len(history))
	}
	if history[0].IP != "203.0.113.30" {
		t.Fatalf("newest history = %+v", history[0])
	}
}

func TestProbe_SyncProbeFailureDoesNotFailTheWrite(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	probe := newTestProbe(t, &fakeFetcher{
		byURL: map[string][]byte{DefaultTraceURLv4: []byte("ip=198.51.100.40\n")},
		errs:  map[string]error{DefaultTraceURLv6: errors.New("no v6")},
	}, now)
	probe.ProbeEgressSync = func(context.Context, node.Hash) error { return errors.New("cache.db write failed") }
	hash, _ := node.ParseHex("00000000000000000000000000000003")

	if _, err := probe.Run(context.Background(), hash); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok, err := probe.Store.GetNodeEgress(context.Background(), hash.String()); err != nil || !ok {
		t.Fatalf("egress row missing (ok=%v err=%v)", ok, err)
	}
}

func TestProbe_RequiresFetcherAndStore(t *testing.T) {
	hash, _ := node.ParseHex("00000000000000000000000000000004")
	if _, err := (&Probe{}).Run(context.Background(), hash); err == nil {
		t.Fatal("expected an error when the store is missing")
	}
	if _, err := (&Probe{Store: &store.Store{}}).Run(context.Background(), hash); err == nil {
		t.Fatal("expected an error when the fetcher is missing")
	}
}
