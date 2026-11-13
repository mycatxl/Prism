package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/service"
)

func doTokenJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	var err error
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func newStrictTokenActionHandler(t *testing.T, cp *service.ControlPlaneService) (http.Handler, string) {
	t.Helper()
	verifier, credential := tokenActionKeyFixture(t, "token-action-test-user", nil)
	return NewTokenActionHandler("legacy-token-ignored", verifier, cp, 1<<20, nil), credential
}

func TestTokenActionInheritLease_Success(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-target"
	platformID := mustCreatePlatform(t, srv, platformName)

	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		UserID:         "token-action-test-user",
		Account:        "parent-account",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    nowNs - int64(10*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	handler, credential := newStrictTokenActionHandler(t, cp)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent-account",
			"new_account":    "new-account",
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	if body["status"] != "ok" {
		t.Fatalf("status field: got %v, want %q", body["status"], "ok")
	}

	child := cp.Router.ReadLease(model.LeaseKey{
		PlatformID: platformID,
		UserID:     "token-action-test-user",
		Account:    "new-account",
	})
	if child == nil {
		t.Fatal("expected new-account lease to be created")
	}
	if child.NodeHash != parent.NodeHash {
		t.Fatalf("child node_hash: got %q, want %q", child.NodeHash, parent.NodeHash)
	}
	if child.EgressIP != parent.EgressIP {
		t.Fatalf("child egress_ip: got %q, want %q", child.EgressIP, parent.EgressIP)
	}
	if child.ExpiryNs != parent.ExpiryNs {
		t.Fatalf("child expiry_ns: got %d, want %d", child.ExpiryNs, parent.ExpiryNs)
	}
}

func TestTokenActionInheritLease_RejectsUnknownFields(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-unknown-field"
	_ = mustCreatePlatform(t, srv, platformName)

	handler, credential := newStrictTokenActionHandler(t, cp)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent",
			"new_account":    "child",
			"extra":          "unexpected",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestTokenActionInheritLease_ParentMissingOrExpiredReturnsNotFound(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-parent-notfound"
	platformID := mustCreatePlatform(t, srv, platformName)
	handler, credential := newStrictTokenActionHandler(t, cp)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "missing-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	nowNs := time.Now().UnixNano()
	expired := model.Lease{
		PlatformID:     platformID,
		UserID:         "token-action-test-user",
		Account:        "expired-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"expired-token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.22",
		CreatedAtNs:    nowNs - int64(2*time.Hour),
		ExpiryNs:       nowNs - int64(time.Second),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(expired); err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}

	rec = doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "expired-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expired parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")
}

func TestTokenActionInheritLease_InvalidArguments(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-invalid-args"
	_ = mustCreatePlatform(t, srv, platformName)
	handler, credential := newStrictTokenActionHandler(t, cp)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/"+credential+"/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "same-account",
			"new_account":    "same-account",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same account status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

// TestTokenActionHandler_RejectsLegacyTokens verifies that neither a configured
// global token nor an empty token configuration can reach the action route.
func TestTokenActionHandler_RejectsLegacyTokens(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-compare-target"
	_ = mustCreatePlatform(t, srv, platformName)
	verifier, _ := tokenActionKeyFixture(t, "token-action-test-user", nil)
	handler := NewTokenActionHandler("legacy-token", verifier, cp, 1<<20, nil)
	actionPath := "/api/v1/" + platformName + "/actions/inherit-lease"
	body := map[string]any{"parent_account": "parent", "new_account": "child"}

	for _, token := range []string{"legacy-token", "wrong-token", ""} {
		path := "/" + token + actionPath
		if token == "" {
			path = actionPath
		}
		rec := doTokenJSONRequest(t, handler, http.MethodPost, path, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("token %q: got %d, want 404", token, rec.Code)
		}
	}
}

// TestTokenActionInheritLease_IsAudited covers the AccessKey token-path
// mutation and ensures the audit row names a non-reversible key actor.
func TestTokenActionInheritLease_IsAudited(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-audit"
	platformID := mustCreatePlatform(t, srv, platformName)
	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		UserID:         "token-action-audit-user",
		Account:        "audit-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"audit-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.44",
		CreatedAtNs:    nowNs - int64(5*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	verifier, credential := tokenActionKeyFixture(t, "token-action-audit-user", nil)
	handler := NewTokenActionHandler("legacy-token", verifier, cp, 1<<20, nil)
	path := "/" + credential + "/api/v1/" + platformName + "/actions/inherit-lease"

	before, err := cp.Engine.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	rec := doTokenJSONRequest(t, handler, http.MethodPost, path,
		map[string]any{"parent_account": "audit-parent", "new_account": "audit-child"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	entries, err := cp.Engine.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != len(before)+1 {
		t.Fatalf("audit entries after the mutation: got %d, want %d", len(entries), len(before)+1)
	}
	entry := entries[0]
	if want := CredentialActor(credential); entry.Actor != want {
		t.Fatalf("audit actor: got %q, want %q", entry.Actor, want)
	}
	if entry.Action != "POST /{token}/api/v1/{platform}/actions/inherit-lease" {
		t.Fatalf("audit action: got %q", entry.Action)
	}
	if entry.Target != "platform="+platformName {
		t.Fatalf("audit target: got %q, want %q (the credential path parameter must not be recorded)",
			entry.Target, "platform="+platformName)
	}
	recorded := entry.Actor + " " + entry.Action + " " + entry.Target + " " + entry.Detail + " " + entry.RemoteAddr
	if strings.Contains(recorded, credential) {
		t.Fatal("audit record leaks the AccessKey credential")
	}

	rec = doTokenJSONRequest(t, handler, http.MethodPost, path,
		map[string]any{"parent_account": "missing-parent", "new_account": "audit-child-2"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("failed mutation status: got %d, want %d", rec.Code, http.StatusNotFound)
	}
	entries, err = cp.Engine.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != len(before)+1 {
		t.Fatalf("failed mutation was audited: got %d entries, want %d", len(entries), len(before)+1)
	}
}
