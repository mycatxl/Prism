package routing

import (
	"path/filepath"
	"prism/internal/model"
	"prism/internal/state"
	"sync"
	"testing"
	"time"
)

func TestStickyRenewExpireReclaimAndPersistence(t *testing.T) {
	r, plat, state := ownerFixture(t, "203.0.113.1")
	t0 := time.Now()
	ttl := time.Duration(plat.StickyTTLNs)
	if _, err := r.routeSticky(plat, state, "tenant", "session", "example.com", t0); err != nil {
		t.Fatal(err)
	}
	first, _ := state.Leases.GetLease("tenant", "session")
	used := t0.Add(ttl / 2)
	res, err := r.routeSticky(plat, state, "tenant", "session", "example.com", used)
	if err != nil || res.LeaseCreated {
		t.Fatalf("renew: %v %v", res, err)
	}
	renewed, _ := state.Leases.GetLease("tenant", "session")
	if renewed.ExpiryNs != used.Add(ttl).UnixNano() || renewed.CreatedAtNs != first.CreatedAtNs || state.IPLoadStats.Get(first.EgressIP) != 1 || state.Leases.Size() != 1 {
		t.Fatalf("renewal or capacity wrong: %+v", renewed)
	}
	stored := r.ReadLease(model.LeaseKey{PlatformID: plat.ID, UserID: "tenant", Account: "session"})
	r2, _, state2 := ownerFixture(t, "203.0.113.1")
	r2.RestoreLeases([]model.Lease{*stored})
	restored, _ := state2.Leases.GetLease("tenant", "session")
	if restored != renewed {
		t.Fatal("renewed deadline lost on restore")
	}
	cleaner := NewLeaseCleaner(r)
	cleaner.sweepPlatformState(plat.ID, state, first.ExpiryNs+1)
	if state.Leases.Size() != 1 {
		t.Fatal("cleaner used stale pre-renewal deadline")
	}
	cleaner.sweepPlatformState(plat.ID, state, renewed.ExpiryNs+1)
	cleaner.sweepPlatformState(plat.ID, state, renewed.ExpiryNs+2)
	if state.Leases.Size() != 0 || state.IPLoadStats.Get(first.EgressIP) != 0 {
		t.Fatal("expiry did not reclaim exactly one slot")
	}
	if _, err := r.routeSticky(plat, state, "tenant", "session", "example.com", time.Unix(0, renewed.ExpiryNs+3)); err != nil {
		t.Fatal(err)
	}
	if !r.DeleteLease(plat.ID, "tenant", "session") || state.IPLoadStats.Get(first.EgressIP) != 0 {
		t.Fatal("explicit reclaim failed")
	}
}

func TestRenewalFlushDuringEventAndDatabaseRestart(t *testing.T) {
	r, plat, rs := ownerFixture(t, "203.0.113.1")
	root := t.TempDir()
	stateDir, cacheDir := filepath.Join(root, "state"), filepath.Join(root, "cache")
	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	r.onLeaseEvent = func(e LeaseEvent) {
		engine.MarkLease(e.PlatformID, e.UserID, e.Account)
		if err := engine.FlushDirtySets(state.CacheReaders{ReadLease: r.ReadLease}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	_, err = r.routeSticky(plat, rs, "tenant", "session", "example.com", start)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.routeSticky(plat, rs, "tenant", "session", "example.com", start.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := rs.Leases.GetLease("tenant", "session")
	// Supply the real persistence relationships; bootstrap correctly removes
	// leases whose node/subscription/platform disappeared.
	entry, _ := r.pool.GetEntry(want.NodeHash)
	for _, err := range []error{
		engine.UpsertSubscription(model.Subscription{ID: "fixture", Name: "Fixture", URL: "https://example.com", Enabled: true, UpdateIntervalNs: int64(time.Hour), EphemeralNodeEvictDelayNs: int64(72 * time.Hour)}),
		engine.UpsertPlatform(model.Platform{ID: plat.ID, Name: plat.Name, StickyTTLNs: plat.StickyTTLNs, RegexFilters: []string{}, RegionFilters: []string{}, ReverseProxyMissAction: "TREAT_AS_EMPTY", AllocationPolicy: "BALANCED"}),
		engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: want.NodeHash.Hex(), RawOptions: entry.RawOptions, CreatedAtNs: start.UnixNano()}}),
		engine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{{SubscriptionID: "fixture", NodeHash: want.NodeHash.Hex(), Tags: []string{}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	engine2, closer2, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer2.Close()
	rows, err := engine2.LoadAllLeases()
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted leases=%v err=%v", rows, err)
	}
	if rows[0].ExpiryNs != want.ExpiryNs {
		t.Fatal("event-triggered flush read the pre-renewal deadline")
	}
	r2, p2, s2 := ownerFixture(t, "203.0.113.1")
	r2.RestoreLeases(rows)
	_, err = r2.routeSticky(p2, s2, "tenant", "session", "example.com", start.Add(45*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Leases.GetLease("tenant", "session")
	if got.CreatedAtNs != want.CreatedAtNs || got.ExpiryNs <= want.ExpiryNs {
		t.Fatal("restored lease did not renew")
	}
}

func TestRestoredLeaseCapAndConcurrentReclaim(t *testing.T) {
	r, plat, s := ownerFixture(t, "203.0.113.1")
	start := time.Now()
	_, _ = r.routeSticky(plat, s, "tenant", "session", "example.com", start)
	l, _ := s.Leases.GetLease("tenant", "session")
	l.CreatedAtNs = start.Add(-8 * 24 * time.Hour).UnixNano()
	l.ExpiryNs = start.Add(time.Hour).UnixNano()
	s.Leases.CreateLease("tenant", "session", l)
	NewLeaseCleaner(r).sweepPlatformState(plat.ID, s, start.UnixNano())
	if s.Leases.Size() != 0 || s.IPLoadStats.Get(l.EgressIP) != 0 {
		t.Fatal("legacy long TTL bypassed cap")
	}
	s.Leases.CreateLease("tenant", "session", l)
	res, err := r.routeSticky(plat, s, "tenant", "session", "example.com", start)
	if err != nil || !res.LeaseCreated {
		t.Fatal("over-age restored lease served once")
	}
	for range 40 {
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = r.routeSticky(plat, s, "tenant", "session", "example.com", start.Add(30*time.Minute))
		}()
		go func() {
			defer wg.Done()
			NewLeaseCleaner(r).sweepPlatformState(plat.ID, s, start.Add(time.Hour).UnixNano())
		}()
		go func() { defer wg.Done(); r.DeleteLease(plat.ID, "tenant", "session") }()
		wg.Wait()
		if s.IPLoadStats.Get(l.EgressIP) != int64(s.Leases.Size()) {
			t.Fatal("renew/delete/cleaner accounting diverged")
		}
	}
	l = Lease{CreatedAtNs: start.UnixNano(), ExpiryNs: start.Add(time.Hour).UnixNano()}
	if !l.IsExpired(time.Unix(0, l.ExpiryNs)) {
		t.Fatal("expiry boundary is not exclusive")
	}
}

func TestStickyRenewalCannotExceedMaximumLifetime(t *testing.T) {
	r, plat, state := ownerFixture(t, "203.0.113.1")
	start := time.Now()
	_, _ = r.routeSticky(plat, state, "tenant", "session", "example.com", start)
	first, _ := state.Leases.GetLease("tenant", "session")
	for elapsed := 30 * time.Minute; elapsed < MaxStickyLeaseLifetime; elapsed += 30 * time.Minute {
		res, err := r.routeSticky(plat, state, "tenant", "session", "example.com", start.Add(elapsed))
		if err != nil || res.LeaseCreated {
			t.Fatal("unexpected replacement", err)
		}
	}
	last, _ := state.Leases.GetLease("tenant", "session")
	if last.ExpiryNs != start.Add(MaxStickyLeaseLifetime).UnixNano() || last.CreatedAtNs != first.CreatedAtNs {
		t.Fatal("lifetime cap moved")
	}
	res, err := r.routeSticky(plat, state, "tenant", "session", "example.com", start.Add(MaxStickyLeaseLifetime+time.Nanosecond))
	if err != nil || !res.LeaseCreated {
		t.Fatal("expired capped lease renewed", err)
	}
	if state.IPLoadStats.Get(first.EgressIP) != 1 {
		t.Fatal("replacement leaked capacity")
	}
}
