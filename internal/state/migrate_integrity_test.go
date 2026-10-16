package state

import (
	"errors"
	"strings"
	"testing"

	"prism/internal/model"
)

// --- 000020_integrity_fixes ---

// A second active subscription for the same user is rejected by the schema now,
// not only by the order the service layer happens to write in: the partial unique
// index turns two racing AssignSubscription calls into one row and one error.
func TestMigrateStateDB_ActiveSubscriptionIsUnique(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	if err := repo.UpsertPlan(model.Plan{
		ID: "p-1", Name: "Starter", CreatedAtNs: 1, UpdatedAtNs: 1,
	}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	active := func(id string) model.UserSubscription {
		return model.UserSubscription{
			ID: id, UserID: "u-1", PlanID: "p-1", PlanSnapshotJSON: `{}`,
			StartsAtNs: 1, ExpiresAtNs: 2, PeriodStartNs: 1,
			Status: model.SubscriptionStatusActive,
		}
	}
	if err := repo.UpsertUserSubscription(active("us-1")); err != nil {
		t.Fatalf("first active subscription: %v", err)
	}

	err := repo.UpsertUserSubscription(active("us-2"))
	if err == nil {
		t.Fatal("a second active subscription for the same user was accepted")
	}
	// The index is the guard; the repository reports its violation as the
	// conflict the service answers with, rather than as a fault.
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second active subscription: got %v, want ErrConflict", err)
	}

	// The schema refuses it on its own, with the repository out of the way.
	_, rawErr := repo.db.Exec(`
		INSERT INTO user_subscriptions (id, user_id, plan_id, plan_snapshot_json,
		                                starts_at_ns, expires_at_ns, period_start_ns, period_used_bytes, status)
		VALUES ('us-raw', 'u-1', 'p-1', '{}', 1, 2, 1, 0, 'active')
	`)
	if !isSQLiteUniqueConstraint(rawErr) {
		t.Fatalf("a raw insert of a second active row: got %v, want a unique constraint error", rawErr)
	}

	// The index is partial on purpose: retired subscriptions may repeat a
	// user_id as often as the account has changed plans.
	retired := active("us-3")
	retired.Status = model.SubscriptionStatusExpired
	if err := repo.UpsertUserSubscription(retired); err != nil {
		t.Fatalf("expired subscription: %v", err)
	}

	var indexSQL string
	if err := repo.db.QueryRow(`
		SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'ux_user_subscriptions_active'
	`).Scan(&indexSQL); err != nil {
		t.Fatalf("ux_user_subscriptions_active is missing after the migration: %v", err)
	}
	if !strings.Contains(indexSQL, "WHERE status = 'active'") {
		t.Errorf("the index is not partial: %s", indexSQL)
	}
}

// The index can only be created over data that satisfies it, so the migration
// repairs the rows that did not - without deleting them. The survivor is the
// latest expiry, then the latest start, then the smallest id.
func TestMigrateStateDB_IntegrityFixesRepairDuplicateActiveSubscriptions(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Stop at 000019: a database from before the fix, where nothing stops two
	// active rows for one user from existing side by side.
	m := newStateMigrator(t, db)
	if err := m.Migrate(stateVersionAlerts); err != nil {
		t.Fatalf("migrate to %d: %v", stateVersionAlerts, err)
	}

	for _, userID := range []string{"u-1", "u-2", "u-3"} {
		if _, err := db.Exec(`
			INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
			VALUES (?, ?, 'hash', 'user', 'active', 1, 1)
		`, userID, "name-"+userID); err != nil {
			t.Fatalf("seed user %s: %v", userID, err)
		}
	}

	seed := []struct {
		id, userID              string
		startsAtNs, expiresAtNs int64
	}{
		// u-1: the latest expiry wins over the latest start.
		{"us-latest-expiry", "u-1", 1, 500},
		{"us-latest-start", "u-1", 9, 300},
		{"us-mid", "u-1", 5, 300},

		// u-2: expiry and start tie, so the smallest id is kept.
		{"us-b", "u-2", 5, 300},
		{"us-a", "u-2", 5, 300},

		// u-3: expiry ties, the later start wins.
		{"us-older-start", "u-3", 1, 300},
		{"us-newer-start", "u-3", 5, 300},
	}
	for _, row := range seed {
		if _, err := db.Exec(`
			INSERT INTO user_subscriptions (
				id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
				period_start_ns, period_used_bytes, status
			) VALUES (?, ?, NULL, '{}', ?, ?, ?, 7, 'active')
		`, row.id, row.userID, row.startsAtNs, row.expiresAtNs, row.startsAtNs); err != nil {
			t.Fatalf("seed subscription %s: %v", row.id, err)
		}
	}

	// The production upgrade path has to carry the repair.
	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)

	// Nothing was thrown away: the demoted rows are history, not garbage.
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_subscriptions`).Scan(&total); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if total != len(seed) {
		t.Errorf("subscription rows: got %d, want %d (the repair demotes, never deletes)", total, len(seed))
	}

	want := map[string]string{"u-1": "us-latest-expiry", "u-2": "us-a", "u-3": "us-newer-start"}
	for userID, wantID := range want {
		rows, err := db.Query(`
			SELECT id FROM user_subscriptions WHERE user_id = ? AND status = 'active'
		`, userID)
		if err != nil {
			t.Fatalf("read active subscriptions of %s: %v", userID, err)
		}
		var activeIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatalf("scan active subscription of %s: %v", userID, err)
			}
			activeIDs = append(activeIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate active subscriptions of %s: %v", userID, err)
		}
		if len(activeIDs) != 1 || activeIDs[0] != wantID {
			t.Errorf("active subscriptions of %s: got %v, want [%s]", userID, activeIDs, wantID)
		}
	}

	// The demoted row keeps everything else it had.
	var usedBytes int64
	if err := db.QueryRow(`
		SELECT period_used_bytes FROM user_subscriptions WHERE id = 'us-a'
	`).Scan(&usedBytes); err != nil {
		t.Fatalf("read the demoted row: %v", err)
	}
	if usedBytes != 7 {
		t.Errorf("period_used_bytes of a demoted row: got %d, want 7", usedBytes)
	}

	// And the shape is enforced from now on.
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('us-dup', 'u-2', NULL, '{}', 1, 2, 1, 0, 'active')
	`); err == nil {
		t.Error("the repaired database accepts a second active subscription")
	}
}

// invites.plan_id is cleared when the plan it points at is deleted. 000018 says
// so in a comment; the trigger makes it true, and it has to leave the invites
// that point elsewhere alone.
func TestMigrateStateDB_PlanDeleteClearsInvitePlan(t *testing.T) {
	repo := newTestStateRepo(t)

	for _, plan := range []model.Plan{
		{ID: "p-1", Name: "Starter", CreatedAtNs: 1, UpdatedAtNs: 1},
		{ID: "p-2", Name: "Pro", CreatedAtNs: 1, UpdatedAtNs: 1},
	} {
		if err := repo.UpsertPlan(plan); err != nil {
			t.Fatalf("UpsertPlan(%s): %v", plan.ID, err)
		}
	}
	for _, inv := range []model.Invite{
		{ID: "inv-1", Code: "CODE1", PlanID: "p-1", MaxUses: 1, Status: "active", CreatedAtNs: 1, UpdatedAtNs: 1},
		{ID: "inv-2", Code: "CODE2", PlanID: "p-2", MaxUses: 1, Status: "active", CreatedAtNs: 1, UpdatedAtNs: 1},
	} {
		if err := repo.InsertInvite(inv); err != nil {
			t.Fatalf("InsertInvite(%s): %v", inv.ID, err)
		}
	}

	if err := repo.DeletePlan("p-1"); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}

	got, err := repo.GetInvite("inv-1")
	if err != nil {
		t.Fatalf("the invite must survive its plan: %v", err)
	}
	if got.PlanID != "" {
		t.Errorf("invites.plan_id after the plan was deleted: got %q, want empty", got.PlanID)
	}

	other, err := repo.GetInvite("inv-2")
	if err != nil {
		t.Fatalf("GetInvite(inv-2): %v", err)
	}
	if other.PlanID != "p-2" {
		t.Errorf("an invite of another plan was touched: got %q, want %q", other.PlanID, "p-2")
	}
}

// The session cleanup deletes on expires_at_ns, which had no index: the delete
// scanned every session row, live ones included.
func TestMigrateStateDB_SessionsExpiryIndex(t *testing.T) {
	repo := newTestStateRepo(t)

	var indexSQL string
	if err := repo.db.QueryRow(`
		SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_sessions_expires'
	`).Scan(&indexSQL); err != nil {
		t.Fatalf("idx_sessions_expires is missing after the migration: %v", err)
	}
	if !strings.Contains(indexSQL, "expires_at_ns") {
		t.Errorf("idx_sessions_expires is not on expires_at_ns: %s", indexSQL)
	}

	// The index has to be usable for the predicate the cleanup uses, otherwise
	// the migration only added write work.
	var (
		selectID, order, from int
		detail                string
	)
	if err := repo.db.QueryRow(`
		EXPLAIN QUERY PLAN SELECT id_hash FROM sessions WHERE expires_at_ns < 1
	`).Scan(&selectID, &order, &from, &detail); err != nil {
		t.Fatalf("explain the session cleanup: %v", err)
	}
	if !strings.Contains(detail, "idx_sessions_expires") {
		t.Errorf("the cleanup predicate does not use the index: %s", detail)
	}
}

// The down migration has to leave a working database behind: the index and the
// trigger are the schema the up migration added.
func TestMigrateStateDB_IntegrityFixesDown(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	m := newStateMigrator(t, db)
	if err := m.Migrate(stateVersionIntegrityFixes); err != nil {
		t.Fatalf("migrate to %d: %v", stateVersionIntegrityFixes, err)
	}
	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u-down', 'down', 'hash', 'user', 'active', 1, 1)
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('us-down', 'u-down', NULL, '{}', 1, 2, 1, 0, 'active')
	`); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("down of %d: %v", stateLatestVersion, err)
	}
	assertStateMigrationVersion(t, db, stateVersionAlerts)

	for _, kind := range []struct {
		name  string
		query string
	}{
		{"ux_user_subscriptions_active", `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'ux_user_subscriptions_active'`},
		{"idx_sessions_expires", `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sessions_expires'`},
		{"trg_invites_clear_plan", `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'trg_invites_clear_plan'`},
	} {
		var count int
		if err := db.QueryRow(kind.query).Scan(&count); err != nil {
			t.Fatalf("look up %s: %v", kind.name, err)
		}
		if count != 0 {
			t.Errorf("%s survived the down migration", kind.name)
		}
	}

	// The rows the up migration kept are still there, and the old shape accepts
	// what the index used to reject.
	var subscriptions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_subscriptions`).Scan(&subscriptions); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subscriptions != 1 {
		t.Errorf("subscription rows after the down migration: got %d, want 1", subscriptions)
	}
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('us-down-2', 'u-down', NULL, '{}', 1, 2, 1, 0, 'active')
	`); err != nil {
		t.Errorf("after the down migration a second active row must be accepted again: %v", err)
	}
}
