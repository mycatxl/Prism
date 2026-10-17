package service

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// Redemption and balance through the service: what a user experiences, and the
// invariants an operator depends on.

func createBalanceTestUser(t *testing.T, cp *ControlPlaneService, username string) model.User {
	t.Helper()
	detail, err := cp.CreateTenantUser(TenantUserInput{
		Username: username,
		Password: "correct horse battery staple",
	}, 1_700_000_000_000_000_000)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return model.User{ID: detail.ID, Username: detail.Username}
}

func TestRedeemCreditsAndRefusesASecondUse(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	user := createBalanceTestUser(t, cp, "redeemer")

	created, err := cp.CreateRedeemCodes(RedeemCodeInput{
		AmountMicro: 10_000_000,
		MaxUses:     1,
		Note:        "community sale",
	}, "admin", nowNs)
	if err != nil {
		t.Fatalf("create codes: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("codes: got %d, want 1", len(created))
	}
	code := created[0].Code

	// The plaintext is returned once and is not stored, so the view must not
	// carry it.
	if created[0].CodeHint != codeHint(code) {
		t.Fatalf("hint: got %q, want %q", created[0].CodeHint, codeHint(code))
	}

	view, err := cp.Redeem(user.ID, code, nowNs)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if view.BalanceMicro != 10_000_000 {
		t.Fatalf("balance: got %d, want 10000000", view.BalanceMicro)
	}

	// A second use of a one-use code is refused, and the balance does not move.
	if _, err := cp.Redeem(user.ID, code, nowNs); err == nil {
		t.Fatal("a spent code was accepted twice")
	}
	after, err := cp.SelfBalance(user.ID, 0)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if after.BalanceMicro != 10_000_000 {
		t.Fatalf("a refused redemption moved the balance: got %d", after.BalanceMicro)
	}
}

func TestRedeemRefusesUnknownCodeWithoutChargingAnything(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	user := createBalanceTestUser(t, cp, "prober")

	// A well-shaped code that was never minted. The refusal must not tell the
	// caller whether the code exists.
	_, err := cp.Redeem(user.ID, "ABCD-EFGH-JKLM-NPQR-STUV", nowNs)
	if err == nil {
		t.Fatal("an unknown code was accepted")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("unknown code: got %v, want INVALID_ARGUMENT", err)
	}

	// A malformed code is refused the same way.
	_, err = cp.Redeem(user.ID, "nonsense", nowNs)
	if !errors.As(err, &svcErr) || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("malformed code: got %v, want INVALID_ARGUMENT", err)
	}

	view, err := cp.SelfBalance(user.ID, 0)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if view.BalanceMicro != 0 || len(view.Ledger) != 0 {
		t.Fatalf("a refused code moved money: %+v", view)
	}
}

func TestRedeemAcceptsLowercaseAndUnseparatedCodes(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	user := createBalanceTestUser(t, cp, "typist")

	created, err := cp.CreateRedeemCodes(RedeemCodeInput{AmountMicro: 1_000_000, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	plain := created[0].Code

	// What a person actually types: lower case, no separators, stray spaces.
	typed := " " + lowerNoSeparators(plain) + " "
	view, err := cp.Redeem(user.ID, typed, nowNs)
	if err != nil {
		t.Fatalf("redeem typed form: %v", err)
	}
	if view.BalanceMicro != 1_000_000 {
		t.Fatalf("balance: got %d", view.BalanceMicro)
	}
}

func lowerNoSeparators(code string) string {
	out := make([]rune, 0, len(code))
	for _, r := range code {
		if r == '-' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r = r - 'A' + 'a'
		}
		out = append(out, r)
	}
	return string(out)
}

func TestAdjustBalanceRequiresNoteAndRefusesOverdraft(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	user := createBalanceTestUser(t, cp, "adjusted")

	if _, err := cp.AdjustBalance(user.ID, 5_000_000, "   ", "admin", nowNs); err == nil {
		t.Fatal("an adjustment without a note was accepted")
	}
	if _, err := cp.AdjustBalance(user.ID, 0, "zero", "admin", nowNs); err == nil {
		t.Fatal("a zero adjustment was accepted")
	}

	if _, err := cp.AdjustBalance(user.ID, 5_000_000, "goodwill", "admin", nowNs); err != nil {
		t.Fatalf("credit: %v", err)
	}
	if _, err := cp.AdjustBalance(user.ID, -6_000_000, "clawback", "admin", nowNs); err == nil {
		t.Fatal("an overdraft was accepted")
	}

	view, err := cp.SelfBalance(user.ID, 0)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if view.BalanceMicro != 5_000_000 {
		t.Fatalf("balance after a refused debit: got %d", view.BalanceMicro)
	}
	if len(view.Ledger) != 1 {
		t.Fatalf("ledger rows: got %d, want 1", len(view.Ledger))
	}
	if view.Ledger[0].Actor != "admin" || view.Ledger[0].Note != "goodwill" {
		t.Fatalf("the adjustment did not record who and why: %+v", view.Ledger[0])
	}
}

func TestCreateRedeemCodesValidatesInput(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	cases := []struct {
		name string
		in   RedeemCodeInput
	}{
		{"zero amount", RedeemCodeInput{AmountMicro: 0}},
		{"negative amount", RedeemCodeInput{AmountMicro: -1}},
		{"absurd amount", RedeemCodeInput{AmountMicro: maxBalanceMicro + 1}},
		{"negative uses", RedeemCodeInput{AmountMicro: 1, MaxUses: -1}},
		{"negative expiry", RedeemCodeInput{AmountMicro: 1, ExpiresInDays: -1}},
		{"too many at once", RedeemCodeInput{AmountMicro: 1, Count: maxRedeemCodeBatch + 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cp.CreateRedeemCodes(tc.in, "admin", nowNs); err == nil {
				t.Fatalf("accepted %+v", tc.in)
			}
		})
	}
}

func TestListRedeemCodesShowsHintsAndComputedStatus(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	if _, err := cp.CreateRedeemCodes(RedeemCodeInput{
		AmountMicro: 1_000_000, MaxUses: 1, Count: 2,
	}, "admin", nowNs); err != nil {
		t.Fatalf("create: %v", err)
	}

	views, total, err := cp.ListRedeemCodes(0, 0, nowNs)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(views) != 2 {
		t.Fatalf("views: got %d of %d", len(views), total)
	}
	for _, view := range views {
		if view.CodeHint == "" {
			t.Fatalf("a listed code has no hint: %+v", view)
		}
		if view.Status != model.RedeemCodeStatusActive {
			t.Fatalf("a fresh code is not active: %+v", view)
		}
	}
}

func TestDeleteRedeemCodeIsNotFoundOnceGone(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	created, err := cp.CreateRedeemCodes(RedeemCodeInput{AmountMicro: 1_000_000}, "admin", nowNs)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cp.DeleteRedeemCode(created[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err = cp.DeleteRedeemCode(created[0].ID)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("delete twice: got %v, want NOT_FOUND", err)
	}
}
