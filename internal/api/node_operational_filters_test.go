package api

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/subscription"
)

func TestNodeOperationalFiltersListAndExport(t *testing.T) {
	srv, cp, runtimeCfg := newControlPlaneTestServer(t)
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LatencyAuthorities = []string{"a.example", "b.example"}
	runtimeCfg.Store(cfg)
	source := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "source", "https://example.com/a", true, false)
	other := subscription.NewSubscription("22222222-2222-2222-2222-222222222222", "other", "https://example.com/b", true, false)
	cp.SubMgr.Register(source)
	cp.SubMgr.Register(other)
	for i, sample := range []struct {
		sub       *subscription.Subscription
		latencies []time.Duration
		hasIP     bool
		region    string
	}{
		{source, []time.Duration{100 * time.Millisecond, 180 * time.Millisecond}, true, "jp"},
		{source, []time.Duration{150 * time.Millisecond}, true, "jp"},
		{source, []time.Duration{300 * time.Millisecond}, true, "us"},
		{source, nil, false, ""},
		{source, []time.Duration{0}, false, ""},
		{other, []time.Duration{50 * time.Millisecond}, true, "jp"},
	} {
		raw := `{"type":"http","server":"1.1.1.1","server_port":` + string(rune('1'+i)) + `080}`
		addNodeForNodeListTest(t, cp, sample.sub, raw, "")
		entry, _ := cp.Pool.GetEntry(node.HashFromRawOptions([]byte(raw)))
		if sample.hasIP {
			entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
		}
		entry.SetEgressRegion(sample.region)
		for j, latency := range sample.latencies {
			entry.LatencyTable.LoadEntry(cfg.LatencyAuthorities[j], node.DomainLatencyStats{Ewma: latency, LastUpdated: time.Now()})
		}
		// Non-reference samples must not affect the range, even on an unmeasured node.
		entry.LatencyTable.LoadEntry("unrelated.example", node.DomainLatencyStats{Ewma: time.Millisecond, LastUpdated: time.Now()})
	}

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"latency_max=150", 2},
		{"latency_min=100.5&latency_max=150", 1},
		{"latency_min=150&latency_max=300", 1},
		{"latency_min=300", 1},
		{"has_egress_ip=true", 4},
		{"has_egress_ip=false", 2},
		{"subscription_id=" + source.ID + "&protocol=http&region=JP&latency_max=150&has_egress_ip=true", 1},
		{"has_egress_ip=false&latency_max=300", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			list := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?"+tc.query+"&limit=1", nil, true)
			if list.Code != http.StatusOK {
				t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
			}
			body := decodeJSONMap(t, list)
			if body["total"] != float64(tc.want) {
				t.Fatalf("list total = %v, want %d", body["total"], tc.want)
			}
			exported := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/export?format=csv&"+tc.query, nil, true)
			if exported.Code != http.StatusOK {
				t.Fatalf("export status = %d: %s", exported.Code, exported.Body.String())
			}
			if rows := strings.Count(exported.Body.String(), "\n") - 1; rows != tc.want {
				t.Fatalf("export rows = %d, want %d", rows, tc.want)
			}
		})
	}
}

func TestNodeOperationalFiltersRejectInvalidValues(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	for _, query := range []string{
		"latency_min=-1", "latency_max=NaN", "latency_max=Inf", "latency_max=bad",
		"latency_max=3600001", "latency_max=1e100", "latency_max=0x10",
		"latency_min=300&latency_max=150", "latency_min=150&latency_max=150",
		"has_egress_ip=maybe", "region=Japan",
	} {
		for _, path := range []string{"/api/v1/nodes?", "/api/v1/nodes/export?format=csv&"} {
			t.Run(path+query, func(t *testing.T) {
				rec := doJSONRequest(t, srv, http.MethodGet, path+query, nil, true)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}
