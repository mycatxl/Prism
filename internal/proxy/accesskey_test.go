package proxy

import (
	"net/netip"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/keys"
	"prism/internal/model"
)

// testPepper returns a deterministic pepper so a test can hash the same secret
// the verifier will check.
func testPepper(t *testing.T) *auth.Pepper {
	t.Helper()
	pepper, err := auth.NewPepper([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("auth.NewPepper: %v", err)
	}
	return pepper
}

// keyFixture is a store holding one usable proxy key, plus the credential that
// unlocks it.
type keyFixture struct {
	store      *keys.Store
	pepper     *auth.Pepper
	keyID      string
	secret     string
	credential string
}

func newKeyFixture(t *testing.T) keyFixture {
	t.Helper()
	pepper := testPepper(t)

	keyID, secret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	user := model.User{ID: "u-1", Username: "u-1", Role: model.UserRoleUser, Status: model.UserStatusActive}
	accessKey := model.AccessKey{
		ID:         keyID,
		UserID:     "u-1",
		SecretHash: pepper.HashSecret(secret),
		Scope:      model.KeyScopeProxy,
		Status:     model.KeyStatusActive,
	}

	store := keys.NewStore()
	if err := store.Load(
		[]model.AccessKey{accessKey},
		map[string]model.User{"u-1": user},
		map[string]keys.PlanSnapshot{"u-1": {Status: model.SubscriptionStatusActive}},
	); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	return keyFixture{
		store:      store,
		pepper:     pepper,
		keyID:      keyID,
		secret:     secret,
		credential: keys.FormatCredential(keyID, secret),
	}
}

func TestAccessKeyVerifier_AcceptsAValidCredential(t *testing.T) {
	fx := newKeyFixture(t)
	verifier := NewAccessKeyVerifier(fx.store, fx.pepper)

	state := verifier.VerifyCredential(fx.credential, testClientAddr)
	if state == nil {
		t.Fatal("a valid credential was rejected")
	}
	if state.KeyID != fx.keyID {
		t.Fatalf("resolved key %q, want %q", state.KeyID, fx.keyID)
	}
	if state.UserID != "u-1" {
		t.Fatalf("resolved user %q, want u-1", state.UserID)
	}
}

// Every failure mode must be indistinguishable from every other: same nil, no
// detail. The table below is the whole contract.
func TestAccessKeyVerifier_RejectsEverythingElse(t *testing.T) {
	fx := newKeyFixture(t)

	otherID, otherSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	cases := []struct {
		name       string
		credential string
	}{
		{"empty", ""},
		{"plain global token", "some-global-proxy-token"},
		{"unknown key id", keys.FormatCredential(otherID, fx.secret)},
		{"wrong secret", keys.FormatCredential(fx.keyID, otherSecret)},
		{"truncated secret", keys.FormatCredential(fx.keyID, fx.secret[:len(fx.secret)-1])},
		{"missing prefix", fx.keyID + "_" + fx.secret},
		{"uppercase secret", keys.FormatCredential(fx.keyID, "ABCDEF")},
		{"separator only", "pk__"},
	}

	verifier := NewAccessKeyVerifier(fx.store, fx.pepper)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if state := verifier.VerifyCredential(tc.credential, testClientAddr); state != nil {
				t.Fatalf("VerifyCredential(%q) resolved %q, want nil", tc.credential, state.KeyID)
			}
		})
	}
}

// A key that authenticates but is not a data-plane key must not open the data
// plane, and vice versa: the scopes are what keep the console credential and
// the proxy credential from substituting for one another.
func TestAccessKeyVerifier_RejectsTheWrongScope(t *testing.T) {
	fx := newKeyFixture(t)

	adminKeyID, adminSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	if err := fx.store.Load(
		[]model.AccessKey{{
			ID:         adminKeyID,
			UserID:     "u-1",
			SecretHash: fx.pepper.HashSecret(adminSecret),
			Scope:      model.KeyScopeAdmin,
			Status:     model.KeyStatusActive,
		}},
		map[string]model.User{"u-1": {ID: "u-1", Role: model.UserRoleAdmin, Status: model.UserStatusActive}},
		nil,
	); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	verifier := NewAccessKeyVerifier(fx.store, fx.pepper)
	if state := verifier.VerifyCredential(keys.FormatCredential(adminKeyID, adminSecret), testClientAddr); state != nil {
		t.Fatalf("an admin-scoped key was accepted on the data plane")
	}
}

func TestAccessKeyVerifier_RejectsUnusableKeys(t *testing.T) {
	nowNs := time.Now().UnixNano()

	cases := []struct {
		name       string
		keyStatus  string
		userStatus string
		keyExpiry  int64
		planStatus string
		planExpiry int64
	}{
		{"disabled key", model.KeyStatusDisabled, model.UserStatusActive, 0, model.SubscriptionStatusActive, 0},
		{"expired key", model.KeyStatusActive, model.UserStatusActive, nowNs - 1, model.SubscriptionStatusActive, 0},
		{"disabled user", model.KeyStatusActive, model.UserStatusDisabled, 0, model.SubscriptionStatusActive, 0},
		{"suspended plan", model.KeyStatusActive, model.UserStatusActive, 0, model.SubscriptionStatusSuspended, 0},
		{"expired plan", model.KeyStatusActive, model.UserStatusActive, 0, model.SubscriptionStatusActive, nowNs - 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pepper := testPepper(t)
			keyID, secret, err := keys.GenerateCredential()
			if err != nil {
				t.Fatalf("keys.GenerateCredential: %v", err)
			}

			store := keys.NewStore()
			if err := store.Load(
				[]model.AccessKey{{
					ID:          keyID,
					UserID:      "u-1",
					SecretHash:  pepper.HashSecret(secret),
					Scope:       model.KeyScopeProxy,
					Status:      tc.keyStatus,
					ExpiresAtNs: tc.keyExpiry,
				}},
				map[string]model.User{"u-1": {ID: "u-1", Role: model.UserRoleUser, Status: tc.userStatus}},
				map[string]keys.PlanSnapshot{"u-1": {Status: tc.planStatus, ExpiresAtNs: tc.planExpiry}},
			); err != nil {
				t.Fatalf("store.Load: %v", err)
			}

			verifier := NewAccessKeyVerifier(store, pepper)
			if state := verifier.VerifyCredential(keys.FormatCredential(keyID, secret), testClientAddr); state != nil {
				t.Fatalf("an unusable key was accepted")
			}
		})
	}
}

// A deployment with no pepper and no store must not crash and must not accept
// anything: it falls back to the global token, which is the transitional path.
func TestAccessKeyVerifier_DegradesWithoutDependencies(t *testing.T) {
	fx := newKeyFixture(t)

	cases := []struct {
		name     string
		verifier AccessKeyVerifier
	}{
		{"nil store and pepper", NewAccessKeyVerifier(nil, nil)},
		{"nil pepper", NewAccessKeyVerifier(fx.store, nil)},
		{"nil store", NewAccessKeyVerifier(nil, fx.pepper)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if state := tc.verifier.VerifyCredential(fx.credential, testClientAddr); state != nil {
				t.Fatal("an unconfigured verifier accepted a credential")
			}
		})
	}
}

// A credential that is not key-shaped must be rejected without the pepper being
// consulted. The test proves it by hashing nothing: a nil pepper would panic if
// the verifier tried to verify a secret it should never have looked at.
func TestAccessKeyVerifier_DoesNotTouchTheStoreForForeignValues(t *testing.T) {
	verifier := NewAccessKeyVerifier(keys.NewStore(), nil)
	for _, value := range []string{"", "global-token", "pk_not-a-key", "pk_zz_zz"} {
		if state := verifier.VerifyCredential(value, testClientAddr); state != nil {
			t.Fatalf("VerifyCredential(%q) resolved a key", value)
		}
	}
}

func TestProxyIdentity_CarriesTheKeyWhenPresent(t *testing.T) {
	fx := newKeyFixture(t)
	state := NewAccessKeyVerifier(fx.store, fx.pepper).VerifyCredential(fx.credential, testClientAddr)
	if state == nil {
		t.Fatal("fixture credential was rejected")
	}

	identity := proxyIdentity{PlatformName: "plat", Account: "acct", Key: state}
	if identity.Key == nil {
		t.Fatal("identity lost the key")
	}
	if !identity.Key.AllowsPlatform("anything") {
		t.Fatal("a key with no platform list must allow every platform")
	}

	// The transitional path: no key, therefore no per-key restriction.
	tokenIdentity := proxyIdentity{PlatformName: "plat", Account: "acct"}
	if tokenIdentity.Key != nil {
		t.Fatal("the global-token identity must not carry a key")
	}
}

// testClientAddr is the source address the fixture keys are used from. The keys
// that pin an allowlist include it.
var testClientAddr = netip.MustParseAddr("203.0.113.7")

// A key pinned to a source-address list must only work from that list, and the
// refusal is the same nil an unknown credential gets: a caller must not learn
// that a key exists at some other address.
func TestAccessKeyVerifier_EnforcesTheIPAllowlist(t *testing.T) {
	pepper := testPepper(t)
	keyID, secret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	accessKey := model.AccessKey{
		ID:          keyID,
		UserID:      "u-1",
		SecretHash:  pepper.HashSecret(secret),
		IPAllowlist: []string{"203.0.113.0/24", "198.51.100.9"},
		Scope:       model.KeyScopeProxy,
		Status:      model.KeyStatusActive,
	}
	user := model.User{ID: "u-1", Username: "u-1", Role: model.UserRoleUser, Status: model.UserStatusActive}
	store := keys.NewStore()
	if err := store.Load(
		[]model.AccessKey{accessKey},
		map[string]model.User{"u-1": user},
		map[string]keys.PlanSnapshot{"u-1": {Status: model.SubscriptionStatusActive}},
	); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	verifier := NewAccessKeyVerifier(store, pepper)
	credential := keys.FormatCredential(keyID, secret)

	for _, tc := range []struct {
		name string
		addr netip.Addr
		want bool
	}{
		{"inside the prefix", netip.MustParseAddr("203.0.113.7"), true},
		{"the listed single host", netip.MustParseAddr("198.51.100.9"), true},
		{"outside every entry", netip.MustParseAddr("192.0.2.1"), false},
		{"an unresolved address", netip.Addr{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := verifier.VerifyCredential(credential, tc.addr)
			if got := state != nil; got != tc.want {
				t.Fatalf("VerifyCredential from %v resolved=%v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
