package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/subscription"
	"strings"
	"testing"
	"time"
)

func TestPurityReasonThroughListDetailQualityFilterAndExport(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)
	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "fixture", "", true, false)
	cp.SubMgr.Register(sub)
	raw := `{"type":"direct","fixture":"unrated"}`
	target := node.HashFromRawOptions([]byte(raw))
	ip := "203.0.113.40"
	addNodeForNodeListTest(t, cp, sub, raw, ip)
	addNodeForNodeListTest(t, cp, sub, `{"type":"direct","fixture":"other-unrated"}`, "203.0.113.41")
	for failure, reason := range map[string]string{"PROVIDER_LIMIT": "IPPURE_RATE_LIMITED", "PROVIDER_TIMEOUT": "IPPURE_TIMEOUT", "PROVIDER_AUTH": "IPPURE_AUTH_FAILED", "EGRESS_MISMATCH": "IPPURE_EGRESS_MISMATCH", "PROVIDER_RESPONSE": "IPPURE_INVALID_RESPONSE", "PROVIDER_UNAVAILABLE": "IPPURE_PROVIDER_UNAVAILABLE"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Now().UTC()
			err := svc.Store().UpsertEvidence(context.Background(), store.Evidence{IP: ip, Provider: "ippure", Status: store.StatusError, ErrorCode: failure, ObservedAtNs: now.UnixNano(), ValidUntilNs: now.Add(time.Hour).UnixNano()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Assess(context.Background(), netip.MustParseAddr(ip)); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/api/v1/nodes/" + target.Hex(), "/api/v1/nodes?purity_state=unrated&purity_reason=" + reason} {
				rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
				if rec.Code != 200 {
					t.Fatalf("status %d", rec.Code)
				}
				obj := decodeJSONMap(t, rec)
				if items, ok := obj["items"]; ok {
					list := items.([]any)
					if len(list) != 1 {
						t.Fatalf("reason filter returned %d nodes", len(list))
					}
					obj = list[0].(map[string]any)
					if obj["node_hash"] != target.Hex() {
						t.Fatal("reason filter selected the wrong node")
					}
				}
				v := intelObject(t, obj)
				if v["purity_score"] != nil {
					t.Fatal("unrated score is not null")
				}
				fact := v["facts"].(map[string]any)["purity"].(map[string]any)
				if fact["cause"] != reason || fact["provider"] != "ippure" || fact["error_code"] != failure || fact["explanation"] == "" || fact["action"] == "" {
					t.Fatalf("incomplete diagnostic: %+v", fact)
				}
			}
			q, err := cp.QualityIP(context.Background(), ip)
			if err != nil {
				t.Fatal(err)
			}
			if q.Assessment == nil || q.Assessment.PurityScore != nil || q.Assessment.Facts["purity"].Cause != reason {
				t.Fatal("quality lost reason")
			}
			rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes/export?format=json&include_intel=true&purity_reason="+reason, nil, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("export status %d", rec.Code)
			}
			var body struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Items) != 1 || body.Items[0]["purity_score"] != nil || body.Items[0]["purity_reason"] != reason {
				t.Fatalf("export lost null/reason: %v", body.Items)
			}
			for _, query := range []string{
				"purity_reason=" + reason + "&purity_state=rated",
				"purity_reason=" + reason + "&purity_min=0",
				"purity_reason=IPPURE_NOT_A_REAL_REASON",
			} {
				response := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?"+query, nil, true)
				if response.Code != http.StatusOK || len(decodeJSONMap(t, response)["items"].([]any)) != 0 {
					t.Fatal("reason filter admitted a nonmatching or unscored node")
				}
			}
		})
	}
}

func TestPurityStorageFailureHasSafeReason(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc := wireIntelForTest(t, cp)
	if err := svc.Store().Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/quality/ip/8.8.8.8", "/api/v1/intel/ip/8.8.8.8"} {
		rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "EVIDENCE_READ_FAILED") || strings.Contains(body, "sql:") {
			t.Fatal("storage failure lost reason or leaked internals")
		}
	}
}
