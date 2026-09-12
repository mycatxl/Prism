package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/inspection"
	"prism/internal/node"
	"prism/internal/quality"
	"prism/internal/service"
	"prism/internal/subscription"
	"prism/internal/testutil"
)

type apiQualityProvider struct{ calls atomic.Int32 }

func (p *apiQualityProvider) Lookup(_ context.Context, ip netip.Addr) (*quality.Evidence, error) {
	p.calls.Add(1)
	score := 0
	now := time.Now().UTC()
	// Synthetic contract evidence; this is not an assessment of the real IP.
	return &quality.Evidence{IP: ip.String(), Provider: quality.ProviderID, Profile: quality.ProfileID,
		IPType: "residential", RiskScore: &score, Grade: "low", ObservedAt: now, ValidUntil: now.Add(time.Hour)}, nil
}

func TestQualityAPIAuthDedupNodeProjectionAndFilters(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	provider := &apiQualityProvider{}
	manager, err := inspection.NewManagerWithSources(cp.Engine, config.QualityConfig{Enabled: true, Workers: 1, QueueSize: 16, Timeout: time.Second}, nil,
		[]inspection.Source{{ID: quality.ProviderID, Name: "Fixture provider", Profile: quality.ProfileID, Configured: true,
			HasKey: true, DailyLimit: 5, CredentialID: "credential-must-not-be-public", Provider: provider}})
	if err != nil {
		t.Fatal(err)
	}
	cp.Inspection = manager
	manager.Start()
	t.Cleanup(manager.Stop)
	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "quality-fixture", "https://example.com/fixture", true, false)
	cp.SubMgr.Register(sub)
	var entries []*node.NodeEntry
	for port := 8001; port <= 8002; port++ {
		raw := json.RawMessage(fmt.Sprintf(`{"type":"http","server":"127.0.0.1","server_port":%d}`, port))
		hash := node.HashFromRawOptions(raw)
		cp.Pool.AddNodeFromSub(hash, raw, sub.ID)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{fmt.Sprintf("node-%d", port)}})
		entry, ok := cp.Pool.GetEntry(hash)
		if !ok {
			t.Fatal("node fixture missing")
		}
		outbound := testutil.NewNoopOutbound()
		entry.Outbound.Store(&outbound)
		entry.SetEgressIP(netip.MustParseAddr("8.8.8.8"))
		entry.LastEgressUpdate.Store(time.Now().UnixNano())
		entry.FailureCount.Store(2)
		entries = append(entries, entry)
	}
	for _, path := range []string{"/api/v1/quality/status", "/api/v1/quality/assessments", "/api/v1/quality/ip/8.8.8.8"} {
		if response := doJSONRequest(t, srv, http.MethodGet, path, nil, false); response.Code != http.StatusUnauthorized {
			t.Fatalf("quality API was public: %s", path)
		}
	}
	if response := doJSONRequest(t, srv, http.MethodPost, "/api/v1/quality/ip/8.8.8.8/actions/probe", nil, false); response.Code != http.StatusUnauthorized {
		t.Fatal("inspection action was public")
	}
	if response := doJSONRequest(t, srv, http.MethodPost, "/api/v1/quality/ip/127.0.0.1/actions/probe", nil, true); response.Code != http.StatusBadRequest {
		t.Fatal("private-IP inspection was accepted")
	}
	if provider.calls.Load() != 0 {
		t.Fatal("rejected requests reached the provider")
	}
	for _, entry := range entries {
		response := doJSONRequest(t, srv, http.MethodPost, "/api/v1/nodes/"+entry.Hash.Hex()+"/actions/probe-quality", nil, true)
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("inspection action failed: %d %s", response.Code, response.Body.String())
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Snapshot(netip.MustParseAddr("8.8.8.8")).State != "valid" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if provider.calls.Load() != 1 {
		t.Fatal("two nodes sharing an exit made duplicate provider requests")
	}
	var evidenceID string
	for _, entry := range entries {
		response := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+entry.Hash.Hex(), nil, true)
		var summary service.NodeSummary
		if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Protocol != "http" || summary.Quality.State != "valid" || summary.Quality.Evidence == nil || summary.Quality.Evidence.RiskScore == nil || *summary.Quality.Evidence.RiskScore != 0 {
			t.Fatalf("node did not receive quality evidence: %+v", summary.Quality)
		}
		if evidenceID == "" {
			evidenceID = summary.Quality.Evidence.ID
		} else if summary.Quality.Evidence.ID != evidenceID {
			t.Fatal("shared-IP nodes did not share evidence")
		}
		if entry.FailureCount.Load() != 2 {
			t.Fatal("quality lookup changed node health")
		}
	}
	response := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?ip_type=residential&risk_grade=low&purity_band=unknown&protocol=http", nil, true)
	var list nodeListPageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || list.Total != 2 {
		t.Fatalf("quality filter failed: %s", response.Body.String())
	}
	if response := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?risk_grade=made-up", nil, true); response.Code != http.StatusBadRequest {
		t.Fatal("invalid quality filter accepted")
	}
	entries[1].SetEgressIP(netip.MustParseAddr("1.1.1.1"))
	response = doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+entries[1].Hash.Hex(), nil, true)
	var changed service.NodeSummary
	if err := json.Unmarshal(response.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Quality.Evidence != nil || changed.Quality.State != "unobserved" {
		t.Fatal("old exit evidence followed the node to a new IP")
	}
	status := doJSONRequest(t, srv, http.MethodGet, "/api/v1/quality/status", nil, true)
	if strings.Contains(status.Body.String(), "credential-must-not-be-public") {
		t.Fatal("provider credential metadata leaked")
	}
	var report inspection.Status
	if err := json.Unmarshal(status.Body.Bytes(), &report); err != nil || len(report.Sources) != 1 || report.Sources[0].UsedToday != 1 {
		t.Fatalf("incorrect provider usage: %s", status.Body.String())
	}
}
