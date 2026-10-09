package tenant

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPepper_CreateLoadAndEnv(t *testing.T) {
	t.Setenv(PepperEnv, "")
	dir := filepath.Join(t.TempDir(), "state")
	p1, err := LoadOrCreatePepper(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != 32 {
		t.Fatalf("len = %d", len(p1))
	}
	path := filepath.Join(dir, PepperFileName)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	p2, err := LoadOrCreatePepper(dir)
	if err != nil || !bytes.Equal(p1, p2) {
		t.Fatalf("reload: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal("loose mode not repaired")
	}

	env := bytes.Repeat([]byte{7}, 32)
	t.Setenv(PepperEnv, base64.StdEncoding.EncodeToString(env))
	p3, err := LoadOrCreatePepper(dir)
	if err != nil || !bytes.Equal(p3, env) {
		t.Fatalf("env override: %v", err)
	}
	t.Setenv(PepperEnv, base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := LoadOrCreatePepper(dir); err == nil {
		t.Fatal("short pepper accepted")
	}
	t.Setenv(PepperEnv, "!!not base64!!")
	if _, err := LoadOrCreatePepper(dir); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

func TestPepper_ConcurrentCreateAgrees(t *testing.T) {
	t.Setenv(PepperEnv, "")
	dir := t.TempDir()
	var wg sync.WaitGroup
	out := make([][]byte, 8)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := LoadOrCreatePepper(dir)
			if err != nil {
				t.Error(err)
			}
			out[i] = p
		}(i)
	}
	wg.Wait()
	for _, p := range out[1:] {
		if !bytes.Equal(p, out[0]) {
			t.Fatal("peppers differ")
		}
	}
}

func TestDeriveKeys_Independent(t *testing.T) {
	k, err := DeriveKeys(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k.KeyHMAC, k.TOTP) || bytes.Equal(k.KeyHMAC, k.Session) || bytes.Equal(k.TOTP, k.Session) {
		t.Fatal("subkeys collide")
	}
	k2, _ := DeriveKeys(bytes.Repeat([]byte{1}, 32))
	if !bytes.Equal(k.KeyHMAC, k2.KeyHMAC) {
		t.Fatal("not deterministic")
	}
	k3, _ := DeriveKeys(bytes.Repeat([]byte{2}, 32))
	if bytes.Equal(k.KeyHMAC, k3.KeyHMAC) {
		t.Fatal("pepper ignored")
	}
}

func TestKeyFormat(t *testing.T) {
	id, secret := NewKeySecret()
	if !strings.HasPrefix(id, PrefixKey) || strings.Contains(id[len(PrefixKey):], "_") || strings.Contains(secret, "_") {
		t.Fatalf("id=%q secret=%q", id, secret)
	}
	gotID, gotSecret, ok := ParseKey(FormatKey(id, secret))
	if !ok || gotID != id || gotSecret != secret {
		t.Fatal("round trip")
	}
	for _, bad := range []string{"", "pk_", "pk__x", "pk_abc_", "xx_abc_def", "pk_abc"} {
		if _, _, ok := ParseKey(bad); ok {
			t.Errorf("ParseKey(%q) ok", bad)
		}
	}
	if NewID("u_") == NewID("u_") {
		t.Fatal("ids repeat")
	}
}

func FuzzParseKey(f *testing.F) {
	f.Add("pk_abc_def")
	f.Add("pk___")
	f.Fuzz(func(t *testing.T, s string) {
		id, secret, ok := ParseKey(s)
		if ok && FormatKey(id, secret) != s {
			t.Fatalf("round trip %q", s)
		}
	})
}

type fixture struct {
	cache  *KeyCache
	creds  map[string]string
	hmac   []byte
	recAdm AuthRecord
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	keys, _ := DeriveKeys(bytes.Repeat([]byte{3}, 32))
	f := &fixture{cache: NewKeyCache(keys.KeyHMAC), creds: map[string]string{}, hmac: keys.KeyHMAC}
	mk := func(name, user string, role Role, sub *Subscription, mod func(*AccessKey)) AuthRecord {
		id, secret := NewKeySecret()
		f.creds[name] = FormatKey(id, secret)
		k := AccessKey{ID: id, UserID: user, SecretHash: HashSecret(keys.KeyHMAC, secret), Status: StatusActive}
		if mod != nil {
			mod(&k)
		}
		return AuthRecord{Key: k, UserStatus: StatusActive, UserRole: role, Subscription: sub}
	}
	sub := &Subscription{ID: "sub", UserID: "u_a", Status: SubActive, ExpiresAtNs: 1000,
		Snapshot: PlanLimits{Platforms: []string{"p1", "p2"}}}
	recs := []AuthRecord{
		mk("a", "u_a", RoleUser, sub, nil),
		mk("a_p1", "u_a", RoleUser, sub, func(k *AccessKey) { k.Platforms = []string{"p1", "p9"} }),
		mk("a_ip", "u_a", RoleUser, sub, func(k *AccessKey) { k.IPAllowlist = []string{"10.0.0.0/8", "2001:db8::1"} }),
		mk("a_exp", "u_a", RoleUser, sub, func(k *AccessKey) { k.ExpiresAtNs = 50 }),
		mk("a_off", "u_a", RoleUser, sub, func(k *AccessKey) { k.Status = StatusDisabled }),
		mk("nosub", "u_n", RoleUser, nil, nil),
		mk("u_admkey", "u_a", RoleUser, sub, func(k *AccessKey) { k.Scope = ScopeAdmin }),
		mk("adm", BuiltinAdminUserID, RoleAdmin, nil, func(k *AccessKey) { k.Scope = ScopeAdmin }),
	}
	f.recAdm = recs[len(recs)-1]
	if err := f.cache.Load(recs); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestKeyCache_Authenticate(t *testing.T) {
	f := newFixture(t)
	c := f.cache
	if c.Len() != 8 {
		t.Fatalf("len = %d", c.Len())
	}
	ok := func(name string, now int64) *KeyState {
		t.Helper()
		ks, err := c.Authenticate(f.creds[name], now)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return ks
	}
	fail := func(cred string, now int64) {
		t.Helper()
		if _, err := c.Authenticate(cred, now); !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("%q: err = %v", cred, err)
		}
	}
	ks := ok("a", 10)
	if ks.LastUsedNs() != 10 || ks.User.ID != "u_a" || ks.User.Role() != RoleUser {
		t.Fatalf("state = %+v", ks)
	}
	fail(f.creds["a"]+"x", 10)
	fail("pk_unknown_secret", 10)
	fail("garbage", 10)
	fail(f.creds["a_off"], 10)
	ok("a_exp", 49)
	fail(f.creds["a_exp"], 50)
	fail(f.creds["nosub"], 10)
	fail(f.creds["a"], 1000)                  // subscription expired by time
	if ok("adm", 1<<62).Scope != ScopeAdmin { // admin: no subscription needed
		t.Fatal("scope")
	}
	if ok("a", 10).Scope != ScopeProxy {
		t.Fatal("default scope")
	}
	fail(f.creds["u_admkey"], 10) // admin scope on a non-admin user

	c.SetUserActive("u_a", false)
	fail(f.creds["a"], 10)
	c.SetUserActive("u_a", true)
	c.SetSubscription("u_a", nil)
	fail(f.creds["a"], 10)
	c.SetSubscription("u_a", &Subscription{Status: SubActive, ExpiresAtNs: 5000, Snapshot: PlanLimits{Platforms: []string{"*"}}})
	ks = ok("a", 10)
	if !ks.AllowsPlatform("anything") {
		t.Fatal("wildcard plan")
	}

	drained := c.DrainLastUsed(9)
	if len(drained) != 3 { // a, a_exp, adm
		t.Fatalf("drained = %v", drained)
	}
}

func TestKeyCache_PlatformAndSource(t *testing.T) {
	f := newFixture(t)
	c := f.cache
	a, _ := c.Authenticate(f.creds["a"], 1)
	if !a.AllowsPlatform("p1") || !a.AllowsPlatform("p2") || a.AllowsPlatform("p3") {
		t.Fatal("plan platforms")
	}
	ap, _ := c.Authenticate(f.creds["a_p1"], 1)
	if !ap.AllowsPlatform("p1") || ap.AllowsPlatform("p2") || ap.AllowsPlatform("p9") {
		t.Fatal("plan ∩ key")
	}
	adm, _ := c.Authenticate(f.creds["adm"], 1)
	if !adm.AllowsPlatform("whatever") {
		t.Fatal("admin unrestricted")
	}
	ip, _ := c.Authenticate(f.creds["a_ip"], 1)
	for addr, want := range map[string]bool{
		"10.1.2.3": true, "::ffff:10.1.2.3": true, "11.0.0.1": false,
		"2001:db8::1": true, "2001:db8::2": false,
	} {
		if got := ip.AllowsSource(netip.MustParseAddr(addr)); got != want {
			t.Errorf("AllowsSource(%s) = %v", addr, got)
		}
	}
	if !a.AllowsSource(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("no allowlist must allow all")
	}
}

func TestKeyCache_PutRemove(t *testing.T) {
	f := newFixture(t)
	c := f.cache
	adm, _ := c.Authenticate(f.creds["adm"], 1)

	// Put replaces a key and keeps one shared UserState per user.
	rec := f.recAdm
	rec.Key.Status = StatusDisabled
	if err := c.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(f.creds["adm"], 1); !errors.Is(err, ErrAuthFailed) {
		t.Fatal("disabled by Put")
	}
	if c.User(BuiltinAdminUserID) != adm.User {
		t.Fatal("user state replaced")
	}
	if got := c.Remove(rec.Key.ID); got == nil || got.ID != rec.Key.ID {
		t.Fatal("remove")
	}
	if len(c.UserKeys("u_a")) != 6 {
		t.Fatal("user keys")
	}
	removed := c.RemoveUser("u_a")
	if len(removed) != 6 || c.Len() != 1 || c.User("u_a") != nil {
		t.Fatalf("remove user: %d left", c.Len())
	}
	bad := f.recAdm
	bad.Key.IPAllowlist = []string{"nope"}
	if err := c.Put(bad); err == nil {
		t.Fatal("bad allowlist accepted")
	}
}

func TestKeyCache_ConcurrentAuthAndUpdate(t *testing.T) {
	f := newFixture(t)
	c := f.cache
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = c.Authenticate(f.creds["a"], 10)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.SetUserActive("u_a", j%2 == 0)
				_ = c.Put(f.recAdm)
			}
		}()
	}
	wg.Wait()
}

func BenchmarkKeyCacheAuthenticate(b *testing.B) {
	keys, _ := DeriveKeys(bytes.Repeat([]byte{3}, 32))
	c := NewKeyCache(keys.KeyHMAC)
	var recs []AuthRecord
	var cred string
	for i := 0; i < 10000; i++ {
		id, secret := NewKeySecret()
		cred = FormatKey(id, secret)
		recs = append(recs, AuthRecord{Key: AccessKey{ID: id, UserID: BuiltinAdminUserID,
			SecretHash: HashSecret(keys.KeyHMAC, secret), Status: StatusActive}, UserStatus: StatusActive, UserRole: RoleAdmin})
	}
	if err := c.Load(recs); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := c.Authenticate(cred, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
}

type fakeStore struct {
	Store
	recs  []AuthRecord
	calls chan string
}

func (f *fakeStore) LoadAuthSnapshot() ([]AuthRecord, error) { return f.recs, nil }
func (f *fakeStore) RollupUsage(int64) (int64, error)        { f.calls <- "rollup"; return 0, nil }
func (f *fakeStore) PruneUsageDaily(int64) (int64, error)    { return 0, nil }
func (f *fakeStore) PruneSessions(int64) (int64, error)      { return 0, nil }

func TestBootstrapAndMaintenance(t *testing.T) {
	t.Setenv(PepperEnv, "")
	dir := t.TempDir()
	pepper, _ := LoadOrCreatePepper(dir)
	keys, _ := DeriveKeys(pepper)
	id, secret := NewKeySecret()
	fs := &fakeStore{calls: make(chan string, 4), recs: []AuthRecord{{
		Key:        AccessKey{ID: id, UserID: "u", SecretHash: HashSecret(keys.KeyHMAC, secret), Status: StatusActive},
		UserStatus: StatusActive, UserRole: RoleAdmin,
	}}}
	rt, err := Bootstrap(fs, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Cache.Authenticate(FormatKey(id, secret), 1); err != nil {
		t.Fatal(err)
	}
	stop := StartMaintenance(fs, Retention{HourlyDays: 30, DailyDays: 400}, time.Hour)
	<-fs.calls // runs once immediately
	stop()
	stop()
}
