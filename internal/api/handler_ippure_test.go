package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"prism/internal/inspection"
	"prism/internal/node"
	"prism/internal/testutil"
)

func TestIPPureActionRequiresAdminUsesChosenNodeAndDoesNotPersist(t *testing.T) {
	for _, changedExit := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-exit", true: "changed-exit"}[changedExit], func(t *testing.T) {
			srv, cp, _ := newControlPlaneTestServer(t)
			raw := json.RawMessage(`{"type":"http","server":"127.0.0.1","server_port":9001}`)
			hash := node.HashFromRawOptions(raw)
			cp.Pool.AddNodeFromSub(hash, raw, "11111111-1111-1111-1111-111111111111")
			entry, ok := cp.Pool.GetEntry(hash)
			if !ok {
				t.Fatal("node missing")
			}
			outbound := testutil.NewNoopOutbound()
			entry.Outbound.Store(&outbound)
			entry.SetEgressIP(netip.MustParseAddr("8.8.8.8"))
			entry.FailureCount.Store(2)
			calls := 0
			cp.IPPure = inspection.NewIPPureCheckerWithFetcher(func(_ context.Context, selected adapter.Outbound) (inspection.IPPureResponse, error) {
				calls++
				if selected != outbound {
					t.Error("different outbound selected")
				}
				if changedExit {
					entry.SetEgressIP(netip.MustParseAddr("1.1.1.1"))
				}
				return inspection.IPPureResponse{StatusCode: 200, Body: []byte(`{"ip":"8.8.8.8","fraudScore":5,"isResidential":true,"isBroadcast":false}`)}, nil
			})
			path := "/api/v1/nodes/" + hash.Hex() + "/actions/review-ippure"
			if response := doJSONRequest(t, srv, http.MethodPost, path, nil, false); response.Code != 401 || calls != 0 {
				t.Fatal("anonymous review reached provider")
			}
			response := doJSONRequest(t, srv, http.MethodPost, path, nil, true)
			if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("review failed: %d %s", response.Code, response.Body.String())
			}
			var review inspection.IPPureReview
			if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil {
				t.Fatal(err)
			}
			if review.MatchesNodeIP == changedExit || review.ExpectedIP != "8.8.8.8" || review.Evidence.IP != "8.8.8.8" || *review.Evidence.RiskScore != 5 {
				t.Fatalf("incorrect exit binding: %+v", review)
			}
			if entry.FailureCount.Load() != 2 {
				t.Fatal("review changed node health")
			}
			records, err := cp.Engine.LoadQualityRecords(context.Background())
			if err != nil || len(records) != 0 {
				t.Fatal("manual IPPure result was persisted")
			}
			response = doJSONRequest(t, srv, http.MethodPost, path, nil, true)
			if response.Code != 429 || response.Header().Get("Retry-After") == "" || calls != 1 {
				t.Fatal("manual cooldown did not reach API")
			}
		})
	}
}
