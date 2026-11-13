package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"prism/internal/export"
	"prism/internal/node"
	"prism/internal/nodeprivacy"
	"prism/internal/service"
	"prism/internal/subscription"
)

func TestTransportWarningReachesHTTPListDetailAndAnalysisExports(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	policy, _ := node.NewPrivacyPolicy(nodeprivacy.Config{})
	cp.Pool.SetPrivacyPolicy(policy)
	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "fixture", "", true, false)
	cp.SubMgr.Register(sub)
	raw := `{"type":"http","tls":{"enabled":true,"insecure":true}}`
	addNodeForNodeListTest(t, cp, sub, raw, "203.0.113.10")
	h := node.HashFromRawOptions([]byte(raw))
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/" + h.Hex()} {
		rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TLS_VERIFICATION_DISABLED") {
			t.Fatal("HTTP surface hid unverified TLS", path, rec.Code)
		}
	}
	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/"+h.Hex(), nil, true)
	var summary service.NodeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	item, ok := cp.ExportItem(summary)
	if !ok {
		t.Fatal("warning node omitted from export")
	}
	for _, include := range []bool{false, true} {
		for _, format := range []string{export.FormatJSON, export.FormatCSV} {
			body, _, _, err := export.Export([]export.Item{item}, format, export.Options{IncludeIntel: include})
			if err != nil || !strings.Contains(string(body), "TLS_VERIFICATION_DISABLED") || !strings.Contains(string(body), "transport_warnings") {
				t.Fatal("analysis export hid transport warning", format, include, err)
			}
		}
	}
}
