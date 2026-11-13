package tron

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRound3IncompleteNativeRowsFailWithoutScanProgress(t *testing.T) {
	valid := fmt.Sprintf(`{"txID":"tx","block_timestamp":1000,"ret":[{"contractRet":"SUCCESS"}],"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"to_address":%q,"amount":1000001}}}]}}`, testAddress)
	for _, replacement := range []struct{ old, new string }{
		{`"contractRet":"SUCCESS"`, `"unused":"SUCCESS"`},
		{`"type":"TransferContract"`, `"unused":"TransferContract"`},
		{`"amount":1000001`, `"unused":1000001`},
		{`"amount":1000001`, `"amount":null`},
		{`"amount":1000001`, `"amount":1.1`},
		{`"amount":1000001`, `"amount":-1`},
		{`"amount":1000001`, `"amount":9223372036854775808`},
		{`"txID":"tx"`, `"txID":""`},
		{`"block_timestamp":1000`, `"block_timestamp":0`},
	} {
		row := strings.Replace(valid, replacement.old, replacement.new, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, `{"data":[%s]}`, row) }))
		page, err := NewClient(server.URL, "", "").TRXTransfersSince(context.Background(), testAddress, 0, 200)
		server.Close()
		if err == nil || page.NewestRawMs != 0 || len(page.Transfers) != 0 {
			t.Fatalf("accepted incomplete native row %s: %+v %v", replacement.new, page, err)
		}
	}
}

func TestRound3TronRejectsOversizedAndRedirectedResponses(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		var followed bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { followed = true; fmt.Fprint(w, `{"data":[]}`) }))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, target.URL, http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(strings.Repeat(" ", (8<<20)+1)))
		}))
		_, err := NewClient(server.URL, "private-api-key", testContract).TransfersSince(context.Background(), testAddress, 0, 200)
		server.Close()
		target.Close()
		if err == nil || followed {
			t.Fatalf("redirect=%v error=%v followed=%v", redirect, err, followed)
		}
	}
}
