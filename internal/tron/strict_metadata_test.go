package tron

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTRC20RequiresExactContractAndExplicitSixDecimals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token any
		value string
		valid bool
	}{
		{"valid", map[string]any{"address": testContract, "decimals": 6}, "1000001", true},
		{"missing metadata", nil, "1000001", false},
		{"missing contract", map[string]any{"decimals": 6}, "1000001", false},
		{"empty contract", map[string]any{"address": "", "decimals": 6}, "1000001", false},
		{"foreign contract", map[string]any{"address": testAddress, "decimals": 6}, "1000001", false},
		{"case changed contract", map[string]any{"address": strings.ToLower(testContract), "decimals": 6}, "1000001", false},
		{"missing decimals", map[string]any{"address": testContract}, "1000001", false},
		{"null decimals", map[string]any{"address": testContract, "decimals": nil}, "1000001", false},
		{"zero decimals", map[string]any{"address": testContract, "decimals": 0}, "1", false},
		{"overflow", map[string]any{"address": testContract, "decimals": 6}, "9223372036854775808", false},
		{"fractional raw", map[string]any{"address": testContract, "decimals": 6}, "1000001.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
					"transaction_id": "tx", "block_timestamp": 1000, "to": testAddress, "value": tc.value, "token_info": tc.token,
				}}})
			}))
			defer srv.Close()
			page, err := NewClient(srv.URL, "", testContract).TransfersSince(context.Background(), testAddress, 0, 200)
			if strings.HasPrefix(tc.name, "missing") || tc.name == "empty contract" || tc.name == "null decimals" || tc.name == "overflow" || tc.name == "fractional raw" {
				if err == nil || len(page.Transfers) != 0 || page.NewestRawMs != 0 {
					t.Fatalf("incomplete metadata advanced scan: page=%+v error=%v", page, err)
				}
				return
			}
			if err != nil || (len(page.Transfers) == 1) != tc.valid || page.NewestRawMs != 1000 {
				t.Fatalf("page=%+v error=%v", page, err)
			}
		})
	}
}

func TestTransientLaterPageResumesAtFailedFingerprint(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("fingerprint") == "next" && calls == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		next := "next"
		if r.URL.Query().Get("fingerprint") == "next" {
			next = ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"transaction_id": next + "tx", "block_timestamp": 1000, "to": testAddress, "value": "1000001",
			"token_info": map[string]any{"address": testContract, "decimals": 6},
		}}, "meta": map[string]any{"fingerprint": next}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", testContract)
	page, err := c.WithPagination("", 2000).TransfersSince(context.Background(), testAddress, 1000, 1)
	if err != nil || len(page.Transfers) != 1 || page.NextFingerprint != "next" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = c.WithPagination(page.NextFingerprint, 2000).TransfersSince(context.Background(), testAddress, 1000, 1)
	if err != nil || len(page.Transfers) != 1 || page.NextFingerprint != "" || calls != 3 {
		t.Fatalf("resumed page=%+v err=%v calls=%d", page, err, calls)
	}
}

func TestEmptyPageWithContinuationDoesNotCompleteHistory(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("fingerprint") == "" {
			_, _ = w.Write([]byte(`{"data":[],"meta":{"fingerprint":"next"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"transaction_id": "later", "block_timestamp": 1000, "to": testAddress, "value": "1000001", "token_info": map[string]any{"address": testContract, "decimals": 6}}}})
	}))
	defer srv.Close()
	page, err := NewClient(srv.URL, "", testContract).TransfersSince(context.Background(), testAddress, 0, 200)
	if err != nil || len(page.Transfers) != 1 || calls != 2 {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
}
