package state

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"prism/internal/model"
)

// --- intel provider settings ---

func TestStateRepo_PrismIntelProviderSettingRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	const apiKey = "sk-prism-intel-0123456789abcdef"
	setting := model.IntelProviderSetting{
		ProviderID:  "ipinfo",
		Enabled:     true,
		APIKey:      apiKey,
		DailyLimit:  5000,
		QPS:         2.5,
		TTLNs:       int64(6 * time.Hour),
		ConfigJSON:  `{"endpoint":"https://api.example.com/v1"}`,
		UpdatedAtNs: now,
	}
	if err := repo.UpsertIntelProviderSetting(setting); err != nil {
		t.Fatalf("UpsertIntelProviderSetting: %v", err)
	}

	list, err := repo.ListIntelProviderSettings()
	if err != nil {
		t.Fatalf("ListIntelProviderSettings: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 provider setting, got %d", len(list))
	}
	got := list[0]
	if got.ProviderID != setting.ProviderID {
		t.Errorf("provider_id: got %q, want %q", got.ProviderID, setting.ProviderID)
	}
	if !got.Enabled {
		t.Error("enabled did not round-trip true")
	}
	if got.APIKey != apiKey {
		t.Errorf("api_key did not round-trip: got %q, want %q", got.APIKey, apiKey)
	}
	if got.DailyLimit != setting.DailyLimit {
		t.Errorf("daily_limit: got %d, want %d", got.DailyLimit, setting.DailyLimit)
	}
	if got.QPS != setting.QPS {
		t.Errorf("qps: got %v, want %v", got.QPS, setting.QPS)
	}
	if got.TTLNs != setting.TTLNs {
		t.Errorf("ttl_ns: got %d, want %d", got.TTLNs, setting.TTLNs)
	}
	if got.ConfigJSON != setting.ConfigJSON {
		t.Errorf("config_json: got %q, want %q", got.ConfigJSON, setting.ConfigJSON)
	}
	if got.UpdatedAtNs != now {
		t.Errorf("updated_at_ns: got %d, want %d", got.UpdatedAtNs, now)
	}

	// The raw column holds the secret so the provider client can use it.
	var storedKey string
	if err := repo.db.QueryRow(
		`SELECT api_key FROM intel_provider_settings WHERE provider_id = ?`, setting.ProviderID,
	).Scan(&storedKey); err != nil {
		t.Fatalf("read persisted api_key: %v", err)
	}
	if storedKey != apiKey {
		t.Fatalf("persisted api_key: got %q, want %q", storedKey, apiKey)
	}

	// Upsert on the same provider_id must update in place, including the key.
	const rotatedKey = "sk-prism-intel-rotated-9876543210"
	setting.APIKey = rotatedKey
	setting.Enabled = false
	if err := repo.UpsertIntelProviderSetting(setting); err != nil {
		t.Fatalf("re-upsert intel provider setting: %v", err)
	}
	list, err = repo.ListIntelProviderSettings()
	if err != nil {
		t.Fatalf("ListIntelProviderSettings after update: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the upsert to update in place, got %d rows", len(list))
	}
	if list[0].APIKey != rotatedKey {
		t.Errorf("rotated api_key: got %q, want %q", list[0].APIKey, rotatedKey)
	}
	if list[0].Enabled {
		t.Error("enabled did not round-trip false")
	}

	// The API key is never serialized.
	raw, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("marshal IntelProviderSetting: %v", err)
	}
	encoded := string(raw)
	for _, secret := range []string{apiKey, rotatedKey} {
		if strings.Contains(encoded, secret) {
			t.Errorf("marshalled IntelProviderSetting leaks the API key %q: %s", secret, encoded)
		}
	}
	if strings.Contains(encoded, "api_key") {
		t.Errorf("marshalled IntelProviderSetting exposes an api_key field: %s", encoded)
	}
	if !strings.Contains(encoded, `"provider_id":"ipinfo"`) {
		t.Errorf("marshalled IntelProviderSetting lost its public fields: %s", encoded)
	}
}

// --- export profiles ---

func TestStateRepo_PrismExportProfileLifecycle(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	profile := model.ExportProfile{
		ID:             "exp-clients",
		Name:           "Clients",
		Format:         "base64",
		TokenSHA256:    strings.Repeat("a1", 32),
		PlatformID:     "plat-1",
		FilterJSON:     `{"alive_only":true}`,
		NameTemplate:   "{{.Name}}-{{.Index}}",
		Enabled:        true,
		LastAccessAtNs: 0,
		AccessCount:    0,
		CreatedAtNs:    now,
		UpdatedAtNs:    now,
	}
	if err := repo.UpsertExportProfile(profile); err != nil {
		t.Fatalf("UpsertExportProfile: %v", err)
	}

	got, err := repo.GetExportProfile(profile.ID)
	if err != nil {
		t.Fatalf("GetExportProfile: %v", err)
	}
	if !reflect.DeepEqual(*got, profile) {
		t.Fatalf("export profile round-trip mismatch:\n got %+v\nwant %+v", *got, profile)
	}

	byToken, err := repo.GetExportProfileByTokenSHA256(profile.TokenSHA256)
	if err != nil {
		t.Fatalf("GetExportProfileByTokenSHA256: %v", err)
	}
	if byToken.ID != profile.ID {
		t.Fatalf("token lookup returned %q, want %q", byToken.ID, profile.ID)
	}

	// A second profile, ordered by name, must not disturb the first one.
	second := model.ExportProfile{
		ID:          "exp-alpha",
		Name:        "Alpha",
		Format:      "links",
		TokenSHA256: strings.Repeat("b2", 32),
		FilterJSON:  `{}`,
		Enabled:     true,
		CreatedAtNs: now,
		UpdatedAtNs: now,
	}
	if err := repo.UpsertExportProfile(second); err != nil {
		t.Fatalf("UpsertExportProfile (second): %v", err)
	}
	list, err := repo.ListExportProfiles()
	if err != nil {
		t.Fatalf("ListExportProfiles: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 export profiles, got %d", len(list))
	}
	if list[0].ID != "exp-alpha" || list[1].ID != "exp-clients" {
		t.Fatalf("ListExportProfiles is not ordered by name: got %q, %q", list[0].ID, list[1].ID)
	}

	// Duplicate name with a different ID is a conflict.
	dupName := second
	dupName.ID = "exp-alpha-copy"
	dupName.TokenSHA256 = strings.Repeat("c3", 32)
	if err := repo.UpsertExportProfile(dupName); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate export profile name: got %v, want ErrConflict", err)
	}

	// Duplicate token with a different ID is a conflict too.
	dupToken := second
	dupToken.ID = "exp-alpha-token-copy"
	dupToken.Name = "Alpha-Token-Copy"
	if err := repo.UpsertExportProfile(dupToken); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate export profile token: got %v, want ErrConflict", err)
	}

	// The rejected rows must not have been written.
	list, err = repo.ListExportProfiles()
	if err != nil {
		t.Fatalf("ListExportProfiles after conflicts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected exactly the 2 valid profiles, got %d: %+v", len(list), list)
	}

	// Touching a profile records the fetch.
	accessAt := now + int64(time.Minute)
	if err := repo.TouchExportProfileAccess(profile.ID, accessAt); err != nil {
		t.Fatalf("TouchExportProfileAccess: %v", err)
	}
	got, err = repo.GetExportProfile(profile.ID)
	if err != nil {
		t.Fatalf("GetExportProfile after touch: %v", err)
	}
	if got.AccessCount != 1 {
		t.Errorf("access_count after one touch: got %d, want 1", got.AccessCount)
	}
	if got.LastAccessAtNs != accessAt {
		t.Errorf("last_access_at_ns: got %d, want %d", got.LastAccessAtNs, accessAt)
	}
	if err := repo.TouchExportProfileAccess(profile.ID, accessAt+1); err != nil {
		t.Fatalf("second TouchExportProfileAccess: %v", err)
	}
	got, err = repo.GetExportProfile(profile.ID)
	if err != nil {
		t.Fatalf("GetExportProfile after second touch: %v", err)
	}
	if got.AccessCount != 2 {
		t.Errorf("access_count after two touches: got %d, want 2", got.AccessCount)
	}
	if got.LastAccessAtNs != accessAt+1 {
		t.Errorf("last_access_at_ns after second touch: got %d, want %d", got.LastAccessAtNs, accessAt+1)
	}

	// Missing rows report ErrNotFound.
	if _, err := repo.GetExportProfile("exp-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetExportProfile(missing): got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetExportProfileByTokenSHA256(strings.Repeat("ff", 32)); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetExportProfileByTokenSHA256(missing): got %v, want ErrNotFound", err)
	}
	if err := repo.TouchExportProfileAccess("exp-missing", accessAt); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchExportProfileAccess(missing): got %v, want ErrNotFound", err)
	}
	if err := repo.DeleteExportProfile("exp-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteExportProfile(missing): got %v, want ErrNotFound", err)
	}

	// Deleting a real profile removes it.
	if err := repo.DeleteExportProfile(profile.ID); err != nil {
		t.Fatalf("DeleteExportProfile: %v", err)
	}
	if _, err := repo.GetExportProfile(profile.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetExportProfile after delete: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetExportProfileByTokenSHA256(profile.TokenSHA256); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetExportProfileByTokenSHA256 after delete: got %v, want ErrNotFound", err)
	}
}

// --- audit log ---

func TestStateRepo_PrismAuditAppendListAndPrune(t *testing.T) {
	repo := newTestStateRepo(t)
	const base = int64(1_700_000_000_000_000_000)
	const step = int64(time.Minute)

	for i := 0; i < 5; i++ {
		entry := model.AuditEntry{
			AtNs:       base + int64(i)*step,
			Actor:      "admin",
			RemoteAddr: "10.0.0.7:54321",
			Action:     "platform.update",
			Target:     "plat-" + itoa(i),
			Detail:     `{"field":"name"}`,
		}
		if err := repo.AppendAudit(entry); err != nil {
			t.Fatalf("AppendAudit(%d): %v", i, err)
		}
	}

	// Newest first.
	entries, err := repo.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 audit entries, got %d", len(entries))
	}
	for i, want := range []string{"plat-4", "plat-3", "plat-2", "plat-1", "plat-0"} {
		if entries[i].Target != want {
			t.Fatalf("ListAudit[%d].Target: got %q, want %q (full order: %+v)", i, entries[i].Target, want, entries)
		}
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ID >= entries[i-1].ID {
			t.Fatalf("ListAudit is not ordered by descending id: %d then %d", entries[i-1].ID, entries[i].ID)
		}
	}
	if entries[0].Actor != "admin" || entries[0].Action != "platform.update" ||
		entries[0].RemoteAddr != "10.0.0.7:54321" || entries[0].Detail != `{"field":"name"}` {
		t.Fatalf("audit entry fields did not round-trip: %+v", entries[0])
	}
	if entries[0].AtNs != base+4*step {
		t.Fatalf("audit at_ns did not round-trip: got %d, want %d", entries[0].AtNs, base+4*step)
	}

	// Cursor paging: the second page starts strictly below the last ID seen.
	page1, err := repo.ListAudit(0, 2)
	if err != nil {
		t.Fatalf("ListAudit page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Target != "plat-4" || page1[1].Target != "plat-3" {
		t.Fatalf("unexpected page 1: %+v", page1)
	}
	page2, err := repo.ListAudit(page1[1].ID, 2)
	if err != nil {
		t.Fatalf("ListAudit page 2: %v", err)
	}
	if len(page2) != 2 || page2[0].Target != "plat-2" || page2[1].Target != "plat-1" {
		t.Fatalf("unexpected page 2: %+v", page2)
	}
	page3, err := repo.ListAudit(page2[1].ID, 2)
	if err != nil {
		t.Fatalf("ListAudit page 3: %v", err)
	}
	if len(page3) != 1 || page3[0].Target != "plat-0" {
		t.Fatalf("unexpected page 3: %+v", page3)
	}
	page4, err := repo.ListAudit(page3[0].ID, 2)
	if err != nil {
		t.Fatalf("ListAudit page 4: %v", err)
	}
	if len(page4) != 0 {
		t.Fatalf("expected an empty last page, got %+v", page4)
	}

	// Age-based prune removes everything strictly older than the cutoff.
	removed, err := repo.PruneAudit(base+3*step, 0)
	if err != nil {
		t.Fatalf("PruneAudit by age: %v", err)
	}
	if removed != 3 {
		t.Fatalf("PruneAudit by age removed %d rows, want 3", removed)
	}
	entries, err = repo.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit after age prune: %v", err)
	}
	if len(entries) != 2 || entries[0].Target != "plat-4" || entries[1].Target != "plat-3" {
		t.Fatalf("unexpected entries after age prune: %+v", entries)
	}

	// Pruning again with the same cutoff is a no-op.
	removed, err = repo.PruneAudit(base+3*step, 0)
	if err != nil {
		t.Fatalf("second PruneAudit by age: %v", err)
	}
	if removed != 0 {
		t.Fatalf("second PruneAudit by age removed %d rows, want 0", removed)
	}

	// Keep-max prune keeps only the newest N.
	for i := 5; i < 8; i++ {
		if err := repo.AppendAudit(model.AuditEntry{
			AtNs:   base + int64(i)*step,
			Actor:  "admin",
			Action: "platform.create",
			Target: "plat-" + itoa(i),
			Detail: "{}",
		}); err != nil {
			t.Fatalf("AppendAudit(%d): %v", i, err)
		}
	}
	removed, err = repo.PruneAudit(0, 2)
	if err != nil {
		t.Fatalf("PruneAudit by keepMax: %v", err)
	}
	if removed != 3 {
		t.Fatalf("PruneAudit by keepMax removed %d rows, want 3", removed)
	}
	entries, err = repo.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit after keep-max prune: %v", err)
	}
	if len(entries) != 2 || entries[0].Target != "plat-7" || entries[1].Target != "plat-6" {
		t.Fatalf("keep-max prune did not retain the 2 newest entries: %+v", entries)
	}

	// A fully disabled prune removes nothing.
	removed, err = repo.PruneAudit(0, 0)
	if err != nil {
		t.Fatalf("PruneAudit(0, 0): %v", err)
	}
	if removed != 0 {
		t.Fatalf("PruneAudit(0, 0) removed %d rows, want 0", removed)
	}
	entries, err = repo.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit after no-op prune: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("PruneAudit(0, 0) changed the table: %+v", entries)
	}
}

// --- subscription parse report ---

func TestStateRepo_PrismSetSubscriptionParseReport(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	subscription := model.Subscription{
		ID:                        "sub-report",
		Name:                      "Reported",
		URL:                       "https://example.com/sub",
		UpdateIntervalNs:          int64(time.Hour),
		Enabled:                   true,
		EphemeralNodeEvictDelayNs: int64(72 * time.Hour),
		CreatedAtNs:               now,
		UpdatedAtNs:               now,
	}
	if err := repo.UpsertSubscription(subscription); err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}

	if err := repo.SetSubscriptionParseReport("sub-missing", `{"total":0}`); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSubscriptionParseReport(unknown id): got %v, want ErrNotFound", err)
	}

	// A small report is stored verbatim.
	const smallReport = `{"total":12,"alive":9,"dead":3,"filters_applied":["alive"]}`
	if err := repo.SetSubscriptionParseReport(subscription.ID, smallReport); err != nil {
		t.Fatalf("SetSubscriptionParseReport: %v", err)
	}
	list, err := repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(list))
	}
	if list[0].LastParseReportJSON != smallReport {
		t.Fatalf("parse report: got %q, want %q", list[0].LastParseReportJSON, smallReport)
	}

	// An oversized report is replaced by a small, valid JSON marker that does
	// not carry the original body.
	const marker = "oversized-payload-marker"
	oversized := `{"body":"` + marker + strings.Repeat("x", maxParseReportBytes) + `"}`
	if len(oversized) <= maxParseReportBytes {
		t.Fatalf("test fixture too small: got %d bytes, need more than %d", len(oversized), maxParseReportBytes)
	}
	if err := repo.SetSubscriptionParseReport(subscription.ID, oversized); err != nil {
		t.Fatalf("SetSubscriptionParseReport(oversized): %v", err)
	}

	list, err = repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions after oversized report: %v", err)
	}
	stored := list[0].LastParseReportJSON
	if len(stored) > maxParseReportBytes {
		t.Errorf("stored parse report is still %d bytes, want <= %d", len(stored), maxParseReportBytes)
	}
	if !json.Valid([]byte(stored)) {
		t.Errorf("stored parse report is not valid JSON: %q", stored)
	}
	if strings.Contains(stored, marker) {
		t.Errorf("stored parse report still contains the original body: %q", stored)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
		t.Errorf("stored parse report is not a JSON object: %q (%v)", stored, err)
	}

	// The subscription row must survive the oversized write untouched.
	list, err = repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions (second read): %v", err)
	}
	if list[0].ID != subscription.ID || list[0].URL != subscription.URL {
		t.Fatalf("subscription row was mutated: %+v", list[0])
	}
}

// --- platforms: quality policy and rotation ---

func TestStateRepo_PrismPlatformQualityPolicyRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	// A row created before migration 000010 keeps the migration defaults.
	if _, err := repo.db.Exec(`
		INSERT INTO platforms (
			id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
			reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header, allocation_policy,
			passive_circuit_breaker_disabled, updated_at_ns
		) VALUES (
			'plat-preexisting', 'PreExisting', 60000000000, '["^Prism/.*"]', '["us"]',
			'TREAT_AS_EMPTY', 'RANDOM', '', 'BALANCED', 0, 1
		)
	`); err != nil {
		t.Fatalf("seed pre-existing platform row: %v", err)
	}

	preexisting, err := repo.GetPlatform("plat-preexisting")
	if err != nil {
		t.Fatalf("GetPlatform(pre-existing): %v", err)
	}
	if !preexisting.RotationAvoidPreviousIP {
		t.Error("rotation_avoid_previous_ip must default to true for pre-000010 rows")
	}
	if preexisting.ScheduledRotationEnabled {
		t.Error("scheduled_rotation_enabled must default to false for pre-000010 rows")
	}
	if preexisting.ScheduledRotationIntervalNs != 0 {
		t.Errorf("scheduled_rotation_interval_ns: got %d, want 0", preexisting.ScheduledRotationIntervalNs)
	}
	if !preexisting.QualityPolicy.IsEmpty() {
		t.Errorf("quality policy must default to empty, got %+v", preexisting.QualityPolicy)
	}
	minPurity := 80
	excludeTor := false
	platform := model.Platform{
		ID:            "plat-quality",
		Name:          "Quality",
		StickyTTLNs:   int64(30 * time.Minute),
		RegexFilters:  []string{"^Prism/.*"},
		RegionFilters: []string{"us", "jp"},
		QualityPolicy: model.QualityPolicy{
			MinPurity:        &minPurity,
			IPTypes:          []string{"residential", "mobile"},
			AllowedVerdicts:  []string{"favorable", "caution"},
			MinConfidence:    "medium",
			RequireNative:    true,
			RequiredChecks:   map[string]string{"chatgpt": "available"},
			MaxAssessmentAge: "24h0m0s",
			MaxEgressAge:     "1h0m0s",
			UnknownAction:    "exclude",
			ExcludeTor:       &excludeTor,
		},
		ReverseProxyMissAction:      "TREAT_AS_EMPTY",
		AllocationPolicy:            "BALANCED",
		ScheduledRotationEnabled:    true,
		ScheduledRotationIntervalNs: int64(45 * time.Minute),
		RotationAvoidPreviousIP:     false,
		UpdatedAtNs:                 now,
	}
	if err := repo.UpsertPlatform(platform); err != nil {
		t.Fatalf("UpsertPlatform: %v", err)
	}

	assertPlatformPrismFields := func(t *testing.T, got model.Platform, want model.Platform) {
		t.Helper()
		if got.QualityPolicy.MinPurity == nil || *got.QualityPolicy.MinPurity != *want.QualityPolicy.MinPurity {
			t.Errorf("quality_policy.min_purity: got %v, want %v", got.QualityPolicy.MinPurity, *want.QualityPolicy.MinPurity)
		}
		if !reflect.DeepEqual(got.QualityPolicy.IPTypes, want.QualityPolicy.IPTypes) {
			t.Errorf("quality_policy.ip_types: got %v, want %v", got.QualityPolicy.IPTypes, want.QualityPolicy.IPTypes)
		}
		if !reflect.DeepEqual(got.QualityPolicy.AllowedVerdicts, want.QualityPolicy.AllowedVerdicts) {
			t.Errorf("quality_policy.allowed_verdicts: got %v, want %v", got.QualityPolicy.AllowedVerdicts, want.QualityPolicy.AllowedVerdicts)
		}
		if got.QualityPolicy.MinConfidence != want.QualityPolicy.MinConfidence {
			t.Errorf("quality_policy.min_confidence: got %q, want %q", got.QualityPolicy.MinConfidence, want.QualityPolicy.MinConfidence)
		}
		if got.QualityPolicy.RequireNative != want.QualityPolicy.RequireNative {
			t.Errorf("quality_policy.require_native: got %v, want %v", got.QualityPolicy.RequireNative, want.QualityPolicy.RequireNative)
		}
		if !reflect.DeepEqual(got.QualityPolicy.RequiredChecks, want.QualityPolicy.RequiredChecks) {
			t.Errorf("quality_policy.required_checks: got %v, want %v", got.QualityPolicy.RequiredChecks, want.QualityPolicy.RequiredChecks)
		}
		if got.QualityPolicy.MaxAssessmentAge != want.QualityPolicy.MaxAssessmentAge {
			t.Errorf("quality_policy.max_assessment_age: got %q, want %q", got.QualityPolicy.MaxAssessmentAge, want.QualityPolicy.MaxAssessmentAge)
		}
		if got.QualityPolicy.MaxEgressAge != want.QualityPolicy.MaxEgressAge {
			t.Errorf("quality_policy.max_egress_age: got %q, want %q", got.QualityPolicy.MaxEgressAge, want.QualityPolicy.MaxEgressAge)
		}
		if got.QualityPolicy.UnknownAction != want.QualityPolicy.UnknownAction {
			t.Errorf("quality_policy.unknown_action: got %q, want %q", got.QualityPolicy.UnknownAction, want.QualityPolicy.UnknownAction)
		}
		if got.QualityPolicy.ExcludeTor == nil || want.QualityPolicy.ExcludeTor == nil ||
			*got.QualityPolicy.ExcludeTor != *want.QualityPolicy.ExcludeTor {
			t.Errorf("quality_policy.exclude_tor: got %v, want %v", got.QualityPolicy.ExcludeTor, want.QualityPolicy.ExcludeTor)
		}
		if got.QualityPolicy.ExcludeHighRisk != nil || want.QualityPolicy.ExcludeHighRisk != nil {
			t.Errorf("quality_policy.exclude_high_risk: got %v, want nil", got.QualityPolicy.ExcludeHighRisk)
		}
		if got.ScheduledRotationEnabled != want.ScheduledRotationEnabled {
			t.Errorf("scheduled_rotation_enabled: got %v, want %v", got.ScheduledRotationEnabled, want.ScheduledRotationEnabled)
		}
		if got.ScheduledRotationIntervalNs != want.ScheduledRotationIntervalNs {
			t.Errorf("scheduled_rotation_interval_ns: got %d, want %d", got.ScheduledRotationIntervalNs, want.ScheduledRotationIntervalNs)
		}
		if got.RotationAvoidPreviousIP != want.RotationAvoidPreviousIP {
			t.Errorf("rotation_avoid_previous_ip: got %v, want %v", got.RotationAvoidPreviousIP, want.RotationAvoidPreviousIP)
		}
	}

	got, err := repo.GetPlatform(platform.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	assertPlatformPrismFields(t, *got, platform)

	list, err := repo.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 platforms, got %d", len(list))
	}
	var listed *model.Platform
	for i := range list {
		if list[i].ID == platform.ID {
			listed = &list[i]
		}
	}
	if listed == nil {
		t.Fatalf("ListPlatforms did not return %q: %+v", platform.ID, list)
	}
	assertPlatformPrismFields(t, *listed, platform)

	// Updating through the upsert path writes the new policy back.
	updatedPurity := 95
	updated := platform
	updated.QualityPolicy.MinPurity = &updatedPurity
	updated.RotationAvoidPreviousIP = true
	updated.ScheduledRotationEnabled = false
	updated.ScheduledRotationIntervalNs = 0
	updated.UpdatedAtNs = now + 1
	if err := repo.UpsertPlatform(updated); err != nil {
		t.Fatalf("UpsertPlatform (update): %v", err)
	}
	got, err = repo.GetPlatform(platform.ID)
	if err != nil {
		t.Fatalf("GetPlatform after update: %v", err)
	}
	assertPlatformPrismFields(t, *got, updated)
}

// --- subscriptions: import options ---

func TestStateRepo_PrismSubscriptionImportOptionsRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	withIntel := model.Subscription{
		ID:                        "sub-intel",
		Name:                      "WithIntel",
		URL:                       "https://example.com/intel",
		UpdateIntervalNs:          int64(time.Hour),
		Enabled:                   true,
		EphemeralNodeEvictDelayNs: int64(72 * time.Hour),
		AutoIntel:                 true,
		UserAgent:                 "Prism/1.0 (+https://example.com/bot)",
		CreatedAtNs:               now,
		UpdatedAtNs:               now,
	}
	withoutIntel := withIntel
	withoutIntel.ID = "sub-plain"
	withoutIntel.Name = "Plain"
	withoutIntel.URL = "https://example.com/plain"
	withoutIntel.AutoIntel = false
	withoutIntel.UserAgent = ""

	for _, s := range []model.Subscription{withIntel, withoutIntel} {
		if err := repo.UpsertSubscription(s); err != nil {
			t.Fatalf("UpsertSubscription(%s): %v", s.ID, err)
		}
	}

	// A row inserted without the 000011 columns keeps the migration defaults.
	if _, err := repo.db.Exec(`
		INSERT INTO subscriptions (
			id, name, source_type, url, content, update_interval_ns, enabled,
			ephemeral, ephemeral_node_evict_delay_ns, incremental_alive_nodes,
			created_at_ns, updated_at_ns
		) VALUES (
			'sub-legacy', 'Legacy', 'remote', 'https://example.com/legacy', '',
			3600000000000, 1, 0, 259200000000000, 0, 1, 1
		)
	`); err != nil {
		t.Fatalf("seed pre-000011 subscription row: %v", err)
	}

	list, err := repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 subscriptions, got %d", len(list))
	}
	byID := make(map[string]model.Subscription, len(list))
	for _, s := range list {
		byID[s.ID] = s
	}

	for _, want := range []model.Subscription{withIntel, withoutIntel} {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("ListSubscriptions did not return %q: %+v", want.ID, list)
		}
		if got.AutoIntel != want.AutoIntel {
			t.Errorf("%s: auto_intel got %v, want %v", want.ID, got.AutoIntel, want.AutoIntel)
		}
		if got.UserAgent != want.UserAgent {
			t.Errorf("%s: user_agent got %q, want %q", want.ID, got.UserAgent, want.UserAgent)
		}
	}

	legacy, ok := byID["sub-legacy"]
	if !ok {
		t.Fatalf("ListSubscriptions did not return the pre-000011 row: %+v", list)
	}
	if !legacy.AutoIntel {
		t.Error("auto_intel must default to true for pre-000011 rows")
	}
	if legacy.UserAgent != "" {
		t.Errorf("user_agent must default to empty for pre-000011 rows, got %q", legacy.UserAgent)
	}

	// The upsert path updates the import options in place.
	withIntel.UserAgent = "Prism/2.0"
	if err := repo.UpsertSubscription(withIntel); err != nil {
		t.Fatalf("UpsertSubscription (update): %v", err)
	}
	list, err = repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions after update: %v", err)
	}
	for _, s := range list {
		if s.ID == withIntel.ID && (s.UserAgent != "Prism/2.0" || !s.AutoIntel) {
			t.Fatalf("import options did not update: %+v", s)
		}
	}
}
