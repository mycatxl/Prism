package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/tron"
)

func TestRound3PaymentRateCannotRoundBeforeCrediting(t *testing.T) {
	h := newPatchHarness(t)
	for _, field := range []string{"ldc_rate", "tron_trx_ratio"} {
		for _, raw := range []string{"0.99999999999999999", "0.10000000000000001", "1000000.00000000001", "1e-999999", "1e999999"} {
			before := h.cp.paymentConfig()
			if _, err := h.cp.PatchRuntimeConfig([]byte(fmt.Sprintf(`{%q:%s}`, field, raw))); err == nil {
				t.Fatalf("silently rounded %s=%s", field, raw)
			}
			after := h.cp.paymentConfig()
			if before.LDCRate != after.LDCRate || before.TronTRXRatio != after.TronTRXRatio {
				t.Fatal("rejected rate changed configuration")
			}
		}
		for _, raw := range []string{"0.1000", "0.125", "1.2345e-12", "1000000"} {
			if _, err := h.cp.PatchRuntimeConfig([]byte(fmt.Sprintf(`{%q:%s}`, field, raw))); err != nil {
				t.Fatalf("exact decimal rejected %s=%s: %v", field, raw, err)
			}
		}
	}
}

func TestRound3MalformedProviderRowKeepsRetiredWalletRetryable(t *testing.T) {
	cp, now := newDepositTestService(t)
	user := createDepositUser(t, cp, "provider-retry")
	order, err := cp.SelfDeposit(user, 10_000_000, now)
	if err != nil {
		t.Fatal(err)
	}
	// Closing checkout removes the current wallet from the automatic scan
	// list. Only the outstanding order can keep its destination retryable.
	cp.EnvCfg.USDTAddress = ""
	var repaired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := map[string]any{"address": config.DefaultUSDTContract}
		if repaired.Load() {
			token["decimals"] = 6
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"transaction_id": "repaired-tx", "block_timestamp": now/1e6 + 1000, "to": testUSDTAddress, "value": strconv.FormatInt(order.AmountMicro, 10), "token_info": token}}})
	}))
	defer server.Close()
	cp.Tron = tron.NewClient(server.URL, "", config.DefaultUSDTContract)
	later := now + int64(2*time.Hour)
	if n, err := cp.ScanDeposits(context.Background(), later); err == nil || n != 0 {
		t.Fatalf("incomplete row accepted: %d %v", n, err)
	}
	targets, err := cp.Engine.DepositScanTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("unpaid order retired: %+v %v", targets, err)
	}
	repaired.Store(true)
	if n, err := cp.ScanDeposits(context.Background(), later); err != nil || n != 1 {
		t.Fatalf("repair failed: %d %v", n, err)
	}
	if balance, err := cp.Engine.GetBalance(user); err != nil || balance != order.CreditMicro {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	if n, err := cp.ScanDeposits(context.Background(), later); err != nil || n != 0 {
		t.Fatalf("retry credited twice: %d %v", n, err)
	}
}

func TestRound3UsageViewsSaturateWithoutWrapping(t *testing.T) {
	cp, _, _, now := newTenantTestService(t)
	day, _ := usageWindow(1, now)
	rows := []model.UsageRow{{KeyID: "a", UserID: "user", PlatformID: "p", TSNs: day, IngressBytes: math.MaxInt64, EgressBytes: 1}, {KeyID: "b", UserID: "user", PlatformID: "p", TSNs: day, IngressBytes: 10}}
	if err := cp.Engine.UpsertUsageHourly(rows); err != nil {
		t.Fatal(err)
	}
	summary, err := cp.UsageSummary(now)
	if err != nil || summary.TodayBytes != math.MaxInt64 {
		t.Fatalf("summary=%+v %v", summary, err)
	}
	days, err := cp.UsageDaily(1, now)
	if err != nil || len(days) != 1 || days[0].TotalBytes != math.MaxInt64 {
		t.Fatalf("days=%+v %v", days, err)
	}
	users, err := cp.UsageByUser(day, day+int64(24*time.Hour), 10, now)
	if err != nil || len(users) != 1 || users[0].TotalBytes != math.MaxInt64 {
		t.Fatalf("users=%+v %v", users, err)
	}
	platforms, err := cp.UsageByPlatform(1, now)
	if err != nil || len(platforms) != 1 || platforms[0].TotalBytes != math.MaxInt64 {
		t.Fatalf("platforms=%+v %v", platforms, err)
	}
	plans, err := cp.UsageByPlan(1, now)
	if err != nil || len(plans) != 1 || plans[0].TotalBytes != math.MaxInt64 {
		t.Fatalf("plans=%+v %v", plans, err)
	}
}
