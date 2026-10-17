package service

import (
	"strings"
	"testing"
)

// An invite and a redeem code both take expires_in_days straight from an
// operator. Without a ceiling the value reaches expiryFromDays, where it used to
// wrap into a negative offset and mint a code that was already expired.
func TestInviteAndRedeemCodeRefuseAnExpiryThatWouldOverflow(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	plan, err := cp.CreatePlan(PlanInput{
		Name: "ceiling", Period: "month", DurationDays: 30, Status: planStatusActive,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	for _, days := range []int{213504, 1_000_000} {
		_, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 1, ExpiresInDays: days}, "admin", nowNs)
		if err == nil {
			t.Fatalf("CreateInvite accepted expires_in_days=%d", days)
		}
		if !strings.Contains(err.Error(), "expires_in_days") {
			t.Fatalf("CreateInvite error %q does not name the field", err.Error())
		}

		_, err = cp.CreateRedeemCodes(RedeemCodeInput{AmountMicro: 1_000_000, Count: 1, ExpiresInDays: days}, "admin", nowNs)
		if err == nil {
			t.Fatalf("CreateRedeemCodes accepted expires_in_days=%d", days)
		}
		if !strings.Contains(err.Error(), "expires_in_days") {
			t.Fatalf("CreateRedeemCodes error %q does not name the field", err.Error())
		}
	}

	if _, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 1, ExpiresInDays: maxExpiryDays}, "admin", nowNs); err != nil {
		t.Fatalf("CreateInvite refused the ceiling itself: %v", err)
	}
}
