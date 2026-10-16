package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// --- RevokeUserSubscription ---

// A revoked subscription stays on the account. Deleting it was what made an
// account read as "no plan", which the loader and the publish path answer with
// the zero snapshot - unlimited.
func TestRevokeUserSubscription_KeepsTheRowAndEndsTheAccess(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	if err := repo.UpsertPlan(model.Plan{ID: "p-1", Name: "Starter", CreatedAtNs: 1, UpdatedAtNs: 1}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	sub := model.UserSubscription{
		ID: "us-1", UserID: "u-1", PlanID: "p-1", PlanSnapshotJSON: `{"name":"Starter","status":"active"}`,
		StartsAtNs: 10, ExpiresAtNs: 5000, PeriodStartNs: 10, PeriodUsedBytes: 42,
		Status: model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}

	if err := repo.RevokeUserSubscription("us-1"); err != nil {
		t.Fatalf("RevokeUserSubscription: %v", err)
	}

	stored, err := repo.GetUserSubscription("us-1")
	if err != nil {
		t.Fatalf("the revoked row must still exist: %v", err)
	}
	if stored.Status != model.SubscriptionStatusSuspended {
		t.Errorf("status after revoking: got %q, want %q", stored.Status, model.SubscriptionStatusSuspended)
	}
	// The window and the counted traffic are history: revoking ends the access,
	// it does not rewrite what happened.
	if stored.ExpiresAtNs != 5000 {
		t.Errorf("expires_at_ns after revoking: got %d, want 5000 (untouched)", stored.ExpiresAtNs)
	}
	if stored.PeriodUsedBytes != 42 {
		t.Errorf("period_used_bytes after revoking: got %d, want 42 (untouched)", stored.PeriodUsedBytes)
	}
	if stored.PlanSnapshotJSON != sub.PlanSnapshotJSON {
		t.Errorf("plan_snapshot_json after revoking: got %q, want %q", stored.PlanSnapshotJSON, sub.PlanSnapshotJSON)
	}

	// The row that decides the account's access is still there and still says
	// no, which is what keeps a restart from handing the account a zero snapshot.
	rows, err := repo.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	deciding := decidingSubscriptionFor(rows, "u-1")
	if deciding == nil {
		t.Fatal("the account has no deciding subscription after the revoke")
	}
	if deciding.Status != model.SubscriptionStatusSuspended {
		t.Errorf("deciding status after revoking: got %q, want %q", deciding.Status, model.SubscriptionStatusSuspended)
	}

	// Revoking is idempotent: the status is already what it would write.
	if err := repo.RevokeUserSubscription("us-1"); err != nil {
		t.Errorf("revoking an already revoked subscription: %v", err)
	}

	if err := repo.RevokeUserSubscription("us-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a missing subscription: got %v, want ErrNotFound", err)
	}
}

// --- AdvanceTOTPLastUsed ---

// The compare and set is the anti-replay point: one step is spent once, and a
// step that has already been spent (or an older one) is refused.
func TestAdvanceTOTPLastUsed_OneStepIsSpentOnce(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-totp", "totp")

	const (
		step    = int64(30_000_000_000)
		stepOne = int64(1_700_000_000_000_000_000) / step * step
	)

	accepted, err := repo.AdvanceTOTPLastUsed(user.ID, stepOne)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed: %v", err)
	}
	if !accepted {
		t.Fatal("the first use of a step was refused")
	}

	accepted, err = repo.AdvanceTOTPLastUsed(user.ID, stepOne)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed (second time): %v", err)
	}
	if accepted {
		t.Error("the same step was accepted twice")
	}

	// An earlier step is still inside the accepted tolerance of the verifier, and
	// it must not be usable after a later one was spent.
	accepted, err = repo.AdvanceTOTPLastUsed(user.ID, stepOne-step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed (earlier step): %v", err)
	}
	if accepted {
		t.Error("a step older than the one already spent was accepted")
	}

	stored, err := repo.GetUser(user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.TOTPLastUsedNs != stepOne {
		t.Errorf("totp_last_used_ns after a refused advance: got %d, want %d", stored.TOTPLastUsedNs, stepOne)
	}

	// The next step is a new code, and it is accepted.
	accepted, err = repo.AdvanceTOTPLastUsed(user.ID, stepOne+step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed (next step): %v", err)
	}
	if !accepted {
		t.Error("the next step was refused")
	}

	if _, err := repo.AdvanceTOTPLastUsed("u-missing", stepOne); !errors.Is(err, ErrNotFound) {
		t.Errorf("advancing the ledger of a missing account: got %v, want ErrNotFound", err)
	}
}

// decidingSubscriptionFor is the rule the loader and the console share
// (keys.DecidingSubscription) reduced to one account, so the test reads the same
// answer the process makes.
func decidingSubscriptionFor(rows []model.UserSubscription, userID string) *model.UserSubscription {
	var mine []model.UserSubscription
	for _, row := range rows {
		if row.UserID == userID {
			mine = append(mine, row)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	best := &mine[0]
	for i := range mine {
		candidate := &mine[i]
		candidateActive := candidate.Status == model.SubscriptionStatusActive
		bestActive := best.Status == model.SubscriptionStatusActive
		if candidateActive != bestActive {
			if candidateActive {
				best = candidate
			}
			continue
		}
		if candidate.ExpiresAtNs > best.ExpiresAtNs {
			best = candidate
		}
	}
	return best
}

// --- UpsertUserSubscription against the partial unique index ---

// ux_user_subscriptions_active (000020) allows one active row per user, and the
// repository turns its violation into ErrConflict so the service can answer a
// conflict instead of a fault when two assigns race. Retired rows are outside
// the index, which is why promoting one back to active is what meets it.
func TestUpsertUserSubscription_SecondActiveRowIsAConflict(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-act", "act")
	if err := repo.UpsertPlan(model.Plan{ID: "p-act", Name: "Act", CreatedAtNs: 1, UpdatedAtNs: 1}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	first := model.UserSubscription{
		ID: "us-act-1", UserID: "u-act", PlanID: "p-act", PlanSnapshotJSON: `{"name":"Act","status":"active"}`,
		StartsAtNs: 10, ExpiresAtNs: 5000, PeriodStartNs: 10, Status: model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(first); err != nil {
		t.Fatalf("UpsertUserSubscription (first): %v", err)
	}

	second := first
	second.ID = "us-act-2"
	if err := repo.UpsertUserSubscription(second); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second active row for one user: got %v, want ErrConflict", err)
	}
	rows, err := repo.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("a refused insert left %d rows behind, want 1", len(rows))
	}

	// A retired row is history and may repeat a user_id.
	second.Status = model.SubscriptionStatusSuspended
	if err := repo.UpsertUserSubscription(second); err != nil {
		t.Fatalf("UpsertUserSubscription (suspended second row): %v", err)
	}

	// Promoting it is a terms write, and it meets the same index.
	second.Status = model.SubscriptionStatusActive
	if err := repo.UpdateUserSubscriptionTerms(second); !errors.Is(err, ErrConflict) {
		t.Fatalf("promoting a second row to active: got %v, want ErrConflict", err)
	}
}
