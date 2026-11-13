package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNativeTRXFiltersAndNormalizesHexAddresses(t *testing.T) {
	address, ok := NormalizeAddress("410000000000000000000000000000000000000000")
	if !ok || !ValidAddress(address) {
		t.Fatal("hex conversion failed")
	}
	var rows []map[string]any
	for _, tc := range []struct {
		id, status, kind, to string
		amount               any
	}{
		{"valid", "SUCCESS", "TransferContract", "410000000000000000000000000000000000000000", 50_000_123},
		{"failed", "REVERT", "TransferContract", address, 50_000_123},
		{"token", "SUCCESS", "TriggerSmartContract", address, 50_000_123},
		{"foreign", "SUCCESS", "TransferContract", testAddress, 50_000_123},
	} {
		rows = append(rows, map[string]any{"txID": tc.id, "block_timestamp": 1000, "ret": []any{map[string]any{"contractRet": tc.status}}, "raw_data": map[string]any{"contract": []any{map[string]any{"type": tc.kind, "parameter": map[string]any{"value": map[string]any{"amount": tc.amount, "to_address": tc.to}}}}}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/transactions") || r.URL.Query().Get("only_confirmed") != "true" || r.URL.Query().Get("only_to") != "true" {
			t.Error("unsafe native query")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	defer srv.Close()
	p, err := NewClient(srv.URL, "", testContract).TRXTransfersSince(context.Background(), address, 0, 200)
	if err != nil || len(p.Transfers) != 1 || p.Transfers[0].TxID != "valid" || p.Transfers[0].AmountMicro != 50_000_123 {
		t.Fatalf("page=%+v err=%v", p, err)
	}
}

func TestLargeSameTimestampHistoryResumesBoundedBatches(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		i, _ := strconv.Atoi(r.URL.Query().Get("fingerprint"))
		next := ""
		if i < 11 {
			next = strconv.Itoa(i + 1)
		}
		if r.URL.Query().Get("max_timestamp") != "2000" {
			t.Error("lost upper bound")
		}
		fmt.Fprintf(w, `{"data":[{"transaction_id":%q,"block_timestamp":1000,"to":%q,"value":"1000001","token_info":{"address":%q,"decimals":6}}],"meta":{"fingerprint":%q}}`, fmt.Sprint(i), testAddress, testContract, next)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", testContract)
	fingerprint := ""
	total := 0
	for range 3 {
		p, err := c.WithPagination(fingerprint, 2000).TransfersSince(context.Background(), testAddress, 1000, 1)
		if err != nil {
			t.Fatal(err)
		}
		total += len(p.Transfers)
		fingerprint = p.NextFingerprint
	}
	if total != 12 || requests != 12 || fingerprint != "" {
		t.Fatalf("total=%d requests=%d continuation=%q", total, requests, fingerprint)
	}
}

func TestTransferPaginationRetainsSameTimestampRows(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fingerprint := "next-page"
				id := "first"
				if r.URL.Query().Get("fingerprint") == "next-page" {
					fingerprint = ""
					id = "second"
				}
				if native {
					fmt.Fprintf(w, `{"data":[{"txID":%q,"block_timestamp":1000,"ret":[{"contractRet":"SUCCESS"}],"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"to_address":%q,"amount":1000001}}}]}}],"meta":{"fingerprint":%q}}`, id, testAddress, fingerprint)
				} else {
					fmt.Fprintf(w, `{"data":[{"transaction_id":%q,"block_timestamp":1000,"to":%q,"value":"1000001","token_info":{"address":%q,"decimals":6}}],"meta":{"fingerprint":%q}}`, id, testAddress, testContract, fingerprint)
				}
			}))
			defer srv.Close()
			client := NewClient(srv.URL, "", testContract)
			var p TransferPage
			var err error
			if native {
				p, err = client.TRXTransfersSince(context.Background(), testAddress, 1000, 1)
			} else {
				p, err = client.TransfersSince(context.Background(), testAddress, 1000, 1)
			}
			if err != nil || calls != 2 || len(p.Transfers) != 2 || p.NewestRawMs != 1000 {
				t.Fatalf("page=%+v calls=%d err=%v", p, calls, err)
			}
		})
	}
}

func TestTRC20AddressComparisonIsCaseSensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"transaction_id":"wrong-case","block_timestamp":1000,"to":%q,"value":"1000001","token_info":{"address":%q,"decimals":6}}]}`, strings.ToLower(testAddress), testContract)
	}))
	defer srv.Close()
	p, err := NewClient(srv.URL, "", testContract).TransfersSince(context.Background(), testAddress, 0, 200)
	if err != nil || len(p.Transfers) != 0 {
		t.Fatal("accepted different Base58 address")
	}
}
