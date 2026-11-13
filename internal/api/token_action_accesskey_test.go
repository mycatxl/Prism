package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/proxy"
	"prism/internal/service"
)

// tokenActionKeyFixture builds a verifier holding one usable proxy key and
// returns the credential that unlocks it.
func tokenActionKeyFixture(t *testing.T, userID string, platforms []string) (proxy.AccessKeyVerifier, string) {
	t.Helper()

	pepper, err := auth.NewPepper([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("auth.NewPepper: %v", err)
	}
	keyID, secret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	store := keys.NewStore()
	if err := store.Load(
		[]model.AccessKey{{
			ID:         keyID,
			UserID:     userID,
			SecretHash: pepper.HashSecret(secret),
			Scope:      model.KeyScopeProxy,
			Status:     model.KeyStatusActive,
			Platforms:  platforms,
		}},
		map[string]model.User{userID: {ID: userID, Role: model.UserRoleUser, Status: model.UserStatusActive}},
		nil,
	); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	return proxy.NewAccessKeyVerifier(store, pepper), keys.FormatCredential(keyID, secret)
}

// seedTokenActionParentLease writes the lease the inherit action reads.
func seedTokenActionParentLease(t *testing.T, cp *service.ControlPlaneService, platformID string) {
	t.Helper()
	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		UserID:         "user-1",
		Account:        "parent-account",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"token-key-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    nowNs - int64(10*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}
}

// The token-path route accepts only a verified proxy AccessKey.
func TestTokenAction_AcceptsAnAccessKey(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-key-target"
	platformID := mustCreatePlatform(t, srv, platformName)
	seedTokenActionParentLease(t, cp, platformID)

	verifier, credential := tokenActionKeyFixture(t, "user-1", []string{platformID})
	handler := NewTokenActionHandler("tok", verifier, cp, 1<<20, nil)

	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenAction_CannotUseAnotherUsersLease(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-key-owner-bound"
	platformID := mustCreatePlatform(t, srv, platformName)
	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		UserID:         "different-user",
		Account:        "parent-account",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"different-user-parent"}`)).Hex(),
		EgressIP:       "203.0.113.11",
		CreatedAtNs:    nowNs - int64(10*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	verifier, credential := tokenActionKeyFixture(t, "user-1", []string{platformID})
	handler := NewTokenActionHandler("tok", verifier, cp, 1<<20, nil)
	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
	if child := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, UserID: "user-1", Account: "new-account"}); child != nil {
		t.Fatal("token action created a lease under the wrong owner")
	}
}

func TestTokenAction_RejectsTheGlobalToken(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-plain-target"
	platformID := mustCreatePlatform(t, srv, platformName)
	seedTokenActionParentLease(t, cp, platformID)

	verifier, _ := tokenActionKeyFixture(t, "user-1", nil)
	handler := NewTokenActionHandler("tok", verifier, cp, 1<<20, nil)

	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

// A revoked key must not act by being presented where a token is expected, even
// when the configured token happens to be the revoked credential itself.
func TestTokenAction_RejectsARevokedKeyRatherThanFallingBack(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-revoked-target"
	platformID := mustCreatePlatform(t, srv, platformName)
	seedTokenActionParentLease(t, cp, platformID)

	verifier, _ := tokenActionKeyFixture(t, "user-1", nil)
	revokedID, revokedSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	revoked := keys.FormatCredential(revokedID, revokedSecret)

	handler := NewTokenActionHandler(revoked, verifier, cp, 1<<20, nil)
	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/"+revoked+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenAction_RecordsOnlyKeyShapedVerificationFailures(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-auth-failure-target"
	_ = mustCreatePlatform(t, srv, platformName)
	verifier, validCredential := tokenActionKeyFixture(t, "user-1", nil)

	failures := 0
	handler := NewTokenActionHandlerWithAuthFailureRecorder(
		"legacy-token",
		verifier,
		cp,
		1<<20,
		nil,
		func(*http.Request) { failures++ },
	)
	body := map[string]any{"parent_account": "parent", "new_account": "child"}
	actionPath := "/api/v1/" + platformName + "/actions/inherit-lease"

	unknownID, unknownSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	unknownCredential := keys.FormatCredential(unknownID, unknownSecret)
	if rec := doTokenJSONRequest(t, handler, http.MethodPost, "/"+unknownCredential+actionPath, body); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown key status = %d, want 404", rec.Code)
	}
	if failures != 1 {
		t.Fatalf("recorded failures = %d, want 1", failures)
	}

	if rec := doTokenJSONRequest(t, handler, http.MethodPost, "/legacy-token"+actionPath, body); rec.Code != http.StatusNotFound {
		t.Fatalf("legacy token status = %d, want 404", rec.Code)
	}
	if failures != 1 {
		t.Fatalf("non-key token changed recorded failures to %d", failures)
	}

	_ = doTokenJSONRequest(t, handler, http.MethodPost, "/"+validCredential+actionPath, body)
	if failures != 1 {
		t.Fatalf("valid credential changed recorded failures to %d", failures)
	}
}

// A key restricted to one platform must not rewrite another platform's leases:
// this route changes routing state, so the data-plane restriction applies here
// exactly as it does on the proxy paths.
func TestTokenAction_KeyCannotReachAnotherPlatform(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	allowedName := "token-key-allowed"
	allowedID := mustCreatePlatform(t, srv, allowedName)
	otherName := "token-key-other"
	otherID := mustCreatePlatform(t, srv, otherName)
	seedTokenActionParentLease(t, cp, otherID)

	verifier, credential := tokenActionKeyFixture(t, "user-1", []string{allowedID})
	handler := NewTokenActionHandler("tok", verifier, cp, 1<<20, nil)

	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/"+credential+"/api/v1/"+otherName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// The audit row must name the credential that acted, not the admin token: the
// trail is read back through the console, so an action taken with a key has to
// be attributable to that key's owner.
func TestTokenAction_KeyActionIsAuditedUnderTheKey(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-audit-target"
	platformID := mustCreatePlatform(t, srv, platformName)
	seedTokenActionParentLease(t, cp, platformID)

	verifier, credential := tokenActionKeyFixture(t, "user-1", nil)
	handler := NewTokenActionHandler("tok", verifier, cp, 1<<20, nil)

	rec := doTokenJSONRequest(t, handler, http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{"parent_account": "parent-account", "new_account": "new-account"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	entries, err := cp.Engine.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the action was not audited")
	}
	entry := entries[0]

	if entry.Actor == "" {
		t.Fatal("the audit entry has no actor")
	}
	if entry.Actor == credential {
		t.Fatal("the audit entry stored the credential itself")
	}
	if entry.UserID != "user-1" {
		t.Fatalf("audit user_id = %q, want user-1", entry.UserID)
	}
	// The credential must not appear anywhere in the row, including the target
	// built from the path parameters.
	if strings.Contains(entry.Target, credential) {
		t.Fatalf("audit target leaked the credential: %q", entry.Target)
	}
}
