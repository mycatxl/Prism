package inspection

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func torFixture(now time.Time) []byte {
	stamp := now.UTC().Format("2006-01-02 15:04:05")
	return []byte(fmt.Sprintf(`{"relays_published":%q,"relays":[{"or_addresses":["1.1.1.1:9001","[2606:4700:4700::1111]:443,9001"],"exit_addresses":["8.8.8.8"],"flags":["Running","Guard","Exit"],"last_seen":%q}]}`, stamp, stamp))
}

func TestTorRegistryDistinguishesORGuardAndObservedExitAddresses(t *testing.T) {
	now := time.Now().UTC()
	roles, published, err := decodeTorRegistry(torFixture(now), now)
	if err != nil {
		t.Fatal(err)
	}
	if roles[netip.MustParseAddr("1.1.1.1")] != torRelay|torGuard || roles[netip.MustParseAddr("8.8.8.8")] != torExit {
		t.Fatal("Exit flag was mistaken for an observed exit address")
	}
	if roles[netip.MustParseAddr("2606:4700:4700::1111")] != torRelay|torGuard {
		t.Fatal("IPv6/port-list address was not classified")
	}
	r := &TorRegistry{roles: roles, published: published, checked: now, nextRefresh: now.Add(time.Hour)}
	unknown := r.Source(netip.MustParseAddr("9.9.9.9"))
	if unknown.Evidence == nil || unknown.Evidence.Signals.Tor != nil || len(unknown.Evidence.TorRoles) > 0 {
		t.Fatal("absent registry record was treated as proof of no Tor usage")
	}
	if _, _, err = decodeTorRegistry(torFixture(now.Add(-7*time.Hour)), now); err == nil {
		t.Fatal("stale registry was accepted as fresh")
	}
	if _, _, err = decodeTorRegistry([]byte(`{"relays":null}`), now); err == nil {
		t.Fatal("incomplete registry became an empty clean list")
	}
}

func TestTorRegistryRefreshIsSharedAndConditional(t *testing.T) {
	var calls atomic.Int32
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", now.Format(http.TimeFormat))
		_, _ = w.Write(torFixture(now))
	}))
	defer server.Close()
	registry := NewTorRegistry()
	registry.url = server.URL
	defer registry.client.CloseIdleConnections()
	for range 20 {
		registry.Source(netip.MustParseAddr("8.8.8.8"))
	}
	deadline := time.Now().Add(3 * time.Second)
	for !registry.Status().Ready && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !registry.Status().Ready || calls.Load() != 1 {
		t.Fatal("per-IP lookups did not share one background refresh")
	}
	registry.refresh()
	if calls.Load() != 2 || len(registry.Source(netip.MustParseAddr("8.8.8.8")).Evidence.TorRoles) != 1 {
		t.Fatal("304 response lost existing registry")
	}
}
