package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
const testAddress = "TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"

// TronGrid answers with a wrapper around decoded Transfer events. The client has
// to keep the ones addressed to us and in our contract, and drop the rest.
func TestTransfersSinceFiltersToAddressAndContract(t *testing.T) {
	var gotPath, gotQuery, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotKey = r.Header.Get("TRON-PRO-API-KEY")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"transaction_id":"tx-ok","block_timestamp":1700000001000,
				 "from":"TFROM1","to":"` + testAddress + `","type":"Transfer","value":"10000001",
				 "token_info":{"address":"` + testContract + `","decimals":6}},
				{"transaction_id":"tx-other-token","block_timestamp":1700000002000,
				 "from":"TFROM2","to":"` + testAddress + `","type":"Transfer","value":"5000000",
				 "token_info":{"address":"TSOMETHINGELSE","decimals":6}},
				{"transaction_id":"tx-elsewhere","block_timestamp":1700000003000,
				 "from":"TFROM3","to":"TSOMEONEELSE","type":"Transfer","value":"7000000",
				 "token_info":{"address":"` + testContract + `","decimals":6}},
				{"transaction_id":"tx-approval","block_timestamp":1700000004000,
				 "from":"TFROM4","to":"` + testAddress + `","type":"Approval","value":"9000000",
				 "token_info":{"address":"` + testContract + `","decimals":6}}
			]
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", testContract)
	got, err := client.TransfersSince(context.Background(), testAddress, 1700000000000, 200)
	if err != nil {
		t.Fatalf("TransfersSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("transfers: got %d, want 1 (%+v)", len(got), got)
	}
	if got[0].TxID != "tx-ok" {
		t.Fatalf("tx id: got %q, want tx-ok", got[0].TxID)
	}
	if got[0].AmountMicro != 10_000_001 {
		t.Fatalf("amount: got %d, want 10000001", got[0].AmountMicro)
	}
	if got[0].BlockTSMs != 1700000001000 {
		t.Fatalf("block time: got %d, want 1700000001000", got[0].BlockTSMs)
	}

	// The request must ask for confirmed transfers only: crediting from head
	// state would let a reorganisation take the money back.
	if want := "/v1/accounts/" + testAddress + "/transactions/trc20"; gotPath != want {
		t.Fatalf("path: got %q, want %q", gotPath, want)
	}
	for _, fragment := range []string{"only_confirmed=true", "min_timestamp=1700000000000", "contract_address=" + testContract} {
		if !containsFragment(gotQuery, fragment) {
			t.Fatalf("query %q is missing %q", gotQuery, fragment)
		}
	}
	if gotKey != "test-key" {
		t.Fatalf("api key header: got %q, want test-key", gotKey)
	}
}

func TestTransfersSinceRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"Error":"rate limited"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", testContract)
	if _, err := client.TransfersSince(context.Background(), testAddress, 0, 200); err == nil {
		t.Fatal("a non-200 answer must be an error, not an empty result")
	}
}

func TestTransfersSinceRequiresAddress(t *testing.T) {
	client := NewClient("https://example.invalid", "", testContract)
	if _, err := client.TransfersSince(context.Background(), "", 0, 200); err == nil {
		t.Fatal("an empty address must be refused")
	}
}

func containsFragment(query, fragment string) bool {
	for _, part := range strings.Split(query, "&") {
		if part == fragment {
			return true
		}
	}
	return false
}
