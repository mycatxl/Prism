package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/tron"
)

func TestDepositScanRetainsCursorExceptExplicitInvalidFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reset  bool
	}{
		{"server error", 503, `{"error":"server unavailable"}`, false},
		{"rate limit", 429, `{"error":"rate limited"}`, false},
		{"unrelated bad request", 400, `{"error":"invalid address","fingerprint":"saved-page"}`, false},
		{"invalid response", 200, `{`, false},
		{"network interruption", 0, "", false},
		{"deadline", -1, "", false},
		{"invalid fingerprint", 400, `{"error":"invalid fingerprint"}`, true},
		{"expired fingerprint", 410, `{"error":"fingerprint has expired"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, now := newDepositTestService(t)
			const since, until = int64(1000), int64(2000)
			if err := cp.Engine.SaveChainScan(depositCursorName, since, until, "saved-page"); err != nil {
				t.Fatal(err)
			}
			requests := make(chan string, 2)
			var fail atomic.Bool
			fail.Store(true)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.URL.Query().Get("fingerprint")
				if r.URL.Query().Get("min_timestamp") != "1000" || r.URL.Query().Get("max_timestamp") != "2000" {
					t.Error("lost finite scan bounds")
				}
				if !fail.Load() {
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
				if tc.status == 0 {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				if tc.status == -1 {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cp.Tron = tron.NewClient(srv.URL, "", config.DefaultUSDTContract)
			target := model.DepositTarget{Currency: "usdt", Address: testUSDTAddress, Contract: config.DefaultUSDTContract}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := cp.scanDepositTarget(ctx, now, cp.paymentConfig(), target)
			cancel()
			if err == nil {
				t.Fatal("provider failure was ignored")
			}
			if got := <-requests; got != "saved-page" {
				t.Fatalf("first fingerprint=%q", got)
			}
			lo, hi, fingerprint, err := cp.Engine.ChainScan(depositCursorName)
			want := "saved-page"
			if tc.reset {
				want = ""
			}
			if err != nil || lo != since || hi != until || fingerprint != want {
				t.Fatalf("saved scan=%d,%d,%q err=%v", lo, hi, fingerprint, err)
			}
			fail.Store(false)
			if _, err := cp.scanDepositTarget(context.Background(), now, cp.paymentConfig(), target); err != nil {
				t.Fatal(err)
			}
			if got := <-requests; got != want {
				t.Fatalf("retry fingerprint=%q want=%q", got, want)
			}
		})
	}
}

func TestLDCSignedCallbackRequiresExactMoney(t *testing.T) {
	cp, user, now := newLDCTestService(t)
	_, id, err := cp.CreateLDCDeposit(user, 12.5, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, money := range []string{"", " ", "12.49", "12.5000001", "1.25e1", "-12.5", strings.Repeat("1", 1000)} {
		params := map[string]string{"out_trade_no": id, "trade_no": "tx", "trade_status": "TRADE_SUCCESS", "money": money}
		if err := cp.HandleLDCCallback(signedLDCCallback(testLDCKey, params), now+1); err == nil {
			t.Fatalf("accepted money=%q", money)
		}
	}
	deposit, err := cp.Engine.GetLDCDeposit(id)
	if err != nil || deposit.Status != model.LDCDepositStatusPending {
		t.Fatalf("deposit=%+v err=%v", deposit, err)
	}
	if balance, err := cp.Engine.GetBalance(user); err != nil || balance != 0 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	if ledger, err := cp.Engine.ListBalanceLedger(user, 20); err != nil || len(ledger) != 0 {
		t.Fatalf("ledger=%+v err=%v", ledger, err)
	}
}

func TestLDCDecimalPrecisionAndMaximum(t *testing.T) {
	for _, raw := range []string{"12.5000000001", "1000000.0000000001", "-1", "1e6", "NaN", "0", "0.001", strings.Repeat("0", 1000) + "1"} {
		if _, err := normalizeLDCPointsDecimal(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for raw, want := range map[string]int64{"0.01": 10_000, "12.50": 12_500_000, "1000000": 1_000_000_000_000} {
		if got, err := normalizeLDCPointsDecimal(raw); err != nil || got != want {
			t.Fatalf("%q=%d err=%v", raw, got, err)
		}
	}
}
