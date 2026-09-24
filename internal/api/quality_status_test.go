package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"prism/internal/inspection"
	"prism/internal/service"
)

// jsonFieldNames returns the JSON object key names of a struct, in declaration
// order. Non-struct values or fields without a JSON tag return an empty list.
func jsonFieldNames(value any) []string {
	typ := reflect.TypeOf(value)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// TestQualityStatusJSONContract is the WP04 §4.5 snapshot test: it pins
// /api/v1/quality/status to the WebUI QualityStatus type
// (internal/api/web/src/features/quality/types.ts) so fields cannot silently
// disappear again, and it checks that the disabled shape uses empty arrays and
// a string storage_error instead of null.
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

	// Item shape: every field the frontend SourceStatus type reads must exist.
	wantSourceFields := []string{
		"id", "name", "website", "configured", "requires_key", "has_key",
		"daily_limit", "used_today", "queued", "running", "failed", "paused",
		"next_allowed_at", "error_code",
	}
	if got := jsonFieldNames(inspection.SourceStatus{}); !slices.Equal(got, wantSourceFields) {
		t.Fatalf("sources[] fields changed:\n got: %v\nwant: %v", got, wantSourceFields)
	}

	wantManualFields := []string{
		"id", "name", "website", "busy", "interval_seconds", "next_allowed_at", "current_ips",
	}
	if got := jsonFieldNames(inspection.ManualSourceStatus{}); !slices.Equal(got, wantManualFields) {
		t.Fatalf("manual_sources[] fields changed:\n got: %v\nwant: %v", got, wantManualFields)
	}

	wantRegistryFields := []string{"id", "ready", "entries", "updated_at", "error_code"}
	if got := jsonFieldNames(inspection.RegistryStatus{}); !slices.Equal(got, wantRegistryFields) {
		t.Fatalf("registry_sources[] fields changed:\n got: %v\nwant: %v", got, wantRegistryFields)
	}
}
