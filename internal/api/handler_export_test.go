package api

import (
	"bytes"
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
		EnvCfg: &config.EnvConfig{ProxyPort: 2260},
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
	srv := NewServer(0, exportTestAdminToken, service.SystemInfo{}, nil, cp.EnvCfg, cp, 1<<20, nil, nil)
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

// TestExportProfileLifecycle checks §4.2 end to end, including the one-time
// token rule.
func TestExportProfileLifecycle(t *testing.T) {
	_, server := newExportTestServer(t)

	create := map[string]any{
		"name":          "residential",
		"format":        "singbox",
		"filter":        map[string]any{"purity_band": "excellent"},
		"name_template": "{name}",
		"enabled":       true,
	}
	raw, _ := json.Marshal(create)
	resp := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d (%s)", resp.StatusCode, body)
	}
	var created exportProfileResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" || created.URL == "" || !created.Enabled {
		t.Fatalf("create response = %+v", created)
	}
	if !strings.Contains(created.URL, "/sub/") {
		t.Fatalf("subscription url = %q", created.URL)
	}
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]
	if token == "" {
		t.Fatal("empty token in the subscription url")
	}
	if strings.Contains(string(body), ExportProfileTokenSHA256(token)) {
		t.Fatal("the create response leaked the token digest")
	}

	get := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/export-profiles/"+created.ID, nil)
	getBody, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d (%s)", get.StatusCode, getBody)
	}
	if strings.Contains(string(getBody), token) {
		t.Fatal("GET leaked the plaintext token")
	}
	if strings.Contains(string(getBody), ExportProfileTokenSHA256(token)) {
		t.Fatal("GET leaked the token digest")
	}
	var fetched exportProfileResponse
	if err := json.Unmarshal(getBody, &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.URL != "" {
		t.Fatalf("url must only be returned once, got %q", fetched.URL)
	}

	list := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/export-profiles", nil)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d (%s)", list.StatusCode, listBody)
	}
	if strings.Contains(string(listBody), token) {
		t.Fatal("list leaked the plaintext token")
	}

	rotate := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles/"+created.ID+"/actions/rotate-token", nil)
	rotateBody, _ := io.ReadAll(rotate.Body)
	rotate.Body.Close()
	if rotate.StatusCode != http.StatusOK {
		t.Fatalf("rotate: status %d (%s)", rotate.StatusCode, rotateBody)
	}
	var rotated exportProfileResponse
	if err := json.Unmarshal(rotateBody, &rotated); err != nil {
		t.Fatalf("decode rotate response: %v", err)
	}
	if rotated.URL == "" || rotated.URL == created.URL {
		t.Fatalf("rotate did not issue a new token: %q", rotated.URL)
	}

	patch, _ := json.Marshal(map[string]any{"enabled": false})
	patchResp := doExportAuthedRequest(t, http.MethodPatch, server.URL+"/api/v1/export-profiles/"+created.ID, bytes.NewReader(patch))
	patchBody, _ := io.ReadAll(patchResp.Body)
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch: status %d (%s)", patchResp.StatusCode, patchBody)
	}
	if !strings.Contains(string(patchBody), `"enabled":false`) {
		t.Fatalf("patch response = %s", patchBody)
	}

	del := doExportAuthedRequest(t, http.MethodDelete, server.URL+"/api/v1/export-profiles/"+created.ID, nil)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", del.StatusCode)
	}
	missing := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/export-profiles/"+created.ID, nil)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", missing.StatusCode)
	}
}

// TestExportProfileDuplicateNameConflicts checks the ErrConflict mapping.
func TestExportProfileDuplicateNameConflicts(t *testing.T) {
	_, server := newExportTestServer(t)
	raw, _ := json.Marshal(map[string]any{"name": "dup", "format": "csv"})
	first := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status %d", first.StatusCode)
	}
	second := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
	body, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second create: status %d (%s)", second.StatusCode, body)
	}
	if !strings.Contains(string(body), `"code":"CONFLICT"`) {
		t.Fatalf("conflict body = %s", body)
	}
}

// TestExportProfileValidation checks the request-body rules.
func TestExportProfileValidation(t *testing.T) {
	_, server := newExportTestServer(t)
	cases := []map[string]any{
		{"format": "csv"},
		{"name": ""},
		{"name": strings.Repeat("x", 129), "format": "csv"},
		{"name": "bad-format", "format": "surge"},
		{"name": "bad-filter", "format": "csv", "filter": map[string]any{"ip_type": "nope"}},
		{"name": "bad-platform", "format": "csv", "platform_id": "not-a-uuid"},
	}
	for _, payload := range cases {
		raw, _ := json.Marshal(payload)
		resp := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v: status %d (%s)", payload, resp.StatusCode, body)
		}
	}
}

// TestSubscriptionTokenFlow checks §4.3 end to end: an unknown token is a 404,
// a disabled profile is a 404, the access counter increments and the v2rayN
// refresh header is present.
func TestSubscriptionTokenFlow(t *testing.T) {
	_, server := newExportTestServer(t)

	raw, _ := json.Marshal(map[string]any{"name": "sub", "format": "v2rayn"})
	create := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
	createBody, _ := io.ReadAll(create.Body)
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d (%s)", create.StatusCode, createBody)
	}
	var created exportProfileResponse
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]

	missing, err := http.Get(server.URL + "/sub/definitely-not-a-token")
	if err != nil {
		t.Fatalf("get unknown token: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token status = %d", missing.StatusCode)
	}

	ok, err := http.Get(server.URL + "/sub/" + token)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("subscription status = %d", ok.StatusCode)
	}
	if ok.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", ok.Header.Get("Cache-Control"))
	}
	if ok.Header.Get("Profile-Update-Interval") != "12" {
		t.Fatalf("profile-update-interval = %q", ok.Header.Get("Profile-Update-Interval"))
	}

	fetched := fetchExportProfile(t, server, created.ID)
	if fetched.AccessCount != 1 {
		t.Fatalf("access_count = %d, want 1", fetched.AccessCount)
	}
	if fetched.LastAccessAtNs == 0 {
		t.Fatal("last_access_at_ns was not recorded")
	}

	disable, _ := json.Marshal(map[string]any{"enabled": false})
	patch := doExportAuthedRequest(t, http.MethodPatch, server.URL+"/api/v1/export-profiles/"+created.ID, bytes.NewReader(disable))
	patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("patch: status %d", patch.StatusCode)
	}
	disabled, err := http.Get(server.URL + "/sub/" + token)
	if err != nil {
		t.Fatalf("get disabled subscription: %v", err)
	}
	disabled.Body.Close()
	if disabled.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled profile status = %d", disabled.StatusCode)
	}
}

// TestSubscriptionMihomoContentDisposition checks the mihomo-specific header.
func TestSubscriptionMihomoContentDisposition(t *testing.T) {
	_, server := newExportTestServer(t)
	raw, _ := json.Marshal(map[string]any{"name": "clash", "format": "mihomo"})
	create := doExportAuthedRequest(t, http.MethodPost, server.URL+"/api/v1/export-profiles", bytes.NewReader(raw))
	createBody, _ := io.ReadAll(create.Body)
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d (%s)", create.StatusCode, createBody)
	}
	var created exportProfileResponse
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]

	resp, err := http.Get(server.URL + "/sub/" + token)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Disposition"); got != "inline; filename=prism.yaml" {
		t.Fatalf("content-disposition = %q", got)
	}
}

func fetchExportProfile(t *testing.T, server *httptest.Server, id string) exportProfileResponse {
	t.Helper()
	resp := doExportAuthedRequest(t, http.MethodGet, server.URL+"/api/v1/export-profiles/"+id, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get export profile: status %d (%s)", resp.StatusCode, body)
	}
	var profile exportProfileResponse
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatalf("decode export profile: %v", err)
	}
	return profile
}
