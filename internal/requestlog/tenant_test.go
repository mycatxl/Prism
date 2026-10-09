package requestlog

import (
	"testing"
	"time"

	"prism/internal/proxy"
)

func TestRepo_UserAndKeyAttribution(t *testing.T) {
	logDir := t.TempDir()
	repo := NewRepo(logDir, 1<<20, 2)
	if err := repo.Open(); err != nil {
		t.Fatal(err)
	}
	// Turn the active DB into a pre-multi-tenant one, then rotate and reopen
	// so ensureRequestLogSchema has to add the columns back.
	if _, err := repo.activeDB.Exec(`
		DROP INDEX IF EXISTS idx_request_logs_user_ts;
		DROP INDEX IF EXISTS idx_request_logs_key_ts;
		ALTER TABLE request_logs DROP COLUMN user_id;
		ALTER TABLE request_logs DROP COLUMN key_id;`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo = NewRepo(logDir, 1<<20, 2)
	if err := repo.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ts := time.Now().Add(-time.Minute).UnixNano()
	if _, err := repo.InsertBatch([]proxy.RequestLogEntry{
		{ID: "a", StartedAtNs: ts, ProxyType: proxy.ProxyTypeForward, UserID: "u_a", KeyID: "pk_a"},
		{ID: "b", StartedAtNs: ts + 1, ProxyType: proxy.ProxyTypeForward, UserID: "u_b", KeyID: "pk_b"},
		{ID: "c", StartedAtNs: ts + 2, ProxyType: proxy.ProxyTypeForward},
	}); err != nil {
		t.Fatal(err)
	}

	rows, _, _, err := repo.List(ListFilter{UserID: "u_a", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "a" || rows[0].UserID != "u_a" || rows[0].KeyID != "pk_a" {
		t.Fatalf("user filter = %+v", rows)
	}
	// Fuzzy mode must not widen the owner filter.
	rows, _, _, _ = repo.List(ListFilter{UserID: "u_", Fuzzy: true, Limit: 10})
	if len(rows) != 0 {
		t.Fatalf("fuzzy user filter leaked %d rows", len(rows))
	}
	rows, _, _, _ = repo.List(ListFilter{KeyID: "pk_b", Limit: 10})
	if len(rows) != 1 || rows[0].ID != "b" {
		t.Fatalf("key filter = %+v", rows)
	}
	rows, _, _, _ = repo.List(ListFilter{Limit: 10})
	if len(rows) != 3 {
		t.Fatalf("unfiltered = %d", len(rows))
	}
}
