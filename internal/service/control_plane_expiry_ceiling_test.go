package service

import (
	"strings"
	"testing"

	"prism/internal/model"
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

func TestTenantEntitlementTermsRespectTheExpiryCeiling(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	plan, err := cp.CreatePlan(PlanInput{
		Name: "tenant-ceiling", Period: "month", DurationDays: 30, Status: planStatusActive,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	user, err := cp.CreateAccount("ceiling-user", "ceiling-user-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if _, err := cp.AssignSubscription(user.ID, plan.ID, maxExpiryDays+1, nowNs); err == nil {
		t.Fatal("AssignSubscription accepted an overlong duration")
	}
	sub, err := cp.AssignSubscription(user.ID, plan.ID, 30, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription: %v", err)
	}
	if _, err := cp.UpdateTenantSubscription(sub.ID, []byte(`{"extend_days":36501}`), nowNs); err == nil {
		t.Fatal("UpdateTenantSubscription accepted an overlong extension")
	}
	if _, err := cp.UpdateTenantSubscription(sub.ID, []byte(`{"expires_at_ns":9223372036854775807}`), nowNs); err == nil {
		t.Fatal("UpdateTenantSubscription accepted an unbounded absolute expiry")
	}
	if _, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{
		Name: "ceiling-key", ExpiresInDays: maxExpiryDays + 1,
	}, nowNs); err == nil {
		t.Fatal("CreateAccessKey accepted an overlong expiry")
	}
}
