package state

import (
	"path/filepath"
	"strings"
	"testing"
)

// downUpPair names a migration and the table its up file creates, so the test
// can tell that re-applying it actually rebuilt something.
type downUpPair struct {
	name  string
	table string
}

// TestDownMigrationsRunAndCanBeReapplied pins the properties a down file has to
// have, none of which anything else in the tree checks: nothing executes a down
// migration, so a broken one is invisible until an operator needs it.
//
//  1. the whole set runs, in the order a rollback uses - newest first;
//  2. every up file still applies afterwards, so a downgrade and a re-upgrade
//     leave a schema the code can use.
//
// The order matters as much as the statements. 000028 indexes a column that
// 000024 adds, so dropping 000024's column while 000028's index is still there
// fails with "error in index ... after drop column". A rollback takes the index
// away first, which is why the down files are applied in descending order
// rather than each one against the full schema.
//
// The pairs are the tenant-era ones: they are the migrations this tree
// introduced, and the ones an operator rolling back a release would touch.
func TestDownMigrationsRunAndCanBeReapplied(t *testing.T) {
	pairs := []downUpPair{
		{name: "000028_renewal_sweep_index", table: ""},
		{name: "000027_deposits", table: "deposit_intents"},
		{name: "000026_drop_orders", table: ""},
		{name: "000025_balance_ledger_ref_scope", table: ""},
		{name: "000024_plan_price_and_auto_renew", table: ""},
		{name: "000023_redeem_codes", table: "redeem_codes"},
		{name: "000022_balance", table: "balance_ledger"},
		{name: "000021_subscription_backfill_and_totp_replay", table: ""},
		{name: "000020_integrity_fixes", table: ""},
		{name: "000019_alerts", table: "alerts"},
		{name: "000018_invites", table: "invites"},
	}

	db, err := OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}

	// 1. Roll the whole set back, newest first, the way an operator would.
	for _, pair := range pairs {
		t.Run("down/"+pair.name, func(t *testing.T) {
			down, err := migrationsFS.ReadFile(stateMigrationsPath + "/" + pair.name + ".down.sql")
			if err != nil {
				t.Fatalf("read down: %v", err)
			}
			if _, err := db.Exec(string(down)); err != nil {
				t.Fatalf("apply down: %v", err)
			}
		})
	}

	// 2. Put it back, oldest first, and check each step rebuilt what it owns.
	for i := len(pairs) - 1; i >= 0; i-- {
		pair := pairs[i]
		t.Run("up/"+pair.name, func(t *testing.T) {
			up, err := migrationsFS.ReadFile(stateMigrationsPath + "/" + pair.name + ".up.sql")
			if err != nil {
				t.Fatalf("read up: %v", err)
			}
			if _, err := db.Exec(string(up)); err != nil {
				t.Fatalf("re-apply up after the downgrade: %v", err)
			}
			if pair.table == "" {
				return
			}
			var count int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, pair.table,
			).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", pair.table, err)
			}
			if count != 1 {
				t.Fatalf("table %s is missing after the round trip", pair.table)
			}
		})
	}
}

// The renewal sweep asks for the subscriptions that renew themselves and expire
// inside the lead time, once a minute. Without an index on that range it reads
// every subscription row, retired ones included, on every pass.
func TestRenewalSweepIndexExists(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}

	var sql string
	err = db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_user_subscriptions_renewal'`,
	).Scan(&sql)
	if err != nil {
		t.Fatalf("the renewal sweep index is missing: %v", err)
	}
	for _, want := range []string{"expires_at_ns", "auto_renew", "status"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the renewal sweep index does not cover %s: %s", want, sql)
		}
	}
}
