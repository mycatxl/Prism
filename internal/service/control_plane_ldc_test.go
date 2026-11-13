package service

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"prism/internal/config"
	"prism/internal/model"
)

const (
	testLDCPID = "10001"
	testLDCKey = "ldc-test-secret"
)

func newLDCTestService(t *testing.T) (*ControlPlaneService, string, int64) {
	t.Helper()
	cp, _, _, nowNs := newTenantTestService(t)
	cp.EnvCfg = &config.EnvConfig{
		LDCPID:        testLDCPID,
		LDCKey:        testLDCKey,
		LDCGatewayURL: "https://payments.example/epay",
		LDCNotifyURL:  "https://prism.example/api/v1/public/ldc/notify",
		LDCReturnURL:  "https://prism.example/ui/me/deposit",
	}
	cp.InitializeLDC()
	user, err := cp.CreateAccount("ldc-service-user", "ldc-service-password-1234", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("create LDC user: %v", err)
	}
	return cp, user.ID, nowNs
}

func TestSelfLDCInfoFollowsClientConfiguration(t *testing.T) {
	cp := &ControlPlaneService{}
	if cp.SelfLDCInfo().Enabled {
		t.Fatal("LDC must be disabled without a client")
	}
	cp.EnvCfg = &config.EnvConfig{LDCPID: " pid ", LDCKey: " key "}
	cp.InitializeLDC()
	if !cp.SelfLDCInfo().Enabled {
		t.Fatal("LDC must be enabled when both credentials are configured")
	}
	cp.EnvCfg.LDCKey = ""
	cp.InitializeLDC()
	if cp.SelfLDCInfo().Enabled {
		t.Fatal("LDC must be disabled when either credential is missing")
	}
}

func TestCreateLDCDepositAndCallbackCreditExactlyOnce(t *testing.T) {
	cp, userID, nowNs := newLDCTestService(t)

	redirectURL, outTradeNo, err := cp.CreateLDCDeposit(userID, 12.50, nowNs)
	if err != nil {
		t.Fatalf("CreateLDCDeposit: %v", err)
	}
	if !strings.HasPrefix(outTradeNo, "ldc_") {
		t.Fatalf("out_trade_no = %q, want ldc_ prefix", outTradeNo)
	}
	parsed, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if got := parsed.Query().Get("out_trade_no"); got != outTradeNo {
		t.Fatalf("redirect out_trade_no = %q, want %q", got, outTradeNo)
	}
	if got := parsed.Query().Get("money"); got != "12.50" {
		t.Fatalf("redirect money = %q, want 12.50", got)
	}

	params := signedLDCCallback(testLDCKey, map[string]string{
		"pid":          testLDCPID,
		"trade_status": "TRADE_SUCCESS",
		"out_trade_no": outTradeNo,
		"trade_no":     "gateway-trade-123",
		"money":        "12.50",
		"sign_type":    "MD5",
	})
	if err := cp.HandleLDCCallback(params, nowNs+1); err != nil {
		t.Fatalf("HandleLDCCallback: %v", err)
	}
	if err := cp.HandleLDCCallback(params, nowNs+2); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}

	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 12_500_000 {
		t.Fatalf("balance = %d, want 12500000", balance)
	}
	ledger, err := cp.Engine.ListBalanceLedger(userID, 10)
	if err != nil {
		t.Fatalf("ListBalanceLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(ledger))
	}
	if ledger[0].Reason != model.BalanceReasonDeposit || ledger[0].RefID != outTradeNo {
		t.Fatalf("ledger entry = %+v", ledger[0])
	}
	if !strings.Contains(ledger[0].Note, "gateway-trade-123") {
		t.Fatalf("ledger note = %q, want provider trade number", ledger[0].Note)
	}
}

func TestHandleLDCCallbackConcurrentReplayCreditsOnce(t *testing.T) {
	cp, userID, nowNs := newLDCTestService(t)
	_, outTradeNo, err := cp.CreateLDCDeposit(userID, 3.25, nowNs)
	if err != nil {
		t.Fatalf("CreateLDCDeposit: %v", err)
	}
	params := signedLDCCallback(testLDCKey, map[string]string{
		"pid":          testLDCPID,
		"trade_status": "TRADE_SUCCESS",
		"out_trade_no": outTradeNo,
		"trade_no":     "gateway-parallel",
		"money":        "3.25",
		"sign_type":    "MD5",
	})

	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- cp.HandleLDCCallback(params, nowNs+1)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent callback: %v", err)
		}
	}

	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 3_250_000 {
		t.Fatalf("balance after concurrent callbacks = %d, want 3250000", balance)
	}
	ledger, err := cp.Engine.ListBalanceLedger(userID, 0)
	if err != nil {
		t.Fatalf("ListBalanceLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("concurrent callbacks wrote %d ledger rows, want 1", len(ledger))
	}
}

func TestHandleLDCCallbackRejectsInvalidOrMismatchedPayment(t *testing.T) {
	cp, userID, nowNs := newLDCTestService(t)
	if _, _, err := cp.CreateLDCDeposit(userID, 1.001, nowNs); err == nil {
		t.Fatal("a point amount beyond gateway cent precision was accepted")
	}
	_, outTradeNo, err := cp.CreateLDCDeposit(userID, 5, nowNs)
	if err != nil {
		t.Fatalf("CreateLDCDeposit: %v", err)
	}

	badSignature := map[string]string{
		"out_trade_no": outTradeNo,
		"trade_status": "TRADE_SUCCESS",
		"sign":         "invalid",
	}
	if err := cp.HandleLDCCallback(badSignature, nowNs+1); err == nil {
		t.Fatal("invalid signature was accepted")
	}

	mismatch := signedLDCCallback(testLDCKey, map[string]string{
		"out_trade_no": outTradeNo,
		"trade_no":     "gateway-mismatch",
		"trade_status": "TRADE_SUCCESS",
		"money":        "4.99",
	})
	if err := cp.HandleLDCCallback(mismatch, nowNs+2); err == nil {
		t.Fatal("mismatched amount was accepted")
	}
	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("rejected callbacks changed balance to %d", balance)
	}
}

func TestHandleLDCCallbackDoesNotReuseAProviderTradeNumber(t *testing.T) {
	cp, firstUserID, nowNs := newLDCTestService(t)
	second, err := cp.CreateAccount("ldc-service-user-two", "ldc-service-password-1234", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("create second LDC user: %v", err)
	}
	_, firstTrade, err := cp.CreateLDCDeposit(firstUserID, 2, nowNs)
	if err != nil {
		t.Fatalf("first CreateLDCDeposit: %v", err)
	}
	_, secondTrade, err := cp.CreateLDCDeposit(second.ID, 2, nowNs)
	if err != nil {
		t.Fatalf("second CreateLDCDeposit: %v", err)
	}

	callback := signedLDCCallback(testLDCKey, map[string]string{
		"out_trade_no": firstTrade,
		"trade_no":     "provider-once",
		"trade_status": "TRADE_SUCCESS",
		"money":        "2.00",
	})
	if err := cp.HandleLDCCallback(callback, nowNs+1); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	callback["out_trade_no"] = secondTrade
	callback = signedLDCCallback(testLDCKey, callback)
	if err := cp.HandleLDCCallback(callback, nowNs+2); err == nil {
		t.Fatal("the same provider trade number settled two intents")
	}
	firstBalance, err := cp.Engine.GetBalance(firstUserID)
	if err != nil {
		t.Fatalf("first balance: %v", err)
	}
	secondBalance, err := cp.Engine.GetBalance(second.ID)
	if err != nil {
		t.Fatalf("second balance: %v", err)
	}
	if firstBalance != 2_000_000 || secondBalance != 0 {
		t.Fatalf("balances after reused trade: first=%d second=%d", firstBalance, secondBalance)
	}
}

func signedLDCCallback(key string, params map[string]string) map[string]string {
	copyParams := make(map[string]string, len(params)+1)
	for name, value := range params {
		copyParams[name] = value
	}
	keys := make([]string, 0, len(copyParams))
	for name, value := range copyParams {
		if name == "sign" || name == "sign_type" || value == "" {
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+copyParams[name])
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	copyParams["sign"] = hex.EncodeToString(sum[:])
	return copyParams
}
