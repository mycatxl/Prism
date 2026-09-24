package api

import (
	"net/http"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/node"
)

// TestAPIContract_RotateLease covers the WP10 §3 manual rotation endpoint
// POST /api/v1/platforms/{id}/leases/{account}/actions/rotate: it removes the
// lease, records a rotation tombstone for the previous egress IP and returns
// 204 with an empty body.
func TestAPIContract_RotateLease_DeletesLeaseAndRecordsTombstone(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	platformID := mustCreatePlatform(t, srv, "lease-rotate")
	account := "alice"
	hash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"203.0.113.5","port":443}`))
	now := time.Now().UnixNano()
	cp.Router.RestoreLeases([]model.Lease{
		{
			PlatformID:     platformID,
			Account:        account,
			NodeHash:       hash.Hex(),
			EgressIP:       "203.0.113.5",
			ExpiryNs:       now + int64(time.Hour),
			LastAccessedNs: now,
		},
	})

	path := "/api/v1/platforms/" + platformID + "/leases/" + account + "/actions/rotate"
	rec := doJSONRequest(t, srv, http.MethodPost, path, nil, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("rotate lease status: got %d, want %d, body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("rotate lease body: got %q, want empty", rec.Body.String())
	}

	// The lease must be gone.
	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/platforms/"+platformID+"/leases/"+account, nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get rotated lease status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	// The previous egress IP must be recorded as the account's rotation tombstone.
	tombstone, ok := cp.Router.RotationTombstone(platformID, account)
	if !ok {
		t.Fatal("rotate lease did not record a rotation tombstone")
	}
	if tombstone.String() != "203.0.113.5" {
		t.Fatalf("tombstone egress ip = %s, want 203.0.113.5", tombstone)
	}

	// Rotating again has no lease to remove.
	rec = doJSONRequest(t, srv, http.MethodPost, path, nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second rotate status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")
}

func TestAPIContract_RotateLease_ValidationAndAuth(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	platformID := mustCreatePlatform(t, srv, "lease-rotate-validation")

	rec := doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/platforms/not-a-uuid/leases/alice/actions/rotate",
		nil,
		true,
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid platform id status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/platforms/11111111-1111-1111-1111-111111111111/leases/alice/actions/rotate",
		nil,
		true,
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown platform status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/platforms/"+platformID+"/leases/alice/actions/rotate", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status: got %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	assertErrorCode(t, rec, "UNAUTHORIZED")

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/platforms/"+platformID+"/leases/alice/actions/rotate", nil, true)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status: got %d, want %d, body=%s", rec.Code, http.StatusMethodNotAllowed, rec.Body.String())
	}
}
