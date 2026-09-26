package service

import (
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"prism/internal/intel/store"
	"prism/internal/quality"
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

// TestQualityStatusItemFieldContract pins the per-item JSON keys of
// GET /api/v1/quality/status to the WebUI types
// (internal/api/web/src/features/quality/types.ts: SourceStatus,
// ManualSourceStatus and the inline registry_sources item).
//
// It lives in this package because the projection types are unexported; the
// handler-level body check is TestQualityStatusJSONContract in internal/api.
func TestQualityStatusItemFieldContract(t *testing.T) {
	// The first fourteen keys are the exact SourceStatus contract of
	// internal/api/web/src/features/quality/types.ts. terms, ttl_seconds and
	// supports_ipv6 are additive: the settings page shows the vendor terms and
	// the TTL, and an unknown extra key is ignored by the frontend.
	wantSourceFields := []string{
		"id", "name", "website", "configured", "requires_key", "has_key",
		"daily_limit", "used_today", "queued", "running", "failed", "paused",
		"next_allowed_at", "error_code",
		"terms", "ttl_seconds", "supports_ipv6",
	}
	if got := jsonFieldNames(qualitySourceStatus{}); !slices.Equal(got, wantSourceFields) {
		t.Fatalf("sources[] fields changed:\n got: %v\nwant: %v", got, wantSourceFields)
	}

	wantManualFields := []string{
		"id", "name", "website", "busy", "interval_seconds", "next_allowed_at", "current_ips",
	}
	if got := jsonFieldNames(qualityManualSourceStatus{}); !slices.Equal(got, wantManualFields) {
		t.Fatalf("manual_sources[] fields changed:\n got: %v\nwant: %v", got, wantManualFields)
	}

	wantRegistryFields := []string{"id", "ready", "entries", "updated_at", "error_code"}
	if got := jsonFieldNames(qualityRegistrySourceStatus{}); !slices.Equal(got, wantRegistryFields) {
		t.Fatalf("registry_sources[] fields changed:\n got: %v\nwant: %v", got, wantRegistryFields)
	}

	// The summary item itself is the shared quality.Summary the frontend reads
	// for /quality/ip/{ip} and /quality/assessments.
	wantSummaryFields := []string{"assessment", "ip", "state", "evidence", "task", "sources"}
	if got := jsonFieldNames(quality.Summary{}); !slices.Equal(got, wantSummaryFields) {
		t.Fatalf("quality summary fields changed:\n got: %v\nwant: %v", got, wantSummaryFields)
	}
}

// TestQualitySummaryFromRowsProjectsTheLegacyShape checks the row projection
// that both the per-IP endpoint and the list endpoint use. It is a pure function
// test: no database is involved.
func TestQualitySummaryFromRowsProjectsTheLegacyShape(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-time.Minute)
	validUntil := now.Add(time.Hour).UnixNano()

	rows := []store.Evidence{
		{
			IP: "203.0.113.7", Provider: quality.ProviderID, Profile: quality.ProfileID,
			Status: store.StatusOk, ObservedAtNs: observed.UnixNano(), ValidUntilNs: validUntil,
			NormalizedJSON: `{"ip":"203.0.113.7","provider":"proxycheck","profile":"proxycheck-v3-risk-v1",` +
				`"ip_type":"datacenter","asn":"AS64500","organization":"Example Hosting",` +
				`"grade":"low","signals":{},"observed_at":"2026-05-01T11:59:00Z","valid_until":"2026-05-01T13:00:00Z"}`,
		},
		{
			IP: "203.0.113.7", Provider: "ippure", Profile: "ippure-via-node-v2",
			Status: store.StatusOk, ObservedAtNs: observed.UnixNano(), ValidUntilNs: validUntil,
			NormalizedJSON: `{"ip":"203.0.113.7","provider":"ippure","profile":"ippure-via-node-v2",` +
				`"ip_type":"residential","source_type":"Residential","risk_score":12,` +
				`"grade":"low","signals":{},"observed_at":"2026-05-01T11:59:00Z","valid_until":"2026-05-01T13:00:00Z"}`,
		},
	}
	score := int64(88)
	assessment := &store.Assessment{
		IP: "203.0.113.7", Profile: "prism-purity-v2", State: "valid", Verdict: "favorable",
		PurityScore: sql.NullInt64{Int64: score, Valid: true}, PurityBand: "fair", IPType: "datacenter",
		ReasonsJSON: `["NETWORK_EVIDENCE_INCOMPLETE"]`, ValidUntilNs: validUntil,
	}

	summary := qualitySummaryFromRows("203.0.113.7", rows, assessment, now)

	if summary.IP != "203.0.113.7" {
		t.Fatalf("ip: got %q", summary.IP)
	}
	if summary.State != "valid" {
		t.Fatalf("state: got %q, want valid", summary.State)
	}
	if summary.Evidence == nil || summary.Evidence.Provider != quality.ProviderID {
		t.Fatalf("summary.evidence must be the ProxyCheck row, got %+v", summary.Evidence)
	}
	if len(summary.Sources) != 2 {
		t.Fatalf("sources: got %d, want 2", len(summary.Sources))
	}
	for _, source := range summary.Sources {
		if source.State != "valid" || source.Evidence == nil {
			t.Fatalf("source %q must be valid with evidence, got state=%q evidence=%v",
				source.Provider, source.State, source.Evidence)
		}
	}
	if summary.Assessment == nil {
		t.Fatal("assessment is missing")
	}
	if summary.Assessment.PurityScore == nil || *summary.Assessment.PurityScore != 88 {
		t.Fatalf("purity_score: got %v, want 88", summary.Assessment.PurityScore)
	}
	if summary.Assessment.NetworkSource != quality.ProviderID {
		t.Fatalf("network_source: got %q, want %q", summary.Assessment.NetworkSource, quality.ProviderID)
	}
	if len(summary.Assessment.Reasons) != 1 || summary.Assessment.Reasons[0] != "NETWORK_EVIDENCE_INCOMPLETE" {
		t.Fatalf("reasons: got %v", summary.Assessment.Reasons)
	}
}

// TestQualitySummaryFromRowsWithoutAssessment covers the IP that has evidence
// but no assessment row yet: the summary must still be usable, and the state
// must come from the evidence rather than from a fabricated verdict.
func TestQualitySummaryFromRowsWithoutAssessment(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rows := []store.Evidence{{
		IP: "198.51.100.9", Provider: quality.ProviderID, Profile: quality.ProfileID,
		Status: store.StatusOk, ObservedAtNs: now.UnixNano(),
		ValidUntilNs:   now.Add(-time.Minute).UnixNano(),
		NormalizedJSON: `{"ip":"198.51.100.9","provider":"proxycheck","profile":"proxycheck-v3-risk-v1","signals":{}}`,
	}}

	summary := qualitySummaryFromRows("198.51.100.9", rows, nil, now)

	if summary.Assessment != nil {
		t.Fatal("no assessment row must not invent one")
	}
	if summary.State != "stale" {
		t.Fatalf("state: got %q, want stale", summary.State)
	}
	if len(summary.Sources) != 1 || summary.Sources[0].State != "stale" {
		t.Fatalf("sources: got %+v", summary.Sources)
	}
}

// TestQualitySummaryFromRowsReportsProviderFailure checks that a failed provider
// is surfaced through summary.task.error_code (what the WebUI renders) instead of
// being reported as an unsupported answer.
func TestQualitySummaryFromRowsReportsProviderFailure(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rows := []store.Evidence{{
		IP: "203.0.113.9", Provider: "abuseipdb", Profile: quality.AbuseProfileID,
		Status: store.StatusError, ErrorCode: "PROVIDER_LIMIT", ObservedAtNs: now.UnixNano(),
	}}

	summary := qualitySummaryFromRows("203.0.113.9", rows, nil, now)

	if summary.Task == nil || summary.Task.ErrorCode != "PROVIDER_LIMIT" {
		t.Fatalf("task: got %+v, want error_code PROVIDER_LIMIT", summary.Task)
	}
	if summary.Sources[0].State != "unobserved" {
		t.Fatalf("a failed provider must stay unobserved, got %q", summary.Sources[0].State)
	}
}

// TestQualityStateFromRowsPrefersConflict checks the state precedence: an
// assessment that found a conflict is reported as conflicting even though its
// own state is valid.
func TestQualityStateFromRowsPrefersConflict(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	assessment := &store.Assessment{
		State: "valid", Verdict: "conflicting", IPType: "conflicting",
		ValidUntilNs: now.Add(time.Hour).UnixNano(),
	}
	if got := qualityStateFromRows(nil, assessment, now); got != "conflicting" {
		t.Fatalf("state: got %q, want conflicting", got)
	}
}

// TestQualityTimestampIsEmptyForZero pins the frontend contract: an absent gate
// must serialize as "" and never as the Unix epoch, because the WebUI feeds
// these values straight into Date.parse.
func TestQualityTimestampIsEmptyForZero(t *testing.T) {
	if got := qualityTimestamp(0); got != "" {
		t.Fatalf("qualityTimestamp(0) = %q, want empty", got)
	}
	if got := qualityTimestamp(-1); got != "" {
		t.Fatalf("qualityTimestamp(-1) = %q, want empty", got)
	}
	const ns = int64(1_777_000_000_000_000_000)
	if got := qualityTimestamp(ns); got != "2026-04-24T03:06:40Z" {
		t.Fatalf("qualityTimestamp(%d) = %q", ns, got)
	}
}

// TestQualitySecondsParsesProviderTTL checks the duration wire form the provider
// status exposes.
func TestQualitySecondsParsesProviderTTL(t *testing.T) {
	cases := map[string]int64{
		"24h0m0s":  86400,
		"1m0s":     60,
		"":         0,
		"nonsense": 0,
		"-5m":      0,
	}
	for input, want := range cases {
		if got := qualitySeconds(input); got != want {
			t.Fatalf("qualitySeconds(%q) = %d, want %d", input, got, want)
		}
	}
}
