package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestTorExpiredCacheCannotManufactureFreshEvidence(t *testing.T) {
	registry := NewTorRegistry(TorRegistryOptions{Now: func() time.Time { return testNow }})
	// Suppress background networking; only the cached lookup is under test.
	registry.nextRefresh = testNow.Add(time.Hour)
	provider := NewTorProvider(TorProviderOptions{Registry: registry, TTL: 48 * time.Hour, Now: func() time.Time { return testNow }})
	for _, published := range []time.Time{time.Time{}, testNow.Add(-torRegistryValidity), testNow.Add(-72 * time.Hour)} {
		registry.Seed(map[netip.Addr]uint8{testIP: torRelay | torExit}, published, testNow)
		result := provider.Lookup(testIP)
		if result.Evidence != nil || result.Err == nil {
			t.Fatal("expired registry produced current Tor evidence")
		}
		want := CodeRegistryExpired
		if published.IsZero() {
			want = CodeRegistryUnavailable
		}
		if result.Err.Code != want {
			t.Fatalf("cause=%s want %s", result.Err.Code, want)
		}
	}
	registry.Seed(map[netip.Addr]uint8{testIP: torRelay | torExit}, testNow.Add(-time.Hour), testNow)
	evidence := mustEvidence(t, provider.Lookup(testIP))
	if evidence.ValidUntil.After(testNow.Add(-time.Hour).Add(torRegistryValidity)) {
		t.Fatal("configured TTL extended the registry's actual validity")
	}
	absent := provider.Lookup(netip.MustParseAddr("9.9.9.9"))
	if absent.Evidence != nil || absent.Err == nil || absent.Err.Code != CodeTorNotListed {
		t.Fatal("unlisted relay must stay unknown, distinct from a broken registry")
	}
}

func TestTorNotModifiedAllowsTheNextRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }))
	defer server.Close()
	registry := NewTorRegistry(TorRegistryOptions{URL: server.URL, Client: server.Client(), Now: func() time.Time { return testNow }})
	registry.Seed(map[netip.Addr]uint8{testIP: torRelay}, testNow.Add(-time.Hour), testNow.Add(-time.Hour))
	registry.refreshing = true
	if err := registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registry.refreshing {
		t.Fatal("304 permanently blocks subsequent automatic refreshes")
	}
	if !registry.published.Equal(testNow.Add(-time.Hour)) {
		t.Fatal("304 must not refresh the publication timestamp")
	}
}
