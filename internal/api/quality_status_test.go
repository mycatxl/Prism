package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/service"
)

// TestQualityStatusJSONContract is the WP04 §4.5 snapshot test: it pins
// /api/v1/quality/status to the WebUI QualityStatus type
// (internal/api/web/src/features/quality/types.ts) so fields cannot silently
// disappear again, and it checks that the disabled shape uses empty arrays and
// a string storage_error instead of null.
//
// WP08 §8 replaced the in-memory inspection manager with intel.db, so the
// expected body is the shape the intel-backed projection produces when no
// subsystem is wired: enabled=false and every list empty. The field names are
// still exactly the WebUI contract; the per-item field list is pinned by
// TestQualityStatusItemFieldContract in internal/service, which can reflect on
// the unexported projection types.
func TestQualityStatusJSONContract(t *testing.T) {
	cp := &service.ControlPlaneService{}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/quality/status", HandleQualityStatus(cp))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/quality/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	const want = `{"enabled":false,"known_ips":0,"checked_ips":0,` +
		`"low_risk_ips":0,"high_risk_ips":0,"stale_ips":0,` +
		`"queue_capacity":0,"dropped_observations":0,"storage_error":"",` +
		`"sources":[],"manual_sources":[],"registry_sources":[]}`
	got := strings.TrimSpace(rec.Body.String())
	if got != want {
		t.Fatalf("quality status JSON changed:\n got: %s\nwant: %s", got, want)
	}
}
