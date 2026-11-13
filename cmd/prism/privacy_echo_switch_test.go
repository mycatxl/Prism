package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"prism/internal/config"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/nodeprivacy"
	"prism/internal/probe"
	"prism/internal/state"
	"prism/internal/testutil"
	"prism/internal/topology"
)

func TestEchoSwitchCannotSaveMeasuredLeak(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, discovery := range []string{"", "failed", "ready"} {
			t.Run(fmt.Sprintf("enabled=%t/discovery=%s", enabled, discovery), func(t *testing.T) {
				root := t.TempDir()
				engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
				if err != nil {
					t.Fatal(err)
				}
				defer closer.Close()
				cfg := config.NodePrivacyConfig{EchoEnabled: true, EchoURL: "https://echo.example/check", ProbeStatus: "ready"}
				policy, _ := node.NewPrivacyPolicy(cfg)
				sm := topology.NewSubscriptionManager()
				pool := topology.NewGlobalNodePool(topology.PoolConfig{PrivacyPolicy: policy, SubLookup: sm.Lookup, MaxConsecutiveFailures: func() int { return 3 }})
				pm := probe.NewProbeManager(probe.ProbeConfig{Pool: pool, Concurrency: 1})
				app := &prismApp{stateEngine: engine, topoRuntime: &topologyRuntime{pool: pool, subManager: sm, probeMgr: pm}}
				if err := app.initPrivacyDeletion(); err != nil {
					t.Fatal(err)
				}
				raw := json.RawMessage(`{"type":"http","tls":{"enabled":true}}`)
				h := node.HashFromRawOptions(raw)
				pool.AddNodeFromSub(h, raw, "fixture")
				entry, ok := pool.GetEntry(h)
				if !ok {
					t.Fatal("fixture missing")
				}
				exit := netip.MustParseAddr("203.0.113.9")
				entry.SetEgressIP(exit)
				ob := testutil.NewNoopOutbound()
				entry.Outbound.Store(&ob)
				calls := 0
				fetch := func(_ context.Context, _ adapter.Outbound, _ string, opt netutil.OutboundHTTPOptions) ([]byte, time.Duration, error) {
					calls++
					// A setting changes while the already authorized request is in flight.
					next := cfg
					next.EchoEnabled, next.ProbeStatus = enabled, discovery
					p, _ := node.NewPrivacyPolicy(next)
					pool.SetPrivacyPolicy(p)
					response := nodeprivacy.EchoResponse{ObservedAddress: exit.String(), Marker: opt.Headers.Get(nodeprivacy.MarkerHeader), MarkerSurvived: true, Headers: map[string][]string{}}
					for _, key := range nodeprivacy.IdentityHeaders {
						response.Headers[key] = nil
					}
					response.Headers["X-Forwarded-For"] = []string{"192.0.2.9"}
					body, err := json.Marshal(response)
					return body, 0, err
				}
				disabled := cfg
				disabled.EchoEnabled = false
				measureNodeIdentityUsing(pool, h, exit, disabled, fetch)
				if calls != 0 || pool.PrivacyDeleted(h) {
					t.Fatal("disabled switch still measured")
				}
				measureNodeIdentityUsing(pool, h, exit, cfg, fetch)
				if calls != 1 || !pool.PrivacyDeleted(h) || pool.PrivacyDeletionReason(h) != node.PrivacyEchoLeak || entry.PrivacyAllowed() {
					t.Fatal("measured leak survived configuration change")
				}
				if _, ok := pool.GetEntry(h); ok {
					t.Fatal("leak retained in inventory")
				}
				events, err := engine.PrivacyDeletions()
				if err != nil || len(events) != 1 || events[0].Reason != node.PrivacyEchoLeak {
					t.Fatal("leak not journaled", err)
				}
				pool.AddNodeFromSub(h, raw, "fixture")
				if _, ok := pool.GetEntry(h); ok {
					t.Fatal("switch bypassed reimport fence")
				}
			})
		}
	}
}
