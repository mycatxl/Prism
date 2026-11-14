package api

import (
	"net/http"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/subscription"
)

func TestLatencyFiltersIncludeMeasuredSubMillisecondNodes(t *testing.T) {
	srv, cp, current := newControlPlaneTestServer(t)
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LatencyAuthorities = []string{"a.example"}
	current.Store(cfg)
	source := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "fractional", "https://example.com/a", true, false)
	cp.SubMgr.Register(source)
	const raw = `{"type":"http","server":"1.1.1.1","server_port":8080}`
	addNodeForNodeListTest(t, cp, source, raw, "203.0.113.10")
	entry, _ := cp.Pool.GetEntry(node.HashFromRawOptions([]byte(raw)))
	entry.LatencyTable.LoadEntry("a.example", node.DomainLatencyStats{Ewma: 250 * time.Microsecond, LastUpdated: time.Now()})
	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?latency_min=0.25&latency_max=0.5", nil, true)
	body := decodeJSONMap(t, rec)
	if rec.Code != http.StatusOK || body["total"] != float64(1) {
		t.Fatal("a positive sub-millisecond measurement was treated as unmeasured")
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["reference_latency_ms"] != 0.25 {
		t.Fatal("filter and displayed reference latency diverged")
	}
}
