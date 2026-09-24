package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/node"
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

func TestTokenActionInheritLease_Success(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-target"
	platformID := mustCreatePlatform(t, srv, platformName)

	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
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

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
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

	child := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "new-account"})
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

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
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
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
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
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
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
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
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

// TestTokenActionHandler_TokenComparison covers G-07: the constant-time compare
// accepts the configured token, rejects a wrong one, rejects a request without a
// token segment, and keeps the Resin "empty token disables the compare"
// behaviour. No response may echo the token.
func TestTokenActionHandler_TokenComparison(t *testing.T) {
	const proxyToken = "proxy-token-under-test"

	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-compare-target"
	platformID := mustCreatePlatform(t, srv, platformName)
	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		Account:        "token-compare-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"token-compare-node"}`)).Hex(),
		EgressIP:       "203.0.113.31",
		CreatedAtNs:    nowNs - int64(5*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	handler := NewTokenActionHandler(proxyToken, cp, 1<<20)
	actionPath := "/api/v1/" + platformName + "/actions/inherit-lease"

	// 1. The configured token is accepted and a lease is created.
	rec := doTokenJSONRequest(t, handler, http.MethodPost, "/"+proxyToken+actionPath,
		map[string]any{"parent_account": "token-compare-parent", "new_account": "token-compare-child"})
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), proxyToken) {
		t.Fatal("response body echoes the proxy token")
	}
	if lease := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "token-compare-child"}); lease == nil {
		t.Fatal("correct token must inherit the lease")
	}

	// 2. A wrong token answers 404 and must not touch lease state.
	rec = doTokenJSONRequest(t, handler, http.MethodPost, "/wrong-token"+actionPath,
		map[string]any{"parent_account": "token-compare-parent", "new_account": "wrong-token-child"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), proxyToken) {
		t.Fatal("wrong-token response echoes the configured proxy token")
	}
	if lease := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "wrong-token-child"}); lease != nil {
		t.Fatal("wrong token must not change lease state")
	}

	// 3. A request without the token segment does not match the route at all.
	rec = doTokenJSONRequest(t, handler, http.MethodPost, actionPath,
		map[string]any{"parent_account": "token-compare-parent", "new_account": "missing-token-child"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing token: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if lease := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "missing-token-child"}); lease != nil {
		t.Fatal("a request without a token must not change lease state")
	}

	// 4. PRISM_PROXY_TOKEN="" keeps the token compare disabled.
	openHandler := NewTokenActionHandler("", cp, 1<<20)
	rec = doTokenJSONRequest(t, openHandler, http.MethodPost, "/anything"+actionPath,
		map[string]any{"parent_account": "token-compare-parent", "new_account": "open-token-child"})
	if rec.Code != http.StatusOK {
		t.Fatalf("empty configured token: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestTokenActionInheritLease_IsAudited covers G-07: the token-path mutation is
// recorded in the audit trail, its actor is the proxy-token digest (never the
// token), the token path parameter is not stored, and a failed mutation is not
// audited at all.
func TestTokenActionInheritLease_IsAudited(t *testing.T) {
	const proxyToken = "token-action-audit-token"

	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-audit"
	platformID := mustCreatePlatform(t, srv, platformName)
	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
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

	handler := NewTokenActionHandler(proxyToken, cp, 1<<20)
	path := "/" + proxyToken + "/api/v1/" + platformName + "/actions/inherit-lease"

	// mustCreatePlatform goes through the audited management API, so record the
	// audit size before the token-path mutation.
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
	sum := sha256.Sum256([]byte(proxyToken))
	if want := hex.EncodeToString(sum[:4]); entry.Actor != want {
		t.Fatalf("audit actor: got %q, want the proxy-token digest %q", entry.Actor, want)
	}
	if entry.Action != "POST /{token}/api/v1/{platform}/actions/inherit-lease" {
		t.Fatalf("audit action: got %q", entry.Action)
	}
	if entry.Target != "platform="+platformName {
		t.Fatalf("audit target: got %q, want %q (the credential path parameter must not be recorded)",
			entry.Target, "platform="+platformName)
	}
	recorded := entry.Actor + " " + entry.Action + " " + entry.Target + " " + entry.Detail + " " + entry.RemoteAddr
	if strings.Contains(recorded, proxyToken) {
		t.Fatal("audit record leaks the proxy token")
	}

	// A failed mutation is not audited: nothing changed.
	rec = doTokenJSONRequest(t, handler, http.MethodPost, path,
		map[string]any{"parent_account": "missing-parent", "new_account": "audit-child-2"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("failed mutation status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	entries, err = cp.Engine.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != len(before)+1 {
		t.Fatalf("failed mutation was audited: got %d entries, want %d", len(entries), len(before)+1)
	}
}
