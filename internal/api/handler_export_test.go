package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/service"
	"prism/internal/state"
	"prism/internal/subscription"
	"prism/internal/topology"
)

// Export API tests (WP11 §4). They drive the handlers through a real HTTP
// server backed by a real state engine, and they never touch the network: the
// node documents come from the subscription parser fixtures Prism already
// ships.

const exportTestAdminToken = "export-test-admin-token"

// newExportTestControlPlane builds a minimal but real control plane: a state
// engine on a temp directory, a node pool and a subscription manager.
func newExportTestControlPlane(t *testing.T) *service.ControlPlaneService {
	t.Helper()
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(root, "state"),
		filepath.Join(root, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	sub := subscription.NewSubscription("sub-export", "sub-export", "http://127.0.0.1/sub-export", true, false)
	subMgr.Register(sub)
	addExportTestNode(t, pool, sub, "ss-tokyo",
		`{"type":"shadowsocks","tag":"ss-tokyo","server":"198.51.100.10","server_port":8388,"method":"aes-256-gcm","password":"ss-secret-credential"}`)
	addExportTestNode(t, pool, sub, "vmess-ws",
		`{"type":"vmess","tag":"vmess-ws","server":"198.51.100.11","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","alter_id":0,"security":"auto"}`)

	return &service.ControlPlaneService{
		Engine: engine,
		Pool:   pool,
		SubMgr: subMgr,
		// The platform defaults are part of the fixture because creating a
		// platform through the API validates them: without them the platform
		// endpoints answer 500 and no test can scope an export to a platform.
		EnvCfg: &config.EnvConfig{
			ProxyPort:                                       2260,
			DefaultPlatformStickyTTL:                        30 * time.Minute,
			DefaultPlatformRegexFilters:                     []string{},
			DefaultPlatformRegionFilters:                    []string{},
			DefaultPlatformReverseProxyMissAction:           "TREAT_AS_EMPTY",
			DefaultPlatformReverseProxyEmptyAccountBehavior: "ACCOUNT_HEADER_RULE",
			DefaultPlatformReverseProxyFixedAccountHeader:   "Authorization",
			DefaultPlatformAllocationPolicy:                 "BALANCED",
		},
	}
}

// addExportTestNode registers one node document in the pool and records the tag
// on the subscription side, mirroring what a refresh does.
func addExportTestNode(t *testing.T, pool *topology.GlobalNodePool, sub *subscription.Subscription, tag, document string) {
	t.Helper()
	raw := json.RawMessage(document)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{tag}})
	pool.AddNodeFromSub(hash, raw, sub.ID)
}

func newExportTestServer(t *testing.T) (*service.ControlPlaneService, *httptest.Server) {
	t.Helper()
	cp := newExportTestControlPlane(t)
	srv := NewServer(0, exportTestAdminToken, service.SystemInfo{}, nil, cp.EnvCfg, cp, 1<<20, nil, nil, nil)
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return cp, server
}

func doExportAuthedRequest(t *testing.T, method, url string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+exportTestAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// exportContentType mirrors export.ContentType for the response assertions.
func exportContentType(format string) string {
	switch format {
	case "singbox", "json":
		return "application/json; charset=utf-8"
	case "mihomo":
		return "text/yaml; charset=utf-8"
	case "csv":
		return "text/csv; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// TestExportNodesEndpointServesEveryFormat checks §4.1 end to end.
func TestExportNodesEndpointServesEveryFormat(t *testing.T) {
	_, server := newExportTestServer(t)

	for _, format := range []string{"singbox", "mihomo", "v2rayn", "uri", "csv", "json"} {
		resp := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/nodes/export?format="+format, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", format, resp.StatusCode, body)
		}
		if !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s: content-disposition = %q", format, resp.Header.Get("Content-Disposition"))
		}
		if resp.Header.Get("X-Prism-Export-Exported") == "" {
			t.Fatalf("%s: exported header missing", format)
		}
		if resp.Header.Get("X-Prism-Export-Skipped") == "" {
			t.Fatalf("%s: skipped header missing", format)
		}
		want := strings.Split(exportContentType(format), ";")[0]
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, want) {
			t.Fatalf("%s: content type = %q", format, ct)
		}
	}
}

// TestExportNodesRejectsUnknownFormatAndFilter checks the 400 paths.
func TestExportNodesRejectsUnknownFormatAndFilter(t *testing.T) {
	_, server := newExportTestServer(t)

	cases := []string{
		"/api/v1/nodes/export",
		"/api/v1/nodes/export?format=surge",
		"/api/v1/nodes/export?format=csv&ip_type=nonsense",
		"/api/v1/nodes/export?format=csv&purity_band=great",
		"/api/v1/nodes/export?format=csv&limit=999999",
		"/api/v1/nodes/export?format=csv&limit=-1",
		"/api/v1/nodes/export?format=csv&offset=-1",
		"/api/v1/nodes/export?format=csv&healthy_only=maybe",
		"/api/v1/nodes/export?format=csv&probed_since=not-a-time",
	}
	for _, target := range cases {
		resp := doExportAuthedRequest(t, http.MethodGet, server.URL+target, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d (%s)", target, resp.StatusCode, body)
		}
		var envelope ErrorResponse
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("%s: decode error body: %v", target, err)
		}
		if envelope.Error.Code != "INVALID_ARGUMENT" || envelope.Error.Message == "" {
			t.Fatalf("%s: error envelope = %+v", target, envelope)
		}
	}
}

// TestExportNodesRequiresAdminToken checks rule R7.
func TestExportNodesRequiresAdminToken(t *testing.T) {
	_, server := newExportTestServer(t)
	resp, err := http.Get(server.URL + "/api/v1/nodes/export?format=csv")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestExportNodesOverLimitIsBoundedAndReported checks the input bound: the
// truncation is visible in the exported/skipped/truncated headers instead of
// being silent.
func TestExportNodesOverLimitIsBoundedAndReported(t *testing.T) {
	_, server := newExportTestServer(t)
	resp := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/nodes/export?format=csv&limit=1", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s)", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Prism-Export-Exported") != "1" {
		t.Fatalf("exported = %q", resp.Header.Get("X-Prism-Export-Exported"))
	}
	if resp.Header.Get("X-Prism-Export-Truncated") != "1" {
		t.Fatalf("truncated = %q (body %s)", resp.Header.Get("X-Prism-Export-Truncated"), body)
	}
}
