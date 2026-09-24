package api

import (
	"net/http"
	"testing"

	"prism/internal/service"
	"prism/internal/topology"
)

// parseReportTestContent is a syntactically valid subscription with two
// importable nodes and four refused ones. The refused types are the ones
// docs/ENGINE_DECISIONS.md D-1 rejects, plus one type Prism never knew.
const parseReportTestContent = `{"outbounds":[
	{"type":"shadowsocks","tag":"keep-1","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"synthetic-one"},
	{"type":"shadowsocks","tag":"keep-2","server":"2.2.2.2","server_port":443,"method":"aes-256-gcm","password":"synthetic-two"},
	{"type":"ssr","tag":"drop-ssr-1","server":"3.3.3.3","server_port":443},
	{"type":"ssr","tag":"drop-ssr-2","server":"4.4.4.4","server_port":443},
	{"type":"mieru","tag":"drop-mieru","server":"5.5.5.5","server_port":443},
	{"type":"not-a-protocol","tag":"drop-unknown","server":"6.6.6.6","server_port":443}
]}`

// TestAPIContract_SubscriptionParseReport_ExposesDroppedNodes covers the WP06 §9
// promise that a node which cannot be represented is reported with a reason and
// never dropped silently: the reason counts must match the input, the summary
// must be part of the subscription response and the full report must be
// readable through GET /api/v1/subscriptions/{id}/parse-report.
func TestAPIContract_SubscriptionParseReport_ExposesDroppedNodes(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	// Same wiring as cmd/prism: every parse report is persisted.
	cp.Scheduler = topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: cp.SubMgr,
		Pool:       cp.Pool,
		OnParseReport: func(id string, reportJSON string) {
			if err := cp.Engine.SetSubscriptionParseReport(id, reportJSON); err != nil {
				t.Errorf("SetSubscriptionParseReport(%s): %v", id, err)
			}
		},
	})

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-parse-report",
		"source_type": "local",
		"content":     parseReportTestContent,
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	subID, _ := decodeJSONMap(t, createRec)["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}

	refreshRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions/"+subID+"/actions/refresh", nil, true)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status: got %d, want %d, body=%s", refreshRec.Code, http.StatusOK, refreshRec.Body.String())
	}

	// 6 inputs: 2 imported, 4 refused (3 with a mihomo-only type, 1 unknown).
	getRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get subscription status: got %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	body := decodeJSONMap(t, getRec)
	if got := body["node_count"]; got != float64(2) {
		t.Fatalf("node_count: got %v, want 2 (body=%s)", got, getRec.Body.String())
	}
	summary, ok := body["parse_report"].(map[string]any)
	if !ok {
		t.Fatalf("subscription response has no parse_report: body=%s", getRec.Body.String())
	}
	if got := summary["total"]; got != float64(6) {
		t.Errorf("parse_report.total: got %v, want 6", got)
	}
	if got := summary["imported"]; got != float64(2) {
		t.Errorf("parse_report.imported: got %v, want 2", got)
	}
	if got := summary["skipped"]; got != float64(4) {
		t.Errorf("parse_report.skipped: got %v, want 4", got)
	}

	reasons, ok := summary["reasons"].([]any)
	if !ok || len(reasons) != 2 {
		t.Fatalf("parse_report.reasons: got %v, want 2 buckets", summary["reasons"])
	}
	byReason := map[string]map[string]any{}
	for _, raw := range reasons {
		entry, _ := raw.(map[string]any)
		reason, _ := entry["reason"].(string)
		byReason[reason] = entry
	}
	engineNotBuilt, ok := byReason["ENGINE_NOT_BUILT"]
	if !ok {
		t.Fatalf("parse_report.reasons is missing ENGINE_NOT_BUILT: %v", byReason)
	}
	if got := engineNotBuilt["count"]; got != float64(3) {
		t.Errorf("ENGINE_NOT_BUILT count: got %v, want 3", got)
	}
	unsupported, ok := byReason["UNSUPPORTED_PROTOCOL"]
	if !ok {
		t.Fatalf("parse_report.reasons is missing UNSUPPORTED_PROTOCOL: %v", byReason)
	}
	if got := unsupported["count"]; got != float64(1) {
		t.Errorf("UNSUPPORTED_PROTOCOL count: got %v, want 1", got)
	}
	assertSampleNames(t, engineNotBuilt, []string{"drop-mieru", "drop-ssr-1", "drop-ssr-2"})
	assertSampleNames(t, unsupported, []string{"drop-unknown"})

	// The exact list is available from the dedicated endpoint.
	reportRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID+"/parse-report", nil, true)
	if reportRec.Code != http.StatusOK {
		t.Fatalf("parse-report status: got %d, want %d, body=%s", reportRec.Code, http.StatusOK, reportRec.Body.String())
	}
	report := decodeJSONMap(t, reportRec)
	if report["parsed"] != true {
		t.Errorf("parse-report parsed: got %v, want true", report["parsed"])
	}
	if report["truncated"] != false {
		t.Errorf("parse-report truncated: got %v, want false", report["truncated"])
	}
	stats, ok := report["stats"].(map[string]any)
	if !ok {
		t.Fatalf("parse-report stats: got %T", report["stats"])
	}
	if stats["imported"] != float64(2) || stats["skipped"] != float64(4) || stats["total"] != float64(6) {
		t.Errorf("parse-report stats: got %v, want imported=2 skipped=4 total=6", stats)
	}
	skipped, ok := report["skipped"].([]any)
	if !ok || len(skipped) != 4 {
		t.Fatalf("parse-report skipped: got %v, want 4 records", report["skipped"])
	}
	seen := map[string]string{}
	for _, raw := range skipped {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		reason, _ := entry["reason"].(string)
		seen[name] = reason
	}
	wantReasons := map[string]string{
		"drop-ssr-1":   "ENGINE_NOT_BUILT",
		"drop-ssr-2":   "ENGINE_NOT_BUILT",
		"drop-mieru":   "ENGINE_NOT_BUILT",
		"drop-unknown": "UNSUPPORTED_PROTOCOL",
	}
	for name, want := range wantReasons {
		got, ok := seen[name]
		if !ok {
			t.Errorf("parse-report is missing %q", name)
			continue
		}
		if got != want {
			t.Errorf("parse-report reason for %q: got %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"keep-1", "keep-2"} {
		if _, ok := seen[name]; ok {
			t.Errorf("imported node %q must not appear in the skip report", name)
		}
	}

	// A subscription that has never been parsed answers parsed=false with an
	// empty list, and an unknown id is a 404.
	freshRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-parse-report-fresh",
		"source_type": "local",
		"content":     "127.0.0.1:8082",
	}, true)
	if freshRec.Code != http.StatusCreated {
		t.Fatalf("create fresh subscription status: got %d, want %d, body=%s", freshRec.Code, http.StatusCreated, freshRec.Body.String())
	}
	freshID, _ := decodeJSONMap(t, freshRec)["id"].(string)
	if _, ok := decodeJSONMap(t, freshRec)["parse_report"]; ok {
		t.Error("a subscription that was never parsed must not carry parse_report")
	}
	freshReportRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+freshID+"/parse-report", nil, true)
	if freshReportRec.Code != http.StatusOK {
		t.Fatalf("fresh parse-report status: got %d, want %d, body=%s", freshReportRec.Code, http.StatusOK, freshReportRec.Body.String())
	}
	freshReport := decodeJSONMap(t, freshReportRec)
	if freshReport["parsed"] != false {
		t.Errorf("fresh parse-report parsed: got %v, want false", freshReport["parsed"])
	}
	if skipped, ok := freshReport["skipped"].([]any); !ok || len(skipped) != 0 {
		t.Errorf("fresh parse-report skipped: got %v, want an empty list", freshReport["skipped"])
	}

	missingRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/11111111-2222-3333-4444-555555555555/parse-report", nil, true)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("unknown subscription parse-report status: got %d, want %d, body=%s", missingRec.Code, http.StatusNotFound, missingRec.Body.String())
	}
}

func assertSampleNames(t *testing.T, bucket map[string]any, want []string) {
	t.Helper()
	raw, ok := bucket["sample_names"].([]any)
	if !ok {
		t.Fatalf("sample_names: got %T (%v)", bucket["sample_names"], bucket["sample_names"])
	}
	got := map[string]bool{}
	for _, entry := range raw {
		name, _ := entry.(string)
		got[name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("sample_names %v is missing %q", raw, name)
		}
	}
}

// TestAPIContract_SubscriptionAutoIntel_RoundTripsAndReachesTheStore covers
// WP08 §3.6: auto_intel is settable per subscription through the API and the
// value that the intel pipeline reads is the value that was set. The pipeline
// resolves the flag through prismApp.intelSubscriptionAutoIntel
// (cmd/prism/intel_runtime.go), which reads the auto_intel column through
// Engine.ListSubscriptions — the same store path asserted here.
func TestAPIContract_SubscriptionAutoIntel_RoundTripsAndReachesTheStore(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-auto-intel-off",
		"source_type": "local",
		"content":     "127.0.0.1:8080",
		"auto_intel":  false,
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	createBody := decodeJSONMap(t, createRec)
	if got := createBody["auto_intel"]; got != false {
		t.Errorf("create response auto_intel: got %v, want false", got)
	}
	subID, _ := createBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}
	if got := storedAutoIntel(t, cp, subID); got != false {
		t.Errorf("stored auto_intel after create: got %v, want false", got)
	}

	patchRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"auto_intel": true,
	}, true)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch subscription status: got %d, want %d, body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if got := decodeJSONMap(t, patchRec)["auto_intel"]; got != true {
		t.Errorf("patch response auto_intel: got %v, want true", got)
	}
	if got := storedAutoIntel(t, cp, subID); got != true {
		t.Errorf("stored auto_intel after patch: got %v, want true", got)
	}

	// A subscription created without the field keeps the documented default.
	defaultRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-auto-intel-default",
		"source_type": "local",
		"content":     "127.0.0.1:8081",
	}, true)
	if defaultRec.Code != http.StatusCreated {
		t.Fatalf("create default subscription status: got %d, want %d, body=%s", defaultRec.Code, http.StatusCreated, defaultRec.Body.String())
	}
	defaultBody := decodeJSONMap(t, defaultRec)
	if got := defaultBody["auto_intel"]; got != true {
		t.Errorf("default auto_intel: got %v, want true", got)
	}
	defaultID, _ := defaultBody["id"].(string)
	if got := storedAutoIntel(t, cp, defaultID); got != true {
		t.Errorf("stored default auto_intel: got %v, want true", got)
	}

	// The field is still validated: a typo must not be silently accepted.
	badRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"auto_intel_enabled": true,
	}, true)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("patch with unknown field: got %d, want %d, body=%s", badRec.Code, http.StatusBadRequest, badRec.Body.String())
	}
}

// storedAutoIntel reads the flag the way the intel pipeline does: through the
// persisted subscription rows (Engine.ListSubscriptions), not through the API
// response.
func storedAutoIntel(t *testing.T, cp *service.ControlPlaneService, id string) bool {
	t.Helper()
	rows, err := cp.Engine.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row.AutoIntel
		}
	}
	t.Fatalf("subscription %s is missing from the persisted rows", id)
	return false
}
