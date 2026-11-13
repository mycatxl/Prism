package topology

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/testutil/measurement"
)

func TestPrivacySubscriptionImportLeaseBoundaryAndRelease(t *testing.T) {
	sm := NewSubscriptionManager()
	sub := subscription.NewSubscription("privacy", "Fixture", "https://fixture.invalid", true, false)
	sm.Register(sub)
	pool := newMeasuredTestPool(sm)
	pool.SetQualitySnapshot(measurement.Snapshot(netip.MustParseAddr("203.0.113.14")))
	cfg := config.NodePrivacyConfig{EchoEnabled: true, ProbeStatus: "ready", OwnCIDRs: []string{"192.0.2.0/24"}}
	policy, err := node.NewPrivacyPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetPrivacyPolicy(policy)
	plat := platform.NewPlatform("privacy", "Privacy", nil, nil)
	pool.RegisterPlatform(plat)
	body := makeSubscriptionJSON(
		`{"type":"http","tag":"plain","server":"203.0.113.10","server_port":80}`,
		`{"type":"socks","tag":"socks","server":"203.0.113.11","server_port":1080}`,
		`{"type":"vless","tag":"vless","server":"203.0.113.12","server_port":80,"uuid":"00000000-0000-0000-0000-000000000001"}`,
		`{"type":"trojan","tag":"trojan-plain","server":"203.0.113.15","server_port":80,"password":"fixture"}`,
		`{"type":"vmess","tag":"vmess-plain","server":"203.0.113.16","server_port":80,"uuid":"00000000-0000-0000-0000-000000000002","security":"none"}`,
		`{"type":"shadowsocks","tag":"ss-plain","server":"203.0.113.17","server_port":80,"method":"none","password":"fixture"}`,
		`{"type":"http","tag":"identity","server":"203.0.113.13","server_port":443,"tls":{"enabled":true},"headers":{"Via":"identity"}}`,
		`{"type":"http","tag":"owned","server":"192.0.2.10","server_port":443,"tls":{"enabled":true}}`,
		`{"type":"http","tag":"safe-self-egress","server":"203.0.113.14","server_port":443,"tls":{"enabled":true}}`,
	)
	newTestScheduler(sm, pool, makeMockFetcher(body, nil)).UpdateSubscription(sub)
	if pool.Size() != 2 {
		t.Fatalf("config leaks were retained: %d", pool.Size())
	}
	var safe node.Hash
	seen := map[string]bool{}
	parsed, _ := subscription.ParseGeneralSubscription(body)
	for _, n := range parsed {
		if reason := node.PrivacyDeletionReason(n.RawOptions); reason != "" {
			seen[reason] = true
			h := node.HashFromRawOptions(n.RawOptions)
			if _, ok := pool.GetEntry(h); ok {
				t.Fatal("config leak retained")
			}
			if _, ok := sub.ManagedNodes().LoadNode(h); ok {
				t.Fatal("config leak mapping retained")
			}
		}
	}
	pool.RangeNodes(func(h node.Hash, e *node.NodeEntry) bool {
		var raw struct {
			Server string `json:"server"`
		}
		_ = json.Unmarshal(e.RawOptions, &raw)
		ob := testutil.NewNoopOutbound()
		e.Outbound.Store(&ob)
		ip := netip.MustParseAddr(raw.Server)
		pool.UpdateNodeEgressIP(h, &ip, nil)
		if pool.PrivacyDeleted(h) {
			seen[node.PrivacyOwnAsset] = true
			return true
		}
		e.SetIdentityMeasurement(ip, "measured")
		latency := 10 * time.Millisecond
		pool.RecordLatency(h, "https://example.com", &latency)
		pool.RecordResult(h, true)
		for _, r := range e.Privacy().Reasons {
			seen[r] = true
		}
		if raw.Server == "203.0.113.14" {
			safe = h
			if !e.PrivacyAllowed() {
				t.Fatal("non-owned self-egress not accepted")
			}
		} else if e.PrivacyAllowed() {
			t.Fatalf("unsafe imported shape accepted: %s", raw.Server)
		}
		return true
	})
	for _, r := range []string{node.PrivacyPlaintext, node.PrivacyIdentity, node.PrivacyOwnAsset} {
		if !seen[r] {
			t.Fatal("missing rejection shape", r)
		}
	}
	if plat.View().Size() != 1 {
		t.Fatalf("sellable=%d want 1", plat.View().Size())
	}
	router := routing.NewRouter(routing.RouterConfig{Pool: pool, Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Hour }})
	for _, account := range []string{"", "sticky"} {
		route, err := router.RouteRequest(plat.Name, "tenant", account, "https://example.com")
		if err != nil || route.NodeHash != safe {
			t.Fatalf("routing: %v %v", route, err)
		}
	}
	// Swap the pointer without rebuilding: stale views must not serve a flagged
	// sticky lease, same-IP replacement, or fresh random allocation.
	cfg.ManualQuarantine = []string{safe.Hex()}
	blocked, _ := node.NewPrivacyPolicy(cfg)
	pool.privacyPolicy.Store(blocked)
	for _, account := range []string{"sticky", "", "new"} {
		if _, err := router.RouteRequest(plat.Name, "tenant", account, "https://example.com"); err == nil {
			t.Fatal("stale view bypassed quarantine", account)
		}
	}
	e, _ := pool.GetEntry(safe)
	if !slices.Contains(e.Privacy().Reasons, node.PrivacyManual) {
		t.Fatal("missing reason")
	}
	if err := router.UpsertLease(model.Lease{PlatformID: plat.ID, UserID: "tenant", Account: "inherited", NodeHash: safe.Hex(), EgressIP: e.GetEgressIP().String()}); err == nil {
		t.Fatal("inheritance bypassed privacy boundary")
	}
	if !pool.MakeHealthyAndEnabledEvaluator()(e) {
		t.Fatal("manual privacy hold changed health classification")
	}
	cfg.ManualQuarantine = nil
	released, _ := node.NewPrivacyPolicy(cfg)
	pool.SetPrivacyPolicy(released)
	if _, err := router.RouteRequest(plat.Name, "tenant", "sticky", "https://example.com"); err != nil {
		t.Fatal("admin release did not restore routing", err)
	}
}

func TestDeletedNodeCannotReturnToEgressOwnershipIndex(t *testing.T) {
	sm := NewSubscriptionManager()
	pool := newTestPool(sm)
	raw := []byte(`{"type":"shadowsocks","server":"203.0.113.2"}`)
	h := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(h, raw, "a")
	old := netip.MustParseAddr("203.0.113.3")
	next := netip.MustParseAddr("203.0.113.4")
	pool.indexEgressIP(h, netip.Addr{}, old)
	pool.DeletePrivacyNode(h, node.PrivacyOwnAsset, "")
	// Simulate a response that retained the entry before deletion and reaches
	// index publication after the remover already emptied its buckets.
	pool.indexEgressIP(h, old, next)
	if len(pool.EgressIPOwners(old))+len(pool.EgressIPOwners(next)) != 0 {
		t.Fatal("late response restored ownership index")
	}
}
