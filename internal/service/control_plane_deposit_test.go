package service

import (
	"errors"
	"testing"

	"prism/internal/config"
	"prism/internal/model"
)

// Deposits are off unless an operator configured a receiving address, so every
// test here starts by saying where the money should arrive.
const testUSDTAddress = "TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"

func newDepositTestService(t *testing.T) (*ControlPlaneService, int64) {
	t.Helper()
	cp, _, _, nowNs := newTenantTestService(t)
	cp.EnvCfg = &config.EnvConfig{
		USDTAddress:  testUSDTAddress,
		USDTContract: config.DefaultUSDTContract,
	}
	return cp, nowNs
}

func createDepositUser(t *testing.T, cp *ControlPlaneService, username string) string {
	t.Helper()
	account, err := cp.CreateAccount(username, username+"-password-1234", model.UserRoleUser, 1)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return account.ID
}

func TestDepositInfoReportsConfiguredState(t *testing.T) {
	cp, _ := newDepositTestService(t)

	info := cp.SelfDepositInfo()
	if !info.Enabled {
		t.Fatal("deposits must be enabled when an address is configured")
	}
	if info.Address != testUSDTAddress {
		t.Fatalf("address: got %q, want %q", info.Address, testUSDTAddress)
	}
	if info.MinMicro != depositMinMicro {
		t.Fatalf("min: got %d, want %d", info.MinMicro, depositMinMicro)
	}
	if info.WindowSecs != 900 {
		t.Fatalf("window: got %d seconds, want 900", info.WindowSecs)
	}

	// An unconfigured address turns the feature off rather than breaking it.
	cp.EnvCfg.USDTAddress = ""
	if cp.SelfDepositInfo().Enabled {
		t.Fatal("deposits must be disabled when no address is configured")
	}
}

func TestDepositCandidatesSkipWholeAmounts(t *testing.T) {
	// Asking for a whole amount must still produce a fractional one: a round
	// number is the most likely to collide with an unrelated transfer.
	candidates := depositCandidates(10_000_000)
	if len(candidates) == 0 {
		t.Fatal("candidates must not be empty")
	}
	if candidates[0] != 10_000_001 {
		t.Fatalf("first candidate: got %d, want 10000001", candidates[0])
	}
	for _, amount := range candidates {
		if amount%1_000_000 == 0 {
			t.Fatalf("candidate %d is a whole amount", amount)
		}
	}
	if len(candidates) > depositAmountAttempts {
		t.Fatalf("candidates: got %d, want at most %d", len(candidates), depositAmountAttempts)
	}
}

func TestSelfDepositRejectsOutOfRangeAmounts(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-range")

	if _, err := cp.SelfDeposit(userID, depositMinMicro-1, nowNs); err == nil {
		t.Fatal("an amount below the minimum must be refused")
	}
	if _, err := cp.SelfDeposit(userID, depositMaxMicro+1, nowNs); err == nil {
		t.Fatal("an amount above the maximum must be refused")
	}
}

// Two intents asking for the same amount get different amounts to pay, and both
// are inside the requested range.
func TestSelfDepositHandsOutDistinctAmounts(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	first := createDepositUser(t, cp, "deposit-one")
	second := createDepositUser(t, cp, "deposit-two")

	a, err := cp.SelfDeposit(first, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("first deposit: %v", err)
	}
	b, err := cp.SelfDeposit(second, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("second deposit: %v", err)
	}
	if a.AmountMicro == b.AmountMicro {
		t.Fatalf("two intents share the amount %d", a.AmountMicro)
	}
	if a.AmountMicro != 10_000_001 || b.AmountMicro != 10_000_002 {
		t.Fatalf("amounts: got %d and %d, want 10000001 and 10000002", a.AmountMicro, b.AmountMicro)
	}
	if a.Status != model.DepositStatusPending {
		t.Fatalf("status: got %q, want pending", a.Status)
	}
	if a.ExpiresAtNs-a.CreatedAtNs != depositWindowNs {
		t.Fatalf("payment window: got %d, want %d", a.ExpiresAtNs-a.CreatedAtNs, depositWindowNs)
	}
	if a.HoldUntilNs-a.ExpiresAtNs != depositHoldNs {
		t.Fatalf("silent hold: got %d, want %d", a.HoldUntilNs-a.ExpiresAtNs, depositHoldNs)
	}
}

// Another user's intent must be indistinguishable from one that does not exist.
func TestSelfDepositIntentHidesOtherUsersIntents(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	owner := createDepositUser(t, cp, "deposit-owner")
	other := createDepositUser(t, cp, "deposit-other")

	intent, err := cp.SelfDeposit(owner, 5_000_000, nowNs)
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	if _, err := cp.SelfDepositIntent(owner, intent.ID); err != nil {
		t.Fatalf("owner reading own intent: %v", err)
	}

	_, err = cp.SelfDepositIntent(other, intent.ID)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("other user: got %v, want a service error", err)
	}
	if svcErr.Code != "NOT_FOUND" {
		t.Fatalf("other user: got code %q, want NOT_FOUND", svcErr.Code)
	}

	// A made-up id answers exactly the same way.
	_, err = cp.SelfDepositIntent(other, "dep_does_not_exist")
	if !errors.As(err, &svcErr) || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("unknown id: got %v, want NOT_FOUND", err)
	}
}

func TestSelfDepositRequiresConfiguredAddress(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-off")
	cp.EnvCfg.USDTAddress = ""

	if _, err := cp.SelfDeposit(userID, 10_000_000, nowNs); err == nil {
		t.Fatal("a deposit must be refused when no address is configured")
	}
}

// The state layer is reached through the engine, so this checks the wiring the
// service adds: the intent is stored for the caller and listed back.
func TestSelfDepositsListsOwnIntents(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-list")

	if _, err := cp.SelfDeposit(userID, 2_000_000, nowNs); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	got, err := cp.SelfDeposits(userID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("intents: got %d, want 1", len(got))
	}
	if got[0].Address != testUSDTAddress {
		t.Fatalf("address: got %q, want %q", got[0].Address, testUSDTAddress)
	}
}
