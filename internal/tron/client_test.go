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
	page, err := client.TransfersSince(context.Background(), testAddress, 1700000000000, 200)
	if err != nil {
		t.Fatalf("TransfersSince: %v", err)
	}
	got := page.Transfers
	// The raw watermark is the last row TronGrid returned, not the last row
	// this client kept: that Approval event is on the page, so the cursor has to
	// clear it even though nothing was credited from it.
	if page.NewestRawMs != 1700000004000 {
		t.Fatalf("raw watermark: got %d, want 1700000004000", page.NewestRawMs)
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
	for _, fragment := range []string{
		"only_confirmed=true",
		"min_timestamp=1700000000000",
		"contract_address=" + testContract,
		// TronGrid defaults to block_timestamp,desc; the watermark is the block
		// time of the newest row, so a descending page makes it move backwards.
		"order_by=block_timestamp%2Casc",
	} {
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

// TestTransfersSinceReturnsOldestFirst pins the contract the deposit scanner
// relies on: the newest transfer is the LAST element, so its block time is what
// the watermark advances to. TronGrid answers newest-first by default, which is
// why the client sorts.
func TestTransfersSinceReturnsOldestFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"transaction_id":"tx-newest","block_timestamp":1700000003000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer","value":"30000001",
				 "token_info":{"address":"` + testContract + `","decimals":6}},
				{"transaction_id":"tx-middle","block_timestamp":1700000002000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer","value":"20000001",
				 "token_info":{"address":"` + testContract + `","decimals":6}},
				{"transaction_id":"tx-oldest","block_timestamp":1700000001000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer","value":"10000001",
				 "token_info":{"address":"` + testContract + `","decimals":6}}
			]
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", testContract)
	page, err := client.TransfersSince(context.Background(), testAddress, 1700000000000, 200)
	if err != nil {
		t.Fatalf("TransfersSince: %v", err)
	}
	got := page.Transfers
	want := []string{"tx-oldest", "tx-middle", "tx-newest"}
	if len(got) != len(want) {
		t.Fatalf("transfers: got %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].TxID != id {
			t.Fatalf("transfer %d: got %q, want %q (the result must be oldest first)", i, got[i].TxID, id)
		}
	}
	if newest := got[len(got)-1].BlockTSMs; newest != 1700000003000 {
		t.Fatalf("newest block time: got %d, want 1700000003000", newest)
	}
}

// A page whose rows all belong to somebody else must still move the watermark.
//
// This endpoint reports the account's transfers in both directions, so a wallet
// that also sends USDT fills its own page with outgoing rows. A watermark that
// only advanced past rows addressed to us would stand still on such a page for
// ever, and every later deposit would stay invisible.
func TestTransfersSinceAdvancesPastAPageOfForeignTransfers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"transaction_id":"tx-out-1","block_timestamp":1700000001000,
				 "from":"` + testAddress + `","to":"TSOMEWHERE",
				 "type":"Transfer","value":"1000000",
				 "token_info":{"address":"` + testContract + `","decimals":6}},
				{"transaction_id":"tx-out-2","block_timestamp":1700000002000,
				 "from":"` + testAddress + `","to":"TSOMEWHERE",
				 "type":"Transfer","value":"2000000",
				 "token_info":{"address":"` + testContract + `","decimals":6}}
			]
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", testContract)
	page, err := client.TransfersSince(context.Background(), testAddress, 1700000000000, 200)
	if err != nil {
		t.Fatalf("TransfersSince: %v", err)
	}
	if len(page.Transfers) != 0 {
		t.Fatalf("transfers: got %d, want 0 (both rows are outgoing)", len(page.Transfers))
	}
	if page.NewestRawMs != 1700000002000 {
		t.Fatalf("raw watermark: got %d, want 1700000002000", page.NewestRawMs)
	}
}

// The amount is scaled by the token's own decimals, never assumed to be USDT's.
func TestTransfersSinceScalesAmountsByTokenDecimals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"transaction_id":"tx-18-decimals","block_timestamp":1700000001000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer",
				 "value":"100000000000000000000",
				 "token_info":{"address":"` + testContract + `","decimals":18}},
				{"transaction_id":"tx-sub-micro","block_timestamp":1700000002000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer",
				 "value":"123456789",
				 "token_info":{"address":"` + testContract + `","decimals":9}},
				{"transaction_id":"tx-whole","block_timestamp":1700000003000,
				 "from":"TFROM","to":"` + testAddress + `","type":"Transfer",
				 "value":"25000000",
				 "token_info":{"address":"` + testContract + `","decimals":6}}
			]
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", testContract)
	page, err := client.TransfersSince(context.Background(), testAddress, 1700000000000, 200)
	if err != nil {
		t.Fatalf("TransfersSince: %v", err)
	}
	if len(page.Transfers) != 2 {
		t.Fatalf("transfers: got %d, want 2 (the sub-micro row cannot be credited exactly): %+v",
			len(page.Transfers), page.Transfers)
	}
	if got := page.Transfers[0]; got.TxID != "tx-18-decimals" || got.AmountMicro != 100_000_000 {
		t.Fatalf("18-decimal amount: got %q %d, want tx-18-decimals 100000000", got.TxID, got.AmountMicro)
	}
	if got := page.Transfers[1]; got.TxID != "tx-whole" || got.AmountMicro != 25_000_000 {
		t.Fatalf("6-decimal amount: got %q %d, want tx-whole 25000000", got.TxID, got.AmountMicro)
	}
	// The dropped row still belongs to the page: the watermark must clear it.
	if page.NewestRawMs != 1700000003000 {
		t.Fatalf("raw watermark: got %d, want 1700000003000", page.NewestRawMs)
	}
}
