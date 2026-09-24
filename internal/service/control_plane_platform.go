package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/quality"
	"prism/internal/state"
)

// ------------------------------------------------------------------
// Platform
// ------------------------------------------------------------------

// PlatformResponse is the API response model for a platform.
type PlatformResponse struct {
	ID                               string              `json:"id"`
	Name                             string              `json:"name"`
	StickyTTL                        string              `json:"sticky_ttl"`
	RegexFilters                     []string            `json:"regex_filters"`
	RegionFilters                    []string            `json:"region_filters"`
	RoutableNodeCount                int                 `json:"routable_node_count"`
	ReverseProxyMissAction           string              `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior string              `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   string              `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 string              `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    bool                `json:"passive_circuit_breaker_disabled"`
	ScheduledRotationInterval        string              `json:"scheduled_rotation_interval"`
	ScheduledRotationEnabled         bool                `json:"scheduled_rotation_enabled"`
	RotationAvoidPreviousIP          bool                `json:"rotation_avoid_previous_ip"`
	QualityPolicy                    model.QualityPolicy `json:"quality_policy"`
	UpdatedAt                        string              `json:"updated_at"`
}

func platformToResponse(p model.Platform) PlatformResponse {
	behavior := normalizePlatformEmptyAccountBehavior(p.ReverseProxyEmptyAccountBehavior)
	fixedHeader := normalizeHeaderFieldName(p.ReverseProxyFixedAccountHeader)
	return PlatformResponse{
		ID:                               p.ID,
		Name:                             p.Name,
		StickyTTL:                        time.Duration(p.StickyTTLNs).String(),
		RegexFilters:                     append([]string(nil), p.RegexFilters...),
		RegionFilters:                    append([]string(nil), p.RegionFilters...),
		RoutableNodeCount:                0,
		ReverseProxyMissAction:           p.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: behavior,
		ReverseProxyFixedAccountHeader:   fixedHeader,
		AllocationPolicy:                 p.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    p.PassiveCircuitBreakerDisabled,
		ScheduledRotationInterval:        time.Duration(p.ScheduledRotationIntervalNs).String(),
		ScheduledRotationEnabled:         p.ScheduledRotationEnabled,
		RotationAvoidPreviousIP:          p.RotationAvoidPreviousIP,
		QualityPolicy:                    p.QualityPolicy,
		UpdatedAt:                        time.Unix(0, p.UpdatedAtNs).UTC().Format(time.RFC3339Nano),
	}
}

func (s *ControlPlaneService) withRoutableNodeCount(resp PlatformResponse) PlatformResponse {
	if s == nil || s.Pool == nil {
		return resp
	}
	plat, ok := s.Pool.GetPlatform(resp.ID)
	if !ok || plat == nil {
		return resp
	}
	resp.RoutableNodeCount = plat.View().Size()
	return resp
}

type platformConfig struct {
	Name                             string
	StickyTTLNs                      int64
	RegexFilters                     []string
	RegionFilters                    []string
	QualityPolicy                    model.QualityPolicy
	ReverseProxyMissAction           string
	ReverseProxyEmptyAccountBehavior string
	ReverseProxyFixedAccountHeader   string
	AllocationPolicy                 string
	PassiveCircuitBreakerDisabled    bool
	ScheduledRotationIntervalNs      int64
	ScheduledRotationEnabled         bool
	RotationAvoidPreviousIP          bool
}

func normalizePlatformMissAction(raw string) string {
	normalized := platform.NormalizeReverseProxyMissAction(raw)
	if normalized == "" {
		return ""
	}
	return string(normalized)
}

func normalizePlatformEmptyAccountBehavior(raw string) string {
	if platform.ReverseProxyEmptyAccountBehavior(raw).IsValid() {
		return raw
	}
	return string(platform.ReverseProxyEmptyAccountBehaviorRandom)
}

func (s *ControlPlaneService) defaultPlatformConfig(name string) platformConfig {
	if s == nil || s.EnvCfg == nil {
		return platformConfig{Name: name}
	}
	return platformConfig{
		Name:                   name,
		StickyTTLNs:            int64(s.EnvCfg.DefaultPlatformStickyTTL),
		RegexFilters:           append([]string(nil), s.EnvCfg.DefaultPlatformRegexFilters...),
		RegionFilters:          append([]string(nil), s.EnvCfg.DefaultPlatformRegionFilters...),
		ReverseProxyMissAction: s.EnvCfg.DefaultPlatformReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: normalizePlatformEmptyAccountBehavior(
			s.EnvCfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		),
		ReverseProxyFixedAccountHeader: normalizeHeaderFieldName(
			s.EnvCfg.DefaultPlatformReverseProxyFixedAccountHeader,
		),
		AllocationPolicy: s.EnvCfg.DefaultPlatformAllocationPolicy,
	}
}

func platformConfigFromModel(mp model.Platform) platformConfig {
	return platformConfig{
		Name:                             mp.Name,
		StickyTTLNs:                      mp.StickyTTLNs,
		RegexFilters:                     append([]string(nil), mp.RegexFilters...),
		RegionFilters:                    append([]string(nil), mp.RegionFilters...),
		ReverseProxyMissAction:           mp.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: normalizePlatformEmptyAccountBehavior(mp.ReverseProxyEmptyAccountBehavior),
		ReverseProxyFixedAccountHeader:   normalizeHeaderFieldName(mp.ReverseProxyFixedAccountHeader),
		AllocationPolicy:                 mp.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    mp.PassiveCircuitBreakerDisabled,
		ScheduledRotationIntervalNs:      mp.ScheduledRotationIntervalNs,
		ScheduledRotationEnabled:         mp.ScheduledRotationEnabled,
		RotationAvoidPreviousIP:          mp.RotationAvoidPreviousIP,
		QualityPolicy:                    mp.QualityPolicy,
	}
}

func (cfg platformConfig) toModel(id string, updatedAtNs int64) model.Platform {
	return model.Platform{
		ID:                               id,
		Name:                             cfg.Name,
		StickyTTLNs:                      cfg.StickyTTLNs,
		RegexFilters:                     append([]string(nil), cfg.RegexFilters...),
		RegionFilters:                    append([]string(nil), cfg.RegionFilters...),
		QualityPolicy:                    cfg.QualityPolicy,
		ReverseProxyMissAction:           cfg.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: cfg.ReverseProxyEmptyAccountBehavior,
		ReverseProxyFixedAccountHeader:   cfg.ReverseProxyFixedAccountHeader,
		AllocationPolicy:                 cfg.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    cfg.PassiveCircuitBreakerDisabled,
		ScheduledRotationIntervalNs:      cfg.ScheduledRotationIntervalNs,
		ScheduledRotationEnabled:         cfg.ScheduledRotationEnabled,
		RotationAvoidPreviousIP:          cfg.RotationAvoidPreviousIP,
		UpdatedAtNs:                      updatedAtNs,
	}
}

func (cfg platformConfig) toRuntime(id string) (*platform.Platform, error) {
	compiledRegexFilters, err := platform.CompileRegexFilters(cfg.RegexFilters)
	if err != nil {
		return nil, err
	}
	plat := platform.NewConfiguredPlatform(
		id,
		cfg.Name,
		compiledRegexFilters,
		cfg.RegionFilters,
		cfg.QualityPolicy,
		cfg.StickyTTLNs,
		cfg.ReverseProxyMissAction,
		cfg.ReverseProxyEmptyAccountBehavior,
		cfg.ReverseProxyFixedAccountHeader,
		cfg.AllocationPolicy,
		cfg.PassiveCircuitBreakerDisabled,
	)
	plat.RotationAvoidPreviousIP = cfg.RotationAvoidPreviousIP
	return plat, nil
}

func validatePlatformMissAction(raw string) *ServiceError {
	if normalizePlatformMissAction(raw) != "" {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"reverse_proxy_miss_action: must be %s or %s",
		platform.ReverseProxyMissActionTreatAsEmpty,
		platform.ReverseProxyMissActionReject,
	))
}

func validatePlatformEmptyAccountBehavior(raw string) *ServiceError {
	if platform.ReverseProxyEmptyAccountBehavior(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"reverse_proxy_empty_account_behavior: must be %s, %s, or %s",
		platform.ReverseProxyEmptyAccountBehaviorRandom,
		platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
		platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule,
	))
}

func normalizeHeaderFieldName(raw string) string {
	normalized, _, err := platform.NormalizeFixedAccountHeaders(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return normalized
}

func validatePlatformEmptyAccountConfig(cfg *platformConfig) *ServiceError {
	if cfg == nil {
		return invalidArg("platform config is required")
	}
	if err := validatePlatformEmptyAccountBehavior(cfg.ReverseProxyEmptyAccountBehavior); err != nil {
		return err
	}
	normalizedFixedHeaders, fixedHeaders, err := platform.NormalizeFixedAccountHeaders(cfg.ReverseProxyFixedAccountHeader)
	if err != nil {
		return invalidArg("reverse_proxy_fixed_account_header: " + err.Error())
	}
	cfg.ReverseProxyFixedAccountHeader = normalizedFixedHeaders
	if cfg.ReverseProxyEmptyAccountBehavior == string(platform.ReverseProxyEmptyAccountBehaviorFixedHeader) &&
		len(fixedHeaders) == 0 {
		return invalidArg(
			"reverse_proxy_fixed_account_header: required when reverse_proxy_empty_account_behavior is FIXED_HEADER",
		)
	}
	return nil
}

func validatePlatformAllocationPolicy(raw string) *ServiceError {
	if platform.AllocationPolicy(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"allocation_policy: must be %s, %s, or %s",
		platform.AllocationPolicyBalanced,
		platform.AllocationPolicyPreferLowLatency,
		platform.AllocationPolicyPreferIdleIP,
	))
}

func setPlatformStickyTTL(cfg *platformConfig, d time.Duration) *ServiceError {
	if d <= 0 {
		return invalidArg("sticky_ttl: must be > 0")
	}
	cfg.StickyTTLNs = int64(d)
	return nil
}

func setPlatformMissAction(cfg *platformConfig, missAction string) *ServiceError {
	if err := validatePlatformMissAction(missAction); err != nil {
		return err
	}
	cfg.ReverseProxyMissAction = normalizePlatformMissAction(missAction)
	return nil
}

func setPlatformEmptyAccountBehavior(cfg *platformConfig, behavior string) *ServiceError {
	if err := validatePlatformEmptyAccountBehavior(behavior); err != nil {
		return err
	}
	cfg.ReverseProxyEmptyAccountBehavior = behavior
	return nil
}

func setPlatformAllocationPolicy(cfg *platformConfig, policy string) *ServiceError {
	if err := validatePlatformAllocationPolicy(policy); err != nil {
		return err
	}
	cfg.AllocationPolicy = policy
	return nil
}

func validatePlatformConfig(cfg *platformConfig, validateRegionFilters bool) *ServiceError {
	if validateRegionFilters {
		if err := platform.ValidateRegionFilters(cfg.RegionFilters); err != nil {
			return invalidArg(err.Error())
		}
	}
	if err := validatePlatformEmptyAccountConfig(cfg); err != nil {
		return err
	}
	return validateQualityPolicy(cfg.QualityPolicy)
}

// validateQualityPolicy enforces the WP10 §2.1 enum, range and duration rules.
func validateQualityPolicy(policy model.QualityPolicy) *ServiceError {
	if policy.MinPurity != nil && (*policy.MinPurity < 0 || *policy.MinPurity > 100) {
		return invalidArg("quality_policy.min_purity must be between 0 and 100")
	}
	for _, ipType := range policy.IPTypes {
		if !qualityIPTypes[ipType] {
			return invalidArg(fmt.Sprintf(
				"quality_policy.ip_types: %q must be one of residential, mobile, business, wireless, datacenter, non_residential",
				ipType,
			))
		}
	}
	for _, verdict := range policy.AllowedVerdicts {
		if !qualityVerdicts[verdict] {
			return invalidArg(fmt.Sprintf(
				"quality_policy.allowed_verdicts: %q must be one of favorable, caution, review, high_risk, conflicting, incomplete, pending",
				verdict,
			))
		}
	}
	if policy.MinConfidence != "" && !qualityConfidences[policy.MinConfidence] {
		return invalidArg("quality_policy.min_confidence must be low, medium or high")
	}
	switch policy.UnknownAction {
	case "", "allow", "exclude":
	default:
		return invalidArg("quality_policy.unknown_action must be allow or exclude")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"max_assessment_age", policy.MaxAssessmentAge},
		{"max_egress_age", policy.MaxEgressAge},
	} {
		if field.value == "" {
			continue
		}
		d, err := time.ParseDuration(field.value)
		if err != nil {
			return invalidArg("quality_policy." + field.name + ": " + err.Error())
		}
		if d < 0 {
			return invalidArg("quality_policy." + field.name + " must be non-negative")
		}
	}
	for id := range policy.RequiredChecks {
		if !validCheckID(id) {
			return invalidArg("quality_policy.required_checks: " + strconv.Quote(id) + " is not a valid check id")
		}
	}
	return nil
}

// qualityIPTypes, qualityVerdicts and qualityConfidences mirror the enum values
// of WP10 §2.1.
var qualityIPTypes = map[string]bool{
	"residential": true, "mobile": true, "business": true,
	"wireless": true, "datacenter": true, "non_residential": true,
}

var qualityVerdicts = map[string]bool{
	"favorable": true, "caution": true, "review": true,
	"high_risk": true, "conflicting": true, "incomplete": true, "pending": true,
}

var qualityConfidences = map[string]bool{"low": true, "medium": true, "high": true}

// validCheckID matches the WP09 rule id shape ([a-z0-9_]+). The live rule set is
// not consulted here because the checks registry belongs to the intel service;
// an unknown id simply never passes admission (fail-closed).
func validCheckID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (s *ControlPlaneService) compileAndUpsertPlatform(id string, cfg platformConfig) (model.Platform, *platform.Platform, *ServiceError) {
	if err := platform.ValidatePlatformName(cfg.Name); err != nil {
		return model.Platform{}, nil, invalidArg("name: " + err.Error())
	}

	plat, err := cfg.toRuntime(id)
	if err != nil {
		return model.Platform{}, nil, invalidArg(err.Error())
	}
	mp := cfg.toModel(id, time.Now().UnixNano())
	if err := s.Engine.UpsertPlatform(mp); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return model.Platform{}, nil, conflict("platform name already exists")
		}
		if strings.HasPrefix(err.Error(), "platform name: ") {
			return model.Platform{}, nil, invalidArg("name: " + strings.TrimPrefix(err.Error(), "platform name: "))
		}
		return model.Platform{}, nil, internal("persist platform", err)
	}
	return mp, plat, nil
}

// ListPlatforms returns all platforms from the database.
func (s *ControlPlaneService) ListPlatforms() ([]PlatformResponse, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("list platforms", fmt.Errorf("service not initialized"))
	}
	platforms, err := s.Engine.ListPlatforms()
	if err != nil {
		return nil, internal("list platforms", err)
	}
	resp := make([]PlatformResponse, len(platforms))
	for i, p := range platforms {
		resp[i] = s.withRoutableNodeCount(platformToResponse(p))
	}
	return resp, nil
}

func (s *ControlPlaneService) getPlatformModel(id string) (*model.Platform, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("get platform", fmt.Errorf("service not initialized"))
	}
	p, err := s.Engine.GetPlatform(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}
	return p, nil
}

// GetPlatform returns a single platform by ID.
func (s *ControlPlaneService) GetPlatform(id string) (*PlatformResponse, error) {
	mp, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	r := s.withRoutableNodeCount(platformToResponse(*mp))
	return &r, nil
}

// CreatePlatformRequest holds create platform parameters.
type CreatePlatformRequest struct {
	Name                             *string              `json:"name"`
	StickyTTL                        *string              `json:"sticky_ttl"`
	RegexFilters                     []string             `json:"regex_filters"`
	RegionFilters                    []string             `json:"region_filters"`
	ReverseProxyMissAction           *string              `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior *string              `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   *string              `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 *string              `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    *bool                `json:"passive_circuit_breaker_disabled"`
	ScheduledRotationInterval        *string              `json:"scheduled_rotation_interval"`
	ScheduledRotationEnabled         *bool                `json:"scheduled_rotation_enabled"`
	RotationAvoidPreviousIP          *bool                `json:"rotation_avoid_previous_ip"`
	QualityPolicy                    *model.QualityPolicy `json:"quality_policy"`
}

// CreatePlatform creates a new platform.
func (s *ControlPlaneService) CreatePlatform(req CreatePlatformRequest) (*PlatformResponse, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("create platform", fmt.Errorf("service not initialized"))
	}
	// Validate name.
	if req.Name == nil {
		return nil, invalidArg("name is required")
	}
	name := platform.NormalizePlatformName(*req.Name)
	if name == "" {
		return nil, invalidArg("name is required")
	}
	if err := platform.ValidatePlatformName(name); err != nil {
		return nil, invalidArg("name: " + err.Error())
	}
	if name == platform.DefaultPlatformName {
		return nil, conflict("cannot use reserved name 'Default'")
	}

	// Apply defaults from env and overlay request fields.
	cfg := s.defaultPlatformConfig(name)
	if req.StickyTTL != nil {
		d, err := time.ParseDuration(*req.StickyTTL)
		if err != nil {
			return nil, invalidArg("sticky_ttl: " + err.Error())
		}
		if err := setPlatformStickyTTL(&cfg, d); err != nil {
			return nil, err
		}
	}
	if req.RegexFilters != nil {
		cfg.RegexFilters = req.RegexFilters
	}
	if req.RegionFilters != nil {
		cfg.RegionFilters = req.RegionFilters
	}
	if req.ReverseProxyMissAction != nil {
		if err := setPlatformMissAction(&cfg, *req.ReverseProxyMissAction); err != nil {
			return nil, err
		}
	}
	if req.ReverseProxyEmptyAccountBehavior != nil {
		if err := setPlatformEmptyAccountBehavior(&cfg, *req.ReverseProxyEmptyAccountBehavior); err != nil {
			return nil, err
		}
	}
	if req.ReverseProxyFixedAccountHeader != nil {
		cfg.ReverseProxyFixedAccountHeader = *req.ReverseProxyFixedAccountHeader
	}
	if req.AllocationPolicy != nil {
		if err := setPlatformAllocationPolicy(&cfg, *req.AllocationPolicy); err != nil {
			return nil, err
		}
	}
	if req.PassiveCircuitBreakerDisabled != nil {
		cfg.PassiveCircuitBreakerDisabled = *req.PassiveCircuitBreakerDisabled
	}
	if req.ScheduledRotationInterval != nil {
		d, err := time.ParseDuration(*req.ScheduledRotationInterval)
		if err != nil {
			return nil, invalidArg("scheduled_rotation_interval: " + err.Error())
		}
		if d < 0 {
			return nil, invalidArg("scheduled_rotation_interval must be non-negative")
		}
		cfg.ScheduledRotationIntervalNs = int64(d)
	}
	if req.ScheduledRotationEnabled != nil {
		cfg.ScheduledRotationEnabled = *req.ScheduledRotationEnabled
	}
	if req.RotationAvoidPreviousIP != nil {
		cfg.RotationAvoidPreviousIP = *req.RotationAvoidPreviousIP
	}
	if req.QualityPolicy != nil {
		cfg.QualityPolicy = *req.QualityPolicy
	}
	if err := validatePlatformConfig(&cfg, true); err != nil {
		return nil, err
	}

	id := uuid.New().String()
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	// Register in topology pool.
	// Build the routable view before publish so concurrent readers don't observe
	// a newly created platform with an empty view.
	s.Pool.RebuildPlatform(plat)
	s.Pool.RegisterPlatform(plat)

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// UpdatePlatform applies a constrained partial patch to a platform.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
func (s *ControlPlaneService) UpdatePlatform(id string, patchJSON json.RawMessage) (*PlatformResponse, error) {
	patch, verr := parseMergePatch(patchJSON)
	if verr != nil {
		return nil, verr
	}
	if err := patch.validateFields(platformPatchAllowedFields, func(key string) string {
		return fmt.Sprintf("field %q is read-only or unknown", key)
	}); err != nil {
		return nil, err
	}

	// Load current.
	current, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	if current.ID == platform.DefaultPlatformID {
		if nameVal, ok := patch["name"]; ok {
			nameStr, _ := nameVal.(string)
			if nameStr != platform.DefaultPlatformName {
				return nil, conflict("cannot rename Default platform")
			}
		}
	}

	cfg := platformConfigFromModel(*current)

	// Apply patch to current config.
	if nameStr, ok, err := patch.optionalNonEmptyString("name"); err != nil {
		return nil, err
	} else if ok {
		cfg.Name = platform.NormalizePlatformName(nameStr)
		if err := platform.ValidatePlatformName(cfg.Name); err != nil {
			return nil, invalidArg("name: " + err.Error())
		}
		if cfg.Name == platform.DefaultPlatformName && current.ID != platform.DefaultPlatformID {
			return nil, conflict("cannot use reserved name 'Default'")
		}
	}

	if d, ok, err := patch.optionalDurationString("sticky_ttl"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformStickyTTL(&cfg, d); err != nil {
			return nil, err
		}
	}

	if filters, ok, err := patch.optionalStringSlice("regex_filters"); err != nil {
		return nil, err
	} else if ok {
		cfg.RegexFilters = filters
	}

	regionFiltersPatched := false
	if filters, ok, err := patch.optionalStringSlice("region_filters"); err != nil {
		return nil, err
	} else if ok {
		regionFiltersPatched = true
		cfg.RegionFilters = filters
	}

	if ma, ok, err := patch.optionalString("reverse_proxy_miss_action"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformMissAction(&cfg, ma); err != nil {
			return nil, err
		}
	}
	if behavior, ok, err := patch.optionalString("reverse_proxy_empty_account_behavior"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformEmptyAccountBehavior(&cfg, behavior); err != nil {
			return nil, err
		}
	}
	if fixedHeader, ok, err := patch.optionalString("reverse_proxy_fixed_account_header"); err != nil {
		return nil, err
	} else if ok {
		cfg.ReverseProxyFixedAccountHeader = fixedHeader
	}

	if ap, ok, err := patch.optionalString("allocation_policy"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformAllocationPolicy(&cfg, ap); err != nil {
			return nil, err
		}
	}
	if disabled, ok, err := patch.optionalBool("passive_circuit_breaker_disabled"); err != nil {
		return nil, err
	} else if ok {
		cfg.PassiveCircuitBreakerDisabled = disabled
	}
	if interval, ok, err := patch.optionalDurationString("scheduled_rotation_interval"); err != nil {
		return nil, err
	} else if ok {
		if interval < 0 {
			return nil, invalidArg("scheduled_rotation_interval must be non-negative")
		}
		cfg.ScheduledRotationIntervalNs = int64(interval)
	}
	if enabled, ok, err := patch.optionalBool("scheduled_rotation_enabled"); err != nil {
		return nil, err
	} else if ok {
		cfg.ScheduledRotationEnabled = enabled
	}
	if avoid, ok, err := patch.optionalBool("rotation_avoid_previous_ip"); err != nil {
		return nil, err
	} else if ok {
		cfg.RotationAvoidPreviousIP = avoid
	}
	if raw, ok, err := patch.optionalObject("quality_policy"); err != nil {
		return nil, err
	} else if ok {
		var policy model.QualityPolicy
		if err := json.Unmarshal(raw, &policy); err != nil {
			return nil, invalidArg("quality_policy: " + err.Error())
		}
		cfg.QualityPolicy = policy
	}
	if err := validatePlatformConfig(&cfg, regionFiltersPatched); err != nil {
		return nil, err
	}
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	// Replace in topology pool.
	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// DeletePlatform deletes a platform.
func (s *ControlPlaneService) DeletePlatform(id string) error {
	if s == nil || s.Engine == nil {
		return internal("delete platform", fmt.Errorf("service not initialized"))
	}
	if id == platform.DefaultPlatformID {
		return conflict("cannot delete Default platform")
	}

	if err := s.Engine.DeletePlatform(id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("platform not found")
		}
		return internal("delete platform", err)
	}
	s.Pool.UnregisterPlatform(id)
	return nil
}

// ResetPlatformToDefault resets a platform to env defaults.
func (s *ControlPlaneService) ResetPlatformToDefault(id string) (*PlatformResponse, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("reset platform", fmt.Errorf("service not initialized"))
	}
	name, err := s.Engine.GetPlatformName(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}

	cfg := s.defaultPlatformConfig(name)
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// RebuildPlatformView triggers a full rebuild of the platform's routable view.
func (s *ControlPlaneService) RebuildPlatformView(id string) error {
	plat, ok := s.Pool.GetPlatform(id)
	if !ok {
		return notFound("platform not found")
	}
	s.Pool.RebuildPlatform(plat)
	return nil
}

// PreviewFilterRequest holds preview filter parameters.
type PreviewFilterRequest struct {
	PlatformID   *string             `json:"platform_id"`
	PlatformSpec *PlatformSpecFilter `json:"platform_spec"`
	// QualityPolicy overrides the platform's stored quality policy for this
	// preview only (WP10 §2.1). When it is nil the platform's own policy is
	// used, so the preview counts match the platform's routable view.
	QualityPolicy *model.QualityPolicy `json:"quality_policy,omitempty"`
}

// PlatformSpecFilter is the inline filter spec of a preview request.
type PlatformSpecFilter struct {
	RegexFilters  []string `json:"regex_filters"`
	RegionFilters []string `json:"region_filters"`
}

// NodeSummary is the API response for a node.
type NodeSummary struct {
	Quality                          quality.Summary `json:"quality"`
	Intel                            NodeIntel       `json:"intel"`
	NodeHash                         string          `json:"node_hash"`
	Protocol                         string          `json:"protocol"`
	CreatedAt                        string          `json:"created_at"`
	Enabled                          bool            `json:"enabled"`
	DisplayTag                       string          `json:"display_tag,omitempty"`
	HasOutbound                      bool            `json:"has_outbound"`
	LastError                        string          `json:"last_error,omitempty"`
	CircuitOpenSince                 *string         `json:"circuit_open_since"`
	FailureCount                     int             `json:"failure_count"`
	EgressIP                         string          `json:"egress_ip,omitempty"`
	Region                           string          `json:"region,omitempty"`
	LastEgressUpdate                 string          `json:"last_egress_update,omitempty"`
	LastLatencyProbeAttempt          string          `json:"last_latency_probe_attempt,omitempty"`
	LastAuthorityLatencyProbeAttempt string          `json:"last_authority_latency_probe_attempt,omitempty"`
	ReferenceLatencyMs               *float64        `json:"reference_latency_ms,omitempty"`
	LastEgressUpdateAttempt          string          `json:"last_egress_update_attempt,omitempty"`
	Tags                             []NodeTag       `json:"tags"`
}

// IsHealthyAndEnabled follows the node-summary health rule used by API/UI
// aggregates: enabled, outbound-ready, and not circuit-open.
func (n NodeSummary) IsHealthyAndEnabled() bool {
	return n.Enabled && n.HasOutbound && n.CircuitOpenSince == nil
}

type NodeTag struct {
	SubscriptionID          string `json:"subscription_id"`
	SubscriptionName        string `json:"subscription_name"`
	Tag                     string `json:"tag"`
	SubscriptionCreatedAtNs int64  `json:"-"`
}

func (s *ControlPlaneService) nodeEntryToSummary(h node.Hash, entry *node.NodeEntry) NodeSummary {
	ns := NodeSummary{
		Quality:      quality.Summary{State: "unobserved", Sources: []quality.SourceSummary{}},
		NodeHash:     h.Hex(),
		Protocol:     entry.Protocol,
		CreatedAt:    entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		Enabled:      true,
		HasOutbound:  entry.HasOutbound(),
		LastError:    entry.GetLastError(),
		FailureCount: int(entry.FailureCount.Load()),
	}

	if s != nil && s.Pool != nil {
		ns.Enabled = !s.Pool.IsNodeDisabled(h)
		ns.DisplayTag = s.Pool.ResolveNodeDisplayTag(h)
	}

	if cos := entry.CircuitOpenSince.Load(); cos > 0 {
		t := time.Unix(0, cos).UTC().Format(time.RFC3339Nano)
		ns.CircuitOpenSince = &t
	}

	egressIP := entry.GetEgressIP()
	ns.Quality = s.nodeQuality(egressIP)
	// WP10 §4: the assessment view of the node's egress IP, read from the
	// in-memory projection. The node_egress display facts are added by
	// FillNodeIntelEgress for a whole page at once.
	ns.Intel = s.nodeIntel(entry, time.Now().UTC())
	if egressIP.IsValid() {
		ns.EgressIP = egressIP.String()
		ns.Region = entry.GetRegion(nil)
		if s.GeoIP != nil {
			ns.Region = entry.GetRegion(s.GeoIP.Lookup)
		}
	}

	if leu := entry.LastEgressUpdate.Load(); leu > 0 {
		ns.LastEgressUpdate = time.Unix(0, leu).UTC().Format(time.RFC3339Nano)
	}
	if lastAny := entry.LastLatencyProbeAttempt.Load(); lastAny > 0 {
		ns.LastLatencyProbeAttempt = time.Unix(0, lastAny).UTC().Format(time.RFC3339Nano)
	}
	if lastAuthority := entry.LastAuthorityLatencyProbeAttempt.Load(); lastAuthority > 0 {
		ns.LastAuthorityLatencyProbeAttempt = time.Unix(0, lastAuthority).UTC().Format(time.RFC3339Nano)
	}
	if s != nil && s.RuntimeCfg != nil {
		if cfg := s.RuntimeCfg.Load(); cfg != nil {
			if avgMs, ok := node.AverageEWMAForDomainsMs(entry, cfg.LatencyAuthorities); ok {
				ns.ReferenceLatencyMs = &avgMs
			}
		}
	}
	if lastEgressAttempt := entry.LastEgressUpdateAttempt.Load(); lastEgressAttempt > 0 {
		ns.LastEgressUpdateAttempt = time.Unix(0, lastEgressAttempt).UTC().Format(time.RFC3339Nano)
	}

	// Build tags.
	subIDs := entry.SubscriptionIDs()
	for _, subID := range subIDs {
		sub := s.SubMgr.Lookup(subID)
		if sub == nil {
			continue
		}
		managed, ok := sub.ManagedNodes().LoadNode(h)
		if !ok {
			continue
		}
		tags := managed.Tags
		for _, tag := range tags {
			ns.Tags = append(ns.Tags, NodeTag{
				SubscriptionID:          subID,
				SubscriptionName:        sub.Name(),
				Tag:                     sub.Name() + "/" + tag,
				SubscriptionCreatedAtNs: sub.CreatedAtNs,
			})
		}
	}
	if ns.Tags == nil {
		ns.Tags = []NodeTag{}
	}
	return ns
}

// PreviewFilter returns nodes matching the given filter spec.
func (s *ControlPlaneService) PreviewFilter(req PreviewFilterRequest) ([]NodeSummary, error) {
	report, err := s.PreviewFilterReport(req)
	if err != nil {
		return nil, err
	}
	return report.Nodes, nil
}

// Exclusion counter keys used by PreviewFilterResult.
const (
	PreviewExcludedRegex  = "regex"
	PreviewExcludedRegion = "region"
)

// PreviewFilterResult is the WP10 §2.1 preview outcome: the matching nodes plus
// the per-rule exclusion counters, so a caller can see why a platform's routable
// view is smaller than the node pool.
type PreviewFilterResult struct {
	Nodes      []NodeSummary
	ExcludedBy map[string]int
}

// PreviewFilterReport runs the preview and returns the §2.1 excluded_by
// counters with it.
//
// A node is counted under the first rule that rejects it: "regex", then
// "region", then the first failing quality admission rule, whose counter is the
// admission reason itself (QUALITY_MIN_PURITY, QUALITY_TOR, QUALITY_CHECK:<id>,
// ...). The quality policy is the platform's own policy, unless the request
// carries an explicit quality_policy, which wins (that is what the preview UI
// experiments with). The admission is evaluated against the in-memory intel
// projection, exactly like the routing path, so the counters match what a
// rebuilt platform view would keep.
func (s *ControlPlaneService) PreviewFilterReport(req PreviewFilterRequest) (PreviewFilterResult, error) {
	hasPlatformID := req.PlatformID != nil && *req.PlatformID != ""
	hasPlatformSpec := req.PlatformSpec != nil

	if hasPlatformID == hasPlatformSpec {
		return PreviewFilterResult{}, invalidArg("exactly one of platform_id or platform_spec is required")
	}

	var (
		regexFilters  node.TagFilter
		regionFilters []string
		policy        model.QualityPolicy
	)

	if hasPlatformID {
		plat, ok := s.Pool.GetPlatform(*req.PlatformID)
		if !ok {
			return PreviewFilterResult{}, notFound("platform not found")
		}
		regexFilters = plat.RegexFilters
		regionFilters = plat.RegionFilters
		policy = plat.QualityPolicy
	} else {
		compiled, err := platform.CompileRegexFilters(req.PlatformSpec.RegexFilters)
		if err != nil {
			return PreviewFilterResult{}, invalidArg(err.Error())
		}
		regexFilters = compiled
		regionFilters = req.PlatformSpec.RegionFilters
		if err := platform.ValidateRegionFilters(regionFilters); err != nil {
			return PreviewFilterResult{}, invalidArg(err.Error())
		}
	}
	if req.QualityPolicy != nil {
		policy = *req.QualityPolicy
	}

	var subLookup node.SubLookupFunc
	if s.Pool != nil {
		subLookup = s.Pool.MakeSubLookup()
	}
	var snap platform.QualitySnapshotReader
	if projection := s.intelProjection(); projection != nil {
		snap = projection
	}
	now := time.Now().UTC()

	result := make([]NodeSummary, 0, 32)
	excluded := make(map[string]int, 4)
	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		if !entry.MatchTagFilter(regexFilters, subLookup) {
			excluded[PreviewExcludedRegex]++
			return true
		}
		if len(regionFilters) > 0 {
			region := entry.GetRegion(nil)
			if s.GeoIP != nil {
				region = entry.GetRegion(s.GeoIP.Lookup)
			}
			if !platform.MatchRegionFilter(region, regionFilters) {
				excluded[PreviewExcludedRegion]++
				return true
			}
		}
		if !policy.IsEmpty() {
			if ok, reason := platform.AdmitQuality(policy, entry, snap, now); !ok {
				if reason == "" {
					reason = platform.ReasonQualityUnknown
				}
				excluded[reason]++
				return true
			}
		}
		result = append(result, s.nodeEntryToSummary(h, entry))
		return true
	})
	return PreviewFilterResult{Nodes: result, ExcludedBy: excluded}, nil
}
