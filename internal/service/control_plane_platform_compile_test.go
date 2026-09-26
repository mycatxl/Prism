package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/platform"
	"prism/internal/topology"
)

// compileAndUpsertPlatform is the shared write path of CreatePlatform,
// UpdatePlatform and ResetPlatformToDefault. Its error mapping (name conflict,
// runtime compile failure, broken store) is not reachable through the handler
// tests, which only exercise the pre-validation above it.

func newPlatformCompileFixture(t *testing.T) *ControlPlaneService {
	t.Helper()
	engine, closeEngine := newStateEngineForTest(t)
	t.Cleanup(closeEngine)
	subMgr := topology.NewSubscriptionManager()
	return &ControlPlaneService{
		Engine: engine,
		Pool:   newNodeListTestPool(subMgr),
		SubMgr: subMgr,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}
}

func TestCompileAndUpsertPlatform_DuplicateNameIsAConflict(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	name := "compile-duplicate"
	created, err := cp.CreatePlatform(CreatePlatformRequest{Name: &name})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if created.Name != name {
		t.Fatalf("created platform name = %q, want %q", created.Name, name)
	}

	// The unique index on platforms.name is the source of the conflict.
	_, err = cp.CreatePlatform(CreatePlatformRequest{Name: intelTestStrPtr("  " + name + "  ")})
	if !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("CreatePlatform(duplicate name) = %v, want CONFLICT", err)
	}
	if err != nil && err.Error() != "platform name already exists" {
		t.Fatalf("message = %q, want %q", err.Error(), "platform name already exists")
	}
}

func TestCompileAndUpsertPlatform_InvalidRegexIsAnInvalidArgument(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	// validatePlatformConfig does not compile the tag filters; the runtime
	// build inside compileAndUpsertPlatform does, and reports the client error.
	_, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:         intelTestStrPtr("compile-bad-regex"),
		RegexFilters: []string{"("},
	})
	if !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("CreatePlatform(bad regex) = %v, want INVALID_ARGUMENT", err)
	}
	if err != nil && err.Error() != "regex_filters[0]: invalid regex: error parsing regexp: missing closing ): `(`" {
		t.Fatalf("message = %q", err.Error())
	}

	// The must/must_not prefixes are compiled the same way.
	if _, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:         intelTestStrPtr("compile-bad-regex-2"),
		RegexFilters: []string{"!["},
	}); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("CreatePlatform(bad must_not regex) = %v, want INVALID_ARGUMENT", err)
	}
}

func TestCompileAndUpsertPlatform_BrokenStoreIsInternal(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	subMgr := topology.NewSubscriptionManager()
	cp := &ControlPlaneService{
		Engine: engine,
		Pool:   newNodeListTestPool(subMgr),
		SubMgr: subMgr,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}

	closeEngine()

	_, err := cp.CreatePlatform(CreatePlatformRequest{Name: intelTestStrPtr("compile-broken-store")})
	if !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("CreatePlatform with a broken store = %v, want INTERNAL", err)
	}
}

func TestCompileAndUpsertPlatform_AcceptsTheEnvironmentDefaults(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	// A second platform with a different name is persisted and registered.
	name := "compile-accepted"
	created, err := cp.CreatePlatform(CreatePlatformRequest{Name: &name})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatalf("platform %s was not registered", created.ID)
	}
	if plat.Name != name || plat.StickyTTLNs != int64(30*time.Minute) {
		t.Fatalf("runtime platform = %+v", plat)
	}
	if plat.AllocationPolicy != platform.AllocationPolicyBalanced {
		t.Fatalf("allocation policy = %q, want %q", plat.AllocationPolicy, platform.AllocationPolicyBalanced)
	}
	stored, err := cp.Engine.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	if stored.Name != name {
		t.Fatalf("stored name = %q, want %q", stored.Name, name)
	}
}

// --- platform field validation (control_plane_platform.go) -------------------

func TestCreatePlatformRejectsInvalidFields(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	tests := []struct {
		name    string
		req     CreatePlatformRequest
		message string
		prefix  string
	}{
		{
			name:    "sticky ttl must be positive",
			req:     CreatePlatformRequest{StickyTTL: intelTestStrPtr("0s")},
			message: "sticky_ttl: must be > 0",
		},
		{
			name:   "sticky ttl must parse",
			req:    CreatePlatformRequest{StickyTTL: intelTestStrPtr("not-a-duration")},
			prefix: "sticky_ttl: ",
		},
		{
			name:   "miss action enum",
			req:    CreatePlatformRequest{ReverseProxyMissAction: intelTestStrPtr("nope")},
			prefix: "reverse_proxy_miss_action: must be",
		},
		{
			name:   "empty account behaviour enum",
			req:    CreatePlatformRequest{ReverseProxyEmptyAccountBehavior: intelTestStrPtr("nope")},
			prefix: "reverse_proxy_empty_account_behavior: must be",
		},
		{
			name:   "allocation policy enum",
			req:    CreatePlatformRequest{AllocationPolicy: intelTestStrPtr("nope")},
			prefix: "allocation_policy: must be",
		},
		{
			name:   "fixed header is required for FIXED_HEADER",
			req:    CreatePlatformRequest{ReverseProxyEmptyAccountBehavior: intelTestStrPtr("FIXED_HEADER")},
			prefix: "reverse_proxy_fixed_account_header: required",
		},
		{
			name:   "rotation interval must parse",
			req:    CreatePlatformRequest{ScheduledRotationInterval: intelTestStrPtr("not-a-duration")},
			prefix: "scheduled_rotation_interval: ",
		},
		{
			name:    "rotation interval must be non-negative",
			req:     CreatePlatformRequest{ScheduledRotationInterval: intelTestStrPtr("-1s")},
			message: "scheduled_rotation_interval must be non-negative",
		},
		{
			name:   "region filters are validated",
			req:    CreatePlatformRequest{RegionFilters: []string{"us", "US"}},
			prefix: "region_filters[1]: must be",
		},
		{
			name:    "purity range",
			req:     CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{MinPurity: intelTestIntPtr(101)}},
			message: "quality_policy.min_purity must be between 0 and 100",
		},
		{
			name:   "ip type enum",
			req:    CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{IPTypes: []string{"nope"}}},
			prefix: "quality_policy.ip_types:",
		},
		{
			name:   "verdict enum",
			req:    CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{AllowedVerdicts: []string{"nope"}}},
			prefix: "quality_policy.allowed_verdicts:",
		},
		{
			name:    "confidence enum",
			req:     CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{MinConfidence: "nope"}},
			message: "quality_policy.min_confidence must be low, medium or high",
		},
		{
			name:    "unknown action enum",
			req:     CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{UnknownAction: "nope"}},
			message: "quality_policy.unknown_action must be allow or exclude",
		},
		{
			name:   "assessment age must parse",
			req:    CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{MaxAssessmentAge: "not-a-duration"}},
			prefix: "quality_policy.max_assessment_age: ",
		},
		{
			name:    "egress age must be non-negative",
			req:     CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{MaxEgressAge: "-1s"}},
			message: "quality_policy.max_egress_age must be non-negative",
		},
		{
			name:   "required check id shape",
			req:    CreatePlatformRequest{QualityPolicy: &model.QualityPolicy{RequiredChecks: map[string]string{"Not-A-Check": "available"}}},
			prefix: "quality_policy.required_checks:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			req.Name = intelTestStrPtr("validation-target")
			_, err := cp.CreatePlatform(req)
			if !isServiceErrorCode(err, "INVALID_ARGUMENT") {
				t.Fatalf("CreatePlatform = %v, want INVALID_ARGUMENT", err)
			}
			if tt.message != "" && err.Error() != tt.message {
				t.Fatalf("message = %q, want %q", err.Error(), tt.message)
			}
			if tt.prefix != "" && !strings.HasPrefix(err.Error(), tt.prefix) {
				t.Fatalf("message = %q, want prefix %q", err.Error(), tt.prefix)
			}
		})
	}
}

func TestCreatePlatformAcceptsEveryValidatedField(t *testing.T) {
	cp := newPlatformCompileFixture(t)

	resp, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                             intelTestStrPtr("validated-platform"),
		StickyTTL:                        intelTestStrPtr("45m"),
		RegexFilters:                     []string{"tag"},
		RegionFilters:                    []string{"jp", "!us"},
		ReverseProxyMissAction:           intelTestStrPtr("REJECT"),
		ReverseProxyEmptyAccountBehavior: intelTestStrPtr("RANDOM"),
		AllocationPolicy:                 intelTestStrPtr("PREFER_IDLE_IP"),
		PassiveCircuitBreakerDisabled:    intelTestBoolPtr(true),
		ScheduledRotationInterval:        intelTestStrPtr("15m"),
		ScheduledRotationEnabled:         intelTestBoolPtr(true),
		RotationAvoidPreviousIP:          intelTestBoolPtr(true),
		QualityPolicy: &model.QualityPolicy{
			MinPurity:      intelTestIntPtr(50),
			RequiredChecks: map[string]string{"chatgpt": "available"},
		},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if resp.StickyTTL != (45 * time.Minute).String() {
		t.Errorf("sticky_ttl = %q, want 45m", resp.StickyTTL)
	}
	if resp.ReverseProxyMissAction != "REJECT" || resp.AllocationPolicy != "PREFER_IDLE_IP" {
		t.Errorf("miss action/allocation policy = %q/%q", resp.ReverseProxyMissAction, resp.AllocationPolicy)
	}
	if !resp.PassiveCircuitBreakerDisabled {
		t.Error("passive_circuit_breaker_disabled was not applied")
	}
	if resp.ScheduledRotationInterval != (15*time.Minute).String() || !resp.ScheduledRotationEnabled || !resp.RotationAvoidPreviousIP {
		t.Errorf("rotation = %q/%v/%v", resp.ScheduledRotationInterval, resp.ScheduledRotationEnabled, resp.RotationAvoidPreviousIP)
	}
	if resp.QualityPolicy.MinPurity == nil || *resp.QualityPolicy.MinPurity != 50 {
		t.Errorf("quality policy = %+v", resp.QualityPolicy)
	}

	plat, ok := cp.Pool.GetPlatform(resp.ID)
	if !ok {
		t.Fatalf("platform %s was not registered", resp.ID)
	}
	if plat.ReverseProxyMissAction != "REJECT" ||
		plat.AllocationPolicy != platform.AllocationPolicyPreferIdleIP ||
		!plat.PassiveCircuitBreakerDisabled ||
		!plat.ScheduledRotationEnabled ||
		plat.ScheduledRotationIntervalNs != int64(15*time.Minute) ||
		!plat.RotationAvoidPreviousIP {
		t.Fatalf("runtime platform = %+v", plat)
	}
	if len(plat.RegexFilters.Any) != 1 || len(plat.RegionFilters) != 2 {
		t.Fatalf("runtime filters = %+v / %v", plat.RegexFilters, plat.RegionFilters)
	}
}

func TestPlatformValidationHelpers(t *testing.T) {
	if got := normalizePlatformMissAction("  TREAT_AS_EMPTY  "); got != "TREAT_AS_EMPTY" {
		t.Errorf("normalizePlatformMissAction(valid) = %q", got)
	}
	if got := normalizePlatformMissAction("nope"); got != "" {
		t.Errorf("normalizePlatformMissAction(invalid) = %q, want empty", got)
	}
	if err := validatePlatformMissAction("REJECT"); err != nil {
		t.Errorf("validatePlatformMissAction(REJECT) = %v", err)
	}
	if err := validatePlatformMissAction("reject"); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Errorf("validatePlatformMissAction is case-sensitive: %v", err)
	}
	for _, policy := range []platform.AllocationPolicy{
		platform.AllocationPolicyBalanced,
		platform.AllocationPolicyPreferLowLatency,
		platform.AllocationPolicyPreferIdleIP,
	} {
		if err := validatePlatformAllocationPolicy(string(policy)); err != nil {
			t.Errorf("validatePlatformAllocationPolicy(%q) = %v", policy, err)
		}
	}
	if err := validatePlatformAllocationPolicy("nope"); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Errorf("validatePlatformAllocationPolicy(nope) = %v", err)
	}
	checkIDs := map[string]bool{
		"": false, "chatgpt": true, "a_b9": true, "A": false, "a-b": false,
		"a b": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	}
	for id, want := range checkIDs {
		if got := validCheckID(id); got != want {
			t.Errorf("validCheckID(%q) = %v, want %v", id, got, want)
		}
	}
	svcErr := internal("persist platform", errors.New("disk full"))
	if svcErr.Error() != "persist platform" {
		t.Errorf("ServiceError.Error() = %q", svcErr.Error())
	}
	if !strings.Contains(svcErr.Unwrap().Error(), "disk full") {
		t.Errorf("ServiceError.Unwrap() = %v", svcErr.Unwrap())
	}
}
