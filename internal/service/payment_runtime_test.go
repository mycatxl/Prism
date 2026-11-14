package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/tron"
)

func patchPayments(t *testing.T, cp *ControlPlaneService, patch map[string]any) *config.RuntimeConfig {
	t.Helper()
	b, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cp.PatchRuntimeConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRuntimePaymentsPersistDisableAndMaskSecrets(t *testing.T) {
	h := newPatchHarness(t)
	h.cp.EnvCfg = &config.EnvConfig{LDCPID: "env-merchant", LDCKey: "env-secret", USDTAddress: testUSDTAddress}
	c := patchPayments(t, h.cp, map[string]any{"ldc_enabled": false, "ldc_key": "new-secret", "tron_deposit_enabled": false, "tron_api_key": "grid-secret"})
	public, err := json.Marshal(c.PublicView())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "new-secret") || strings.Contains(string(public), "grid-secret") {
		t.Fatal("public config contains secrets")
	}
	persisted, _, err := h.engine.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	persisted.SeedPaymentConfig(h.cp.EnvCfg)
	if persisted.LDCEnabled || persisted.TronDepositEnabled || persisted.LDCKey != "new-secret" {
		t.Fatal("restart restored environment settings over explicit runtime settings")
	}
	for _, patch := range []string{`{"ldc_rate":0}`, `{"tron_trx_ratio":-1}`, `{"tron_window_minutes":1441}`, `{"tron_address":"TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSD"}`, `{"ldc_gateway_url":"javascript:alert(1)"}`, `{"ldc_gateway_url":"https://user:secret@example.com"}`, `{"payment_config_initialized":false}`, `{"tron_api_key":null}`} {
		if _, err := h.cp.PatchRuntimeConfig([]byte(patch)); err == nil {
			t.Fatalf("accepted %s", patch)
		}
	}
}

func TestTRXAndUSDTSettleOriginalTermsAfterLiveRotation(t *testing.T) {
	h := newPatchHarness(t)
	cp := h.cp
	now := time.Now().UnixNano()
	patchPayments(t, cp, map[string]any{"tron_deposit_enabled": true, "tron_address": testUSDTAddress, "tron_trx_enabled": true, "tron_trx_ratio": 0.125, "tron_window_minutes": 7})
	user := createDepositUser(t, cp, "dual-chain-user")
	usdt, err := cp.SelfDeposit(user, 50_000_000, now, "usdt")
	if err != nil {
		t.Fatal(err)
	}
	trx, err := cp.SelfDeposit(user, 50_000_000, now, "trx")
	if err != nil {
		t.Fatal(err)
	}
	if usdt.AmountMicro != trx.AmountMicro || trx.CreditMicro != 6_250_000 || trx.ExpiresAtNs-now != int64(7*time.Minute) {
		t.Fatalf("wrong snapshotted TRX terms: %+v", trx)
	}
	patchPayments(t, cp, map[string]any{"tron_address": config.DefaultUSDTContract, "tron_trx_ratio": 0.9, "tron_api_key": "rotated-grid-key", "tron_deposit_enabled": false, "tron_trx_enabled": false})
	view, err := cp.SelfDepositIntent(user, trx.ID)
	if err != nil || view.Address != testUSDTAddress || view.Rate != 0.125 {
		t.Fatalf("order changed with gateway: %+v %v", view, err)
	}
	if _, err := cp.SelfDeposit(user, 50_000_000, now, "trx"); err == nil {
		t.Fatal("disabled gateway accepted new order")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("TRON-PRO-API-KEY") != "rotated-grid-key" {
			t.Error("runtime API key was not used")
		}
		if !strings.Contains(r.URL.Path, testUSDTAddress) {
			t.Error("listener abandoned original recipient")
		}
		if strings.HasSuffix(r.URL.Path, "/trc20") {
			fmt.Fprintf(w, `{"data":[{"transaction_id":"usdt-payment","block_timestamp":%d,"to":%q,"type":"Transfer","value":%q,"token_info":{"address":%q,"decimals":6}}]}`, now/1e6+1, testUSDTAddress, fmt.Sprint(usdt.AmountMicro), config.DefaultUSDTContract)
		} else {
			fmt.Fprintf(w, `{"data":[{"txID":"trx-payment","block_timestamp":%d,"ret":[{"contractRet":"SUCCESS"}],"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"to_address":%q,"amount":%d}}}]}}]}`, now/1e6+1, testUSDTAddress, trx.AmountMicro)
		}
	}))
	defer srv.Close()
	cp.Tron = tron.NewClient(srv.URL, "old-key", config.DefaultUSDTContract)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := cp.ScanDeposits(context.Background(), now+int64(time.Second)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	balance, err := cp.Engine.GetBalance(user)
	if err != nil || balance != usdt.CreditMicro+trx.CreditMicro {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	ledger, err := cp.Engine.ListBalanceLedger(user, 20)
	if err != nil || len(ledger) != 2 {
		t.Fatalf("ledger length=%d error=%v", len(ledger), err)
	}
}

func TestLDCCallbackUsesOriginalRateAndKeyAfterDisable(t *testing.T) {
	h := newPatchHarness(t)
	cp := h.cp
	patchPayments(t, cp, map[string]any{"ldc_enabled": true, "ldc_pid": "merchant", "ldc_key": "original-secret", "ldc_rate": 0.25})
	user := createDepositUser(t, cp, "runtime-ldc")
	_, id, err := cp.CreateLDCDeposit(user, 12.50, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	patchPayments(t, cp, map[string]any{"ldc_enabled": false, "ldc_key": "rotated-secret", "ldc_rate": 10})
	params := map[string]string{"pid": "merchant", "trade_status": "TRADE_SUCCESS", "out_trade_no": id, "trade_no": "provider-transaction", "money": "12.50"}
	if err := cp.HandleLDCCallback(signedLDCCallback("rotated-secret", params), 1); err == nil {
		t.Fatal("accepted replacement key for old order")
	}
	for range 2 {
		if err := cp.HandleLDCCallback(signedLDCCallback("original-secret", params), 1); err != nil {
			t.Fatal(err)
		}
	}
	balance, err := cp.Engine.GetBalance(user)
	if err != nil || balance != 3_125_000 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	d, err := cp.Engine.GetLDCDeposit(id)
	if err != nil || d.Status != model.LDCDepositStatusPaid {
		t.Fatal("order did not settle")
	}
	params["money"] = "12.5000001"
	if err := cp.HandleLDCCallback(signedLDCCallback("original-secret", params), 2); err == nil {
		t.Fatal("rounded inexact signed amount")
	}
}

func TestDepositScannerCanEnableAfterStartup(t *testing.T) {
	h := newPatchHarness(t)
	cp := h.cp
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A poll cadence must not cancel a healthy provider response that takes
		// longer than one tick, including time spent preparing the scan in SQLite.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		select {
		case called <- struct{}{}:
		default:
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()
	cp.Tron = tron.NewClient(srv.URL, "", config.DefaultUSDTContract)
	stop := StartDepositScanner(cp, 10*time.Millisecond, t.Logf)
	defer stop()
	patchPayments(t, cp, map[string]any{"tron_deposit_enabled": true, "tron_address": testUSDTAddress})
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("scanner did not activate after runtime enable")
	}
}

func TestDepositScannerStopCancelsProviderRequest(t *testing.T) {
	h := newPatchHarness(t)
	cp := h.cp
	patchPayments(t, cp, map[string]any{"tron_deposit_enabled": true, "tron_address": testUSDTAddress})
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		select {
		case canceled <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()
	cp.Tron = tron.NewClient(srv.URL, "", config.DefaultUSDTContract)
	stop := StartDepositScanner(cp, 10*time.Millisecond, t.Logf)
	defer stop()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scanner did not reach the provider")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("scanner stop did not cancel the in-flight request")
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not observe request cancellation")
	}
}
