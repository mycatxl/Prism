package state

import (
	"database/sql"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"errors"
	"path/filepath"
	"testing"

	"prism/internal/model"
	"prism/internal/tenant"
)

func newTestTenant(t *testing.T) *TenantRepo {
	t.Helper()
	engine, closer, err := PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return engine.Tenant()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func seedUser(t *testing.T, r *TenantRepo, id, name string, role tenant.Role) {
	t.Helper()
	must(t, r.CreateUser(tenant.User{ID: id, Username: name, PasswordHash: "h", Role: role,
		Status: tenant.StatusActive, CreatedAtNs: 1, UpdatedAtNs: 1}))
}

func seedPlan(t *testing.T, r *TenantRepo, id string) tenant.Plan {
	t.Helper()
	p := tenant.Plan{ID: id, Name: "plan " + id, PlanLimits: tenant.PlanLimits{
		Platforms: []string{"p1", "p2"}, IPTiers: []string{"residential:A"}, TrafficBytes: 1 << 30,
		Period: tenant.Period30d, MaxConcurrent: 10, MaxRPS: 5, MaxKeys: 3, DurationDays: 30,
	}, Status: tenant.StatusActive, CreatedAtNs: 1, UpdatedAtNs: 1}
	must(t, r.CreatePlan(p))
	return p
}

func TestTenant_BuiltinAdminSeeded(t *testing.T) {
	r := newTestTenant(t)
	u, err := r.GetUser(tenant.BuiltinAdminUserID)
	must(t, err)
	if u.Role != tenant.RoleAdmin || u.Username != "admin" || u.Status != tenant.StatusActive {
		t.Fatalf("builtin admin = %+v", u)
	}
	if err := r.DeleteUser(tenant.BuiltinAdminUserID); err == nil {
		t.Fatal("builtin admin deleted")
	}
	role := tenant.RoleUser
	if err := r.UpdateUser(tenant.BuiltinAdminUserID, tenant.UserPatch{Role: &role}, 2); err == nil {
		t.Fatal("builtin admin demoted")
	}
}

func TestTenant_Users(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	if err := r.CreateUser(tenant.User{ID: "u_b", Username: "ALICE", Role: tenant.RoleUser, Status: "active"}); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("duplicate username (case-insensitive): %v", err)
	}
	u, err := r.GetUserByUsername("Alice")
	must(t, err)
	if u.ID != "u_a" {
		t.Fatalf("got %s", u.ID)
	}
	status, pw, totp := tenant.StatusDisabled, "new", []byte{1, 2}
	must(t, r.UpdateUser("u_a", tenant.UserPatch{Status: &status, PasswordHash: &pw, TOTPSecretEnc: &totp}, 5))
	must(t, r.TouchUserLogin("u_a", 7))
	u, _ = r.GetUser("u_a")
	if u.Status != status || u.PasswordHash != "new" || len(u.TOTPSecretEnc) != 2 || u.UpdatedAtNs != 5 || u.LastLoginAtNs != 7 {
		t.Fatalf("after update: %+v", u)
	}
	var none []byte
	must(t, r.UpdateUser("u_a", tenant.UserPatch{TOTPSecretEnc: &none}, 6))
	u, _ = r.GetUser("u_a")
	if u.TOTPSecretEnc != nil {
		t.Fatal("totp not cleared")
	}
	users, err := r.ListUsers()
	must(t, err)
	if len(users) != 2 {
		t.Fatalf("users = %d", len(users))
	}
	if err := r.UpdateUser("u_missing", tenant.UserPatch{Status: &status}, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if _, err := r.GetUser("u_missing"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
}

func TestTenant_DeleteUserCascades(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	seedPlan(t, r, "plan_1")
	must(t, r.CreateKey(tenant.AccessKey{ID: "pk_1", UserID: "u_a", SecretHash: []byte{1}, Status: "active"}))
	must(t, r.ActivateSubscription(tenant.Subscription{ID: "sub_1", UserID: "u_a", PlanID: "plan_1", Status: tenant.SubActive, ExpiresAtNs: 10}))
	must(t, r.CreateSession(tenant.Session{IDHash: "s1", UserID: "u_a", ExpiresAtNs: 100}))
	must(t, r.CreateOrder(tenant.Order{ID: "ord_1", UserID: "u_a"}))
	must(t, r.DeleteUser("u_a"))
	if n, _ := r.CountKeys("u_a"); n != 0 {
		t.Fatal("keys survived")
	}
	if subs, _ := r.ListSubscriptions("u_a"); len(subs) != 0 {
		t.Fatal("subscriptions survived")
	}
	if _, err := r.GetSession("s1", 0); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatal("session survived")
	}
	if o, _ := r.ListOrders("u_a"); len(o) != 0 {
		t.Fatal("orders survived")
	}
	if err := r.DeleteUser("u_a"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestTenant_Plans(t *testing.T) {
	r := newTestTenant(t)
	p := seedPlan(t, r, "plan_1")
	if err := r.CreatePlan(tenant.Plan{ID: "plan_2", Name: p.Name, Period: "30d", Status: "active"}); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("dup name: %v", err)
	}
	p.TrafficBytes = 42
	p.Platforms = nil
	must(t, r.UpdatePlan(p))
	got, err := r.GetPlan("plan_1")
	must(t, err)
	if got.TrafficBytes != 42 || got.Platforms == nil || len(got.Platforms) != 0 || got.IPTiers[0] != "residential:A" {
		t.Fatalf("plan = %+v", got)
	}
	if err := r.UpdatePlan(tenant.Plan{ID: "nope", Period: "30d", Status: "active"}); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	plans, _ := r.ListPlans()
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	if err := r.CreatePlan(tenant.Plan{ID: "plan_bad", Name: "bad", Period: "year", Status: "active"}); err == nil {
		t.Fatal("invalid period accepted")
	}
}

func TestTenant_Subscriptions(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	p := seedPlan(t, r, "plan_1")
	s1 := tenant.Subscription{ID: "sub_1", UserID: "u_a", PlanID: p.ID, Snapshot: p.PlanLimits,
		StartsAtNs: 1, ExpiresAtNs: 100, PeriodStartNs: 1, Status: tenant.SubActive}
	must(t, r.ActivateSubscription(s1))
	s2 := s1
	s2.ID, s2.ExpiresAtNs, s2.UpdatedAtNs = "sub_2", 200, 9
	must(t, r.ActivateSubscription(s2))

	active, err := r.GetActiveSubscription("u_a")
	must(t, err)
	if active.ID != "sub_2" || active.Snapshot.MaxKeys != 3 {
		t.Fatalf("active = %+v", active)
	}
	all, _ := r.ListSubscriptions("u_a")
	if len(all) != 2 || all[1].Status != tenant.SubExpired && all[0].Status != tenant.SubExpired {
		t.Fatalf("subs = %+v", all)
	}
	counts, _ := r.CountActiveSubscribers()
	if counts["plan_1"] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	// Raw insert of a second active row must hit the partial unique index.
	_, err = r.db.Exec(`INSERT INTO user_subscriptions (id,user_id,plan_id,plan_snapshot_json,starts_at_ns,expires_at_ns,period_start_ns,status,created_at_ns,updated_at_ns)
		VALUES ('sub_x','u_a','plan_1','{}',0,0,0,'active',0,0)`)
	if !isConflict(err) {
		t.Fatalf("second active subscription: %v", err)
	}

	must(t, r.FlushUsage(nil, map[string]int64{"sub_2": 500}))
	must(t, r.ResetPeriod("sub_2", 50, 51))
	active, _ = r.GetActiveSubscription("u_a")
	if active.PeriodUsedBytes != 0 || active.PeriodStartNs != 50 {
		t.Fatalf("reset: %+v", active)
	}
	must(t, r.ExtendSubscription("sub_2", 300, 52))
	lim := p.PlanLimits
	lim.TrafficBytes = 7
	n, err := r.SyncPlanSnapshot("plan_1", lim, 53)
	must(t, err)
	if n != 1 {
		t.Fatalf("synced %d", n)
	}
	active, _ = r.GetActiveSubscription("u_a")
	if active.ExpiresAtNs != 300 || active.Snapshot.TrafficBytes != 7 {
		t.Fatalf("after extend/sync: %+v", active)
	}

	users, err := r.ExpireDue(299)
	must(t, err)
	if len(users) != 0 {
		t.Fatalf("expired early: %v", users)
	}
	users, err = r.ExpireDue(300)
	must(t, err)
	if len(users) != 1 || users[0] != "u_a" {
		t.Fatalf("expired = %v", users)
	}
	if _, err := r.GetActiveSubscription("u_a"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("still active: %v", err)
	}
	must(t, r.SetSubscriptionStatus("sub_2", tenant.SubSuspended, 1))
	if err := r.SetSubscriptionStatus("nope", tenant.SubSuspended, 1); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTenant_KeysScopedByOwner(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	seedUser(t, r, "u_b", "bob", tenant.RoleUser)
	must(t, r.CreateKey(tenant.AccessKey{ID: "pk_a", UserID: "u_a", Name: "a", SecretHash: []byte{1},
		Platforms: []string{"p1"}, Status: "active", CreatedAtNs: 1}))
	if err := r.CreateKey(tenant.AccessKey{ID: "pk_a", UserID: "u_b", SecretHash: []byte{1}, Status: "active"}); !errors.Is(err, tenant.ErrConflict) {
		t.Fatalf("dup key: %v", err)
	}

	// IDOR: bob cannot read, edit or delete alice's key.
	if _, err := r.GetKey("u_b", "pk_a"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("cross-user get: %v", err)
	}
	name := "x"
	if err := r.UpdateKey("u_b", "pk_a", tenant.KeyPatch{Name: &name}, 2); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("cross-user update: %v", err)
	}
	if err := r.DeleteKey("u_b", "pk_a"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("cross-user delete: %v", err)
	}
	if keys, _ := r.ListKeys("u_b"); len(keys) != 0 {
		t.Fatal("bob sees alice's keys")
	}

	allow := []string{"10.0.0.0/8"}
	var clear []string
	must(t, r.UpdateKey("u_a", "pk_a", tenant.KeyPatch{Name: &name, IPAllowlist: &allow, Platforms: &clear}, 3))
	k, err := r.GetKey("u_a", "pk_a")
	must(t, err)
	if k.Scope != tenant.ScopeProxy {
		t.Fatalf("default scope = %q", k.Scope)
	}
	if k.Name != "x" || k.Platforms != nil || len(k.IPAllowlist) != 1 || k.UpdatedAtNs != 3 {
		t.Fatalf("key = %+v", k)
	}
	must(t, r.TouchKeysUsed(map[string]int64{"pk_a": 99}))
	must(t, r.TouchKeysUsed(map[string]int64{"pk_a": 50})) // never moves backwards
	k, _ = r.GetKey("u_a", "pk_a")
	if k.LastUsedNs != 99 {
		t.Fatalf("last used = %d", k.LastUsedNs)
	}
	if n, _ := r.CountKeys("u_a"); n != 1 {
		t.Fatalf("count = %d", n)
	}
	must(t, r.DeleteKey("u_a", "pk_a"))
}

func TestTenant_AuthSnapshot(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	p := seedPlan(t, r, "plan_1")
	must(t, r.CreateKey(tenant.AccessKey{ID: "pk_a", UserID: "u_a", SecretHash: []byte{9}, Status: "active"}))
	must(t, r.CreateKey(tenant.AccessKey{ID: "pk_adm", UserID: tenant.BuiltinAdminUserID, Scope: tenant.ScopeAdmin, SecretHash: []byte{8}, Status: "active"}))
	if err := r.CreateKey(tenant.AccessKey{ID: "pk_bad", UserID: "u_a", Scope: "root", SecretHash: []byte{1}, Status: "active"}); err == nil {
		t.Fatal("invalid scope accepted")
	}
	must(t, r.ActivateSubscription(tenant.Subscription{ID: "sub_1", UserID: "u_a", PlanID: p.ID,
		Snapshot: p.PlanLimits, ExpiresAtNs: 100, Status: tenant.SubActive}))

	recs, err := r.LoadAuthSnapshot()
	must(t, err)
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	rec, err := r.LoadAuthRecord("pk_a")
	must(t, err)
	if rec.UserRole != tenant.RoleUser || rec.Subscription == nil || rec.Subscription.ID != "sub_1" ||
		rec.Subscription.Snapshot.Platforms[1] != "p2" {
		t.Fatalf("record = %+v", rec)
	}
	rec, err = r.LoadAuthRecord("pk_adm")
	must(t, err)
	if rec.UserRole != tenant.RoleAdmin || rec.Subscription != nil || rec.Key.Scope != tenant.ScopeAdmin {
		t.Fatalf("admin record = %+v", rec)
	}
	if _, err := r.LoadAuthRecord("pk_none"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTenant_UsageFlushRollupQuery(t *testing.T) {
	r := newTestTenant(t)
	const day = 86400
	base := int64(1000 * day)
	must(t, r.FlushUsage([]tenant.UsageDelta{
		{KeyID: "pk_a", UserID: "u_a", PlatformID: "p1", HourTs: base + 10, IngressBytes: 100, EgressBytes: 10, Requests: 1},
		{KeyID: "pk_a", UserID: "u_a", PlatformID: "p1", HourTs: base + 20, IngressBytes: 100, EgressBytes: 10, Requests: 1},
		{KeyID: "pk_a", UserID: "u_a", PlatformID: "p1", HourTs: base + 3600, IngressBytes: 5, Requests: 1, Errors: 1},
		{KeyID: "pk_b", UserID: "u_b", PlatformID: "p2", HourTs: base + day, IngressBytes: 7},
		{KeyID: "pk_b", UserID: "u_b", PlatformID: "p2", HourTs: base + day}, // empty: skipped
	}, nil))
	var rows int
	must(t, r.db.QueryRow(`SELECT COUNT(*) FROM usage_hourly`).Scan(&rows))
	if rows != 3 {
		t.Fatalf("hourly rows = %d, want 3 (no empty rows)", rows)
	}

	got, err := r.QueryUsage(tenant.UsageFilter{UserID: "u_a"})
	must(t, err)
	if len(got) != 2 || got[0].BucketTs != base || got[0].IngressBytes != 200 || got[0].Requests != 2 || got[1].Errors != 1 {
		t.Fatalf("hourly query = %+v", got)
	}

	// Roll up the first day only.
	moved, err := r.RollupUsage(base + day)
	must(t, err)
	if moved != 2 {
		t.Fatalf("moved = %d", moved)
	}
	moved, _ = r.RollupUsage(base + day) // idempotent
	if moved != 0 {
		t.Fatalf("second rollup moved %d", moved)
	}

	got, err = r.QueryUsage(tenant.UsageFilter{Daily: true, GroupBy: tenant.GroupUser})
	must(t, err)
	if len(got) != 2 || got[0].UserID != "u_a" || got[0].BucketTs != base || got[0].IngressBytes != 205 ||
		got[0].Requests != 3 || got[1].UserID != "u_b" || got[1].IngressBytes != 7 {
		t.Fatalf("daily merged query = %+v", got)
	}
	got, _ = r.QueryUsage(tenant.UsageFilter{GroupBy: tenant.GroupPlatform, FromTs: base + day})
	if len(got) != 1 || got[0].PlatformID != "p2" {
		t.Fatalf("from filter = %+v", got)
	}
	got, _ = r.QueryUsage(tenant.UsageFilter{KeyID: "pk_a", PlatformID: "p1", ToTs: base + day, GroupBy: tenant.GroupKey})
	if len(got) != 1 || got[0].KeyID != "pk_a" || got[0].IngressBytes != 205 {
		t.Fatalf("key filter = %+v", got)
	}

	n, err := r.PruneUsageDaily(base + 1)
	must(t, err)
	if n != 1 {
		t.Fatalf("pruned = %d", n)
	}
}

func TestTenant_InvitesOrdersSessions(t *testing.T) {
	r := newTestTenant(t)
	seedUser(t, r, "u_a", "alice", tenant.RoleUser)
	seedPlan(t, r, "plan_1")
	must(t, r.CreateInvite(tenant.Invite{CodeHash: "c1", PlanID: "plan_1", MaxUses: 2, ExpiresAtNs: 100}))
	must(t, r.CreateInvite(tenant.Invite{CodeHash: "c2", MaxUses: 1}))
	if _, err := r.RedeemInvite("c1", 100); !errors.Is(err, tenant.ErrInviteUnavailable) {
		t.Fatalf("expired invite: %v", err)
	}
	for i := 0; i < 2; i++ {
		inv, err := r.RedeemInvite("c1", 50)
		must(t, err)
		if inv.PlanID != "plan_1" || inv.UsedCount != i+1 {
			t.Fatalf("redeem %d = %+v", i, inv)
		}
	}
	if _, err := r.RedeemInvite("c1", 50); !errors.Is(err, tenant.ErrInviteUnavailable) {
		t.Fatalf("used-up invite: %v", err)
	}
	inv, err := r.RedeemInvite("c2", 1<<62) // no expiry
	must(t, err)
	if inv.PlanID != "" {
		t.Fatalf("plan = %q", inv.PlanID)
	}
	invs, _ := r.ListInvites()
	if len(invs) != 2 {
		t.Fatal("list invites")
	}
	must(t, r.DeleteInvite("c2"))
	if err := r.DeleteInvite("c2"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatal(err)
	}

	must(t, r.CreateOrder(tenant.Order{ID: "ord_1", UserID: "u_a", PlanID: "plan_1", Amount: "10", CreatedAtNs: 1}))
	orders, _ := r.ListOrders("u_a")
	if len(orders) != 1 || orders[0].Amount != "10" {
		t.Fatalf("orders = %+v", orders)
	}

	must(t, r.CreateSession(tenant.Session{IDHash: "h1", UserID: "u_a", CreatedAtNs: 1, ExpiresAtNs: 10}))
	must(t, r.CreateSession(tenant.Session{IDHash: "h2", UserID: "u_a", CreatedAtNs: 2, ExpiresAtNs: 20}))
	if _, err := r.GetSession("h1", 10); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatal("expired session returned")
	}
	must(t, r.ExtendSession("h1", 30))
	if s, err := r.GetSession("h1", 10); err != nil || s.UserID != "u_a" {
		t.Fatalf("extended: %v %+v", err, s)
	}
	ss, _ := r.ListUserSessions("u_a", 25)
	if len(ss) != 1 {
		t.Fatalf("live sessions = %d", len(ss))
	}
	n, _ := r.PruneSessions(25)
	if n != 1 {
		t.Fatalf("pruned %d", n)
	}
	must(t, r.DeleteSession("h1"))
	must(t, r.CreateSession(tenant.Session{IDHash: "h3", UserID: "u_a", ExpiresAtNs: 99}))
	n, _ = r.DeleteUserSessions("u_a")
	if n != 1 {
		t.Fatalf("deleted %d", n)
	}
}

func TestTenant_Audit(t *testing.T) {
	r := newTestTenant(t)
	must(t, r.AppendAudit(model.AuditEntry{AtNs: 1, Actor: "user:alice", Action: "key.create",
		Target: "pk_1", ActorUserID: "u_a", TargetType: "access_key", TargetID: "pk_1"}))
	must(t, r.AppendAudit(model.AuditEntry{AtNs: 2, Actor: "admin", Action: "x"}))
	got, err := r.ListAuditByActor("u_a", 0, 10)
	must(t, err)
	if len(got) != 1 || got[0].TargetType != "access_key" || got[0].TargetID != "pk_1" || got[0].Detail != "{}" {
		t.Fatalf("audit = %+v", got)
	}
}

func TestTenant_RunMaintenance(t *testing.T) {
	r := newTestTenant(t)
	must(t, r.FlushUsage([]tenant.UsageDelta{{KeyID: "k", UserID: "u", PlatformID: "p", HourTs: 3600, IngressBytes: 1}}, nil))
	must(t, tenant.RunMaintenance(r, tenant.Retention{HourlyDays: 30, DailyDays: 400}, timeAt(60*86400)))
	var hourly, daily int
	must(t, r.db.QueryRow(`SELECT COUNT(*) FROM usage_hourly`).Scan(&hourly))
	must(t, r.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&daily))
	if hourly != 0 || daily != 1 {
		t.Fatalf("hourly=%d daily=%d", hourly, daily)
	}
	must(t, tenant.RunMaintenance(r, tenant.Retention{HourlyDays: 30, DailyDays: 400}, timeAt(500*86400)))
	must(t, r.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&daily))
	if daily != 0 {
		t.Fatalf("daily after prune = %d", daily)
	}
}

// TestMigrate_V010UpgradeKeepsLeasesAndAudit upgrades databases shaped like
// v0.1.0 (state version 16, cache version 2) and checks that existing leases
// move to the built-in admin and existing audit rows survive.
func TestMigrate_V010UpgradeKeepsLeasesAndAudit(t *testing.T) {
	stateDir, cacheDir := t.TempDir(), t.TempDir()

	sdb, err := OpenDB(filepath.Join(stateDir, "state.db"))
	must(t, err)
	must(t, newStateMigrator(t, sdb).Migrate(stateVersionDropExportProfiles))
	_, err = sdb.Exec(`INSERT INTO audit_log (at_ns, actor, remote_addr, action, target) VALUES (1,'admin','127.0.0.1','platform.update','p1')`)
	must(t, err)
	defer sdb.Close()

	cdb, err := OpenDB(filepath.Join(cacheDir, "cache.db"))
	must(t, err)
	must(t, newCacheMigrator(t, cdb).Migrate(2))
	_, err = cdb.Exec(`INSERT INTO leases (platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
		VALUES ('p1','acct','node1','1.2.3.4',1,2,3)`)
	must(t, err)
	defer cdb.Close()

	// Consistency repair would drop these leases as orphans (no such
	// platform/node), so migrate directly instead of bootstrapping.
	must(t, MigrateStateDB(sdb))
	must(t, MigrateCacheDB(cdb))
	engine := newStateEngine(newStateRepo(sdb), newCacheRepo(cdb))

	leases, err := engine.LoadAllLeases()
	must(t, err)
	if len(leases) != 1 || leases[0].UserID != tenant.BuiltinAdminUserID || leases[0].EgressIP != "1.2.3.4" ||
		leases[0].NodeHash != "node1" || leases[0].ExpiryNs != 2 {
		t.Fatalf("leases = %+v", leases)
	}
	audit, err := engine.ListAudit(0, 10)
	must(t, err)
	if len(audit) != 1 || audit[0].Action != "platform.update" || audit[0].ActorUserID != "" {
		t.Fatalf("audit = %+v", audit)
	}
	if _, err := engine.Tenant().GetUser(tenant.BuiltinAdminUserID); err != nil {
		t.Fatal(err)
	}

	// Lease writes without a user land on the built-in admin and replace the
	// migrated row instead of duplicating it.
	must(t, engine.BulkUpsertLeases([]model.Lease{{PlatformID: "p1", Account: "acct", NodeHash: "node2", ExpiryNs: 5, LastAccessedNs: 5}}))
	leases, _ = engine.LoadAllLeases()
	if len(leases) != 1 || leases[0].NodeHash != "node2" {
		t.Fatalf("after upsert: %+v", leases)
	}
	must(t, engine.BulkDeleteLeases([]model.LeaseKey{{PlatformID: "p1", Account: "acct"}}))
	leases, _ = engine.LoadAllLeases()
	if len(leases) != 0 {
		t.Fatalf("after delete: %+v", leases)
	}
}

func TestMigrate_StateDownUpRoundTrip(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "state.db"))
	must(t, err)
	defer db.Close()
	m := newStateMigrator(t, db)
	must(t, m.Up())
	must(t, m.Migrate(stateVersionDropExportProfiles))
	if ok, _ := hasTable(db, "users"); ok {
		t.Fatal("users survived down migration")
	}
	must(t, m.Up())
	assertStateMigrationVersion(t, db, stateLatestVersion)
}

func timeAt(sec int64) time.Time { return time.Unix(sec, 0) }

func newCacheMigrator(t *testing.T, db *sql.DB) *migrate.Migrate {
	t.Helper()
	src, err := iofs.New(migrationsFS, cacheMigrationsPath)
	must(t, err)
	drv, err := migratesqlite.WithInstance(db, &migratesqlite.Config{MigrationsTable: migrateDefaultTable})
	must(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	must(t, err)
	return m
}
