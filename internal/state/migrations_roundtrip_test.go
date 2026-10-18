package state

import (
	"path/filepath"
	"testing"
)

// downUpPair names a migration and the table its up file creates, so the test
// can tell that re-applying it actually rebuilt something.
type downUpPair struct {
	name  string
	table string
}

// TestDownMigrationsRunAndCanBeReapplied pins the two properties a down file has
// to have, neither of which anything else in the tree checks: nothing executes a
// down migration, so a broken one is invisible until an operator needs it.
//
//  1. it runs against a fully migrated database;
//  2. the up file that follows it still applies afterwards, so a downgrade and
//     a re-upgrade leave a schema the code can use.
//
// The pairs are the tenant-era ones, newest first: they are the migrations this
// tree introduced, and the ones an operator rolling back a release would touch.
func TestDownMigrationsRunAndCanBeReapplied(t *testing.T) {
	pairs := []downUpPair{
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

	for _, pair := range pairs {
		t.Run(pair.name, func(t *testing.T) {
			down, err := migrationsFS.ReadFile(stateMigrationsPath + "/" + pair.name + ".down.sql")
			if err != nil {
				t.Fatalf("read down: %v", err)
			}
			if _, err := db.Exec(string(down)); err != nil {
				t.Fatalf("apply down: %v", err)
			}
			up, err := migrationsFS.ReadFile(stateMigrationsPath + "/" + pair.name + ".up.sql")
			if err != nil {
				t.Fatalf("read up: %v", err)
			}
			if _, err := db.Exec(string(up)); err != nil {
				t.Fatalf("re-apply up after its own down: %v", err)
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
