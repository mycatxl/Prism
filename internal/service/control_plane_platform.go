package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/quality"
	"prism/internal/state"
	"prism/internal/subscription"
)

// ------------------------------------------------------------------
// Platform
// ------------------------------------------------------------------

// PlatformResponse is the API response model for a platform.
type PlatformResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	StickyTTL string `json:"sticky_ttl"`
	// Node-selection criteria. They are ANDed with each other and the values
	// inside one criterion are alternatives; an empty list means "no
	// restriction". regex_filters keeps its legacy line-oriented tag semantics.
	RegexFilters                     []string            `json:"regex_filters"`
	RegionFilters                    []string            `json:"region_filters"`
	IPTypes                          []string            `json:"ip_types"`
	PurityBands                      []string            `json:"purity_bands"`
	SubscriptionFilters              []string            `json:"subscription_filters"`
	Protocols                        []string            `json:"protocols"`
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
		IPTypes:                          append([]string(nil), p.IPTypes...),
		PurityBands:                      append([]string(nil), p.PurityBands...),
		SubscriptionFilters:              append([]string(nil), p.SubscriptionFilters...),
		Protocols:                        append([]string(nil), p.Protocols...),
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
	IPTypes                          []string
	PurityBands                      []string
	SubscriptionFilters              []string
	Protocols                        []string
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
		IPTypes:                          append([]string(nil), mp.IPTypes...),
		PurityBands:                      append([]string(nil), mp.PurityBands...),
		SubscriptionFilters:              append([]string(nil), mp.SubscriptionFilters...),
		Protocols:                        append([]string(nil), mp.Protocols...),
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
		IPTypes:                          append([]string(nil), cfg.IPTypes...),
		PurityBands:                      append([]string(nil), cfg.PurityBands...),
		SubscriptionFilters:              append([]string(nil), cfg.SubscriptionFilters...),
		Protocols:                        append([]string(nil), cfg.Protocols...),
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
	// WP10 §3: the pool objects built here are what the scheduled rotator reads
	// on every sweep, so the rotation fields must be part of the construction
	// call instead of being patched on afterwards (a missed patch leaves the
	// platform rotating nothing).
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
		cfg.ScheduledRotationEnabled,
		cfg.ScheduledRotationIntervalNs,
		cfg.RotationAvoidPreviousIP,
	)
	// The explicit node-selection criteria must be installed before the platform
	// reaches the pool: the routable view is built from them.
	plat.SetNodeCriteria(platform.NodeCriteria{
		IPTypes:         cfg.IPTypes,
		PurityBands:     cfg.PurityBands,
		SubscriptionIDs: cfg.SubscriptionFilters,
		Protocols:       cfg.Protocols,
	})
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

// validatePlatformConfig checks one platform configuration before it is
// persisted. The explicit node-selection criteria are always validated: a value
// the inventory can never produce would silently select nothing.
func validatePlatformConfig(cfg *platformConfig, validateRegionFilters bool) *ServiceError {
	if validateRegionFilters {
		if err := platform.ValidateRegionFilters(cfg.RegionFilters); err != nil {
			return invalidArg(err.Error())
		}
	}
	if err := platform.ValidateNodeCriteria(cfg.IPTypes, cfg.PurityBands, cfg.SubscriptionFilters, cfg.Protocols); err != nil {
		return invalidArg(err.Error())
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
	IPTypes                          []string             `json:"ip_types"`
	PurityBands                      []string             `json:"purity_bands"`
	SubscriptionFilters              []string             `json:"subscription_filters"`
	Protocols                        []string             `json:"protocols"`
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
	if req.IPTypes != nil {
		cfg.IPTypes = req.IPTypes
	}
	if req.PurityBands != nil {
		cfg.PurityBands = req.PurityBands
	}
	if req.SubscriptionFilters != nil {
		cfg.SubscriptionFilters = req.SubscriptionFilters
	}
	if req.Protocols != nil {
		cfg.Protocols = req.Protocols
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

	if filters, ok, err := patch.optionalStringSlice("ip_types"); err != nil {
		return nil, err
	} else if ok {
		cfg.IPTypes = filters
	}
	if filters, ok, err := patch.optionalStringSlice("purity_bands"); err != nil {
		return nil, err
	} else if ok {
		cfg.PurityBands = filters
	}
	if filters, ok, err := patch.optionalStringSlice("subscription_filters"); err != nil {
		return nil, err
	} else if ok {
		cfg.SubscriptionFilters = filters
	}
	if filters, ok, err := patch.optionalStringSlice("protocols"); err != nil {
		return nil, err
	} else if ok {
		cfg.Protocols = filters
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

// PlatformSpecFilter is the inline filter spec of a preview request. It mirrors
// the criteria fields of CreatePlatformRequest exactly, so the preview of an
// unsaved form and the platform the form creates evaluate the same way: every
// criterion is ANDed and the values inside one criterion are alternatives.
type PlatformSpecFilter struct {
	RegexFilters        []string `json:"regex_filters"`
	RegionFilters       []string `json:"region_filters"`
	IPTypes             []string `json:"ip_types"`
	PurityBands         []string `json:"purity_bands"`
	SubscriptionFilters []string `json:"subscription_filters"`
	Protocols           []string `json:"protocols"`
}

// ProbeErrorView is the read model of NodeEntry.GetProbeFailure: the classified
// reason of the last failed probe. It is deliberately separate from LastError.
type ProbeErrorView struct {
	Class  string `json:"class"`
	Detail string `json:"detail"`
	At     string `json:"at"`
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
	// LastProbeError is the reason this node's last probe failed, or nil when the
	// last probe succeeded. Probe failures never touch LastError: that one means
	// the node could not be built at all and drives the ephemeral cleaner.
	LastProbeError *ProbeErrorView `json:"last_probe_error,omitempty"`
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

	if class, detail, at := entry.GetProbeFailure(); class != node.ProbeErrorNone {
		ns.LastProbeError = &ProbeErrorView{
			Class:  class.String(),
			Detail: detail,
			At:     at.UTC().Format(time.RFC3339Nano),
		}
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

// previewCriteria resolves the node-selection criteria one preview request
// describes. Exactly one of platform_id (the persisted platform) and
// platform_spec (the criteria the form is editing right now) must be set.
func (s *ControlPlaneService) previewCriteria(req PreviewFilterRequest) (platform.NodeCriteria, *ServiceError) {
	hasPlatformID := req.PlatformID != nil && *req.PlatformID != ""
	hasPlatformSpec := req.PlatformSpec != nil

	if hasPlatformID == hasPlatformSpec {
		return platform.NodeCriteria{}, invalidArg("exactly one of platform_id or platform_spec is required")
	}

	var criteria platform.NodeCriteria
	if hasPlatformID {
		plat, ok := s.Pool.GetPlatform(*req.PlatformID)
		if !ok {
			return platform.NodeCriteria{}, notFound("platform not found")
		}
		criteria = plat.Criteria()
	} else {
		compiled, err := platform.CompileRegexFilters(req.PlatformSpec.RegexFilters)
		if err != nil {
			return platform.NodeCriteria{}, invalidArg(err.Error())
		}
		if err := platform.ValidateRegionFilters(req.PlatformSpec.RegionFilters); err != nil {
			return platform.NodeCriteria{}, invalidArg(err.Error())
		}
		if err := platform.ValidateNodeCriteria(
			req.PlatformSpec.IPTypes,
			req.PlatformSpec.PurityBands,
			req.PlatformSpec.SubscriptionFilters,
			req.PlatformSpec.Protocols,
		); err != nil {
			return platform.NodeCriteria{}, invalidArg(err.Error())
		}
		criteria = platform.NodeCriteria{
			TagRules:        compiled,
			Regions:         req.PlatformSpec.RegionFilters,
			IPTypes:         req.PlatformSpec.IPTypes,
			PurityBands:     req.PlatformSpec.PurityBands,
			SubscriptionIDs: req.PlatformSpec.SubscriptionFilters,
			Protocols:       req.PlatformSpec.Protocols,
		}
	}
	if req.QualityPolicy != nil {
		criteria.QualityPolicy = *req.QualityPolicy
	}
	return criteria, nil
}

// previewScanLimit bounds how many pool entries one synchronous preview walks.
// The predicate itself is in-memory only, so the bound keeps the request
// comfortably inside an interactive budget even for a very large pool; a preview
// that stops at the bound reports Truncated so the caller never presents a
// partial count as exact.
const previewScanLimit = 20000

// previewSampleLimit bounds the node sample a preview returns. The count is what
// the UI shows; the sample only exists to prove which nodes match.
const previewSampleLimit = 8

// PreviewFilterReport runs the preview and returns the §2.1 excluded_by
// counters with it.
//
// Every criterion is evaluated by platform.MatchNodeCriteria — the same
// predicate the routable-view rebuild uses — and a node is counted under the
// first criterion that rejects it (the platform.Reason* constants, with a
// quality rejection reported as its own admission reason such as
// QUALITY_MIN_PURITY).
//
// The counters keep the two legacy keys of this endpoint: a rejected tag rule
// stays "regex" and a rejected region stays "region". The runtime health gates
// (disabled, unhealthy, no egress, no latency) are deliberately NOT applied
// here: this endpoint answers "which criteria exclude a node", so a node that is
// merely unhealthy right now must still be reported under its criteria counter.
// The form preview (PreviewPlatformScope) does apply them, because it answers
// "how many nodes would the platform actually load".
//
// The quality policy is the platform's own policy, unless the request carries an
// explicit quality_policy, which wins (that is what the preview UI experiments
// with).
//
// Unlike PreviewPlatformScope this endpoint returns every matching node and is
// therefore unbounded: it exists for the §2.1 explain view, not for the
// interactive form preview.
func (s *ControlPlaneService) PreviewFilterReport(req PreviewFilterRequest) (PreviewFilterResult, error) {
	criteria, svcErr := s.previewCriteria(req)
	if svcErr != nil {
		return PreviewFilterResult{}, svcErr
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
	excluded := make(map[string]int, 8)
	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		ok, reason := platform.MatchNodeCriteriaForPreview(criteria, entry, subLookup, s.geoLookupFunc(), nil, snap, now)
		if !ok {
			excluded[previewExclusionKey(reason)]++
			return true
		}
		result = append(result, s.nodeEntryToSummary(h, entry))
		return true
	})
	return PreviewFilterResult{Nodes: result, ExcludedBy: excluded}, nil
}

// previewExclusionKey maps an admission reason onto the exclusion counter key of
// the §2.1 preview. The tag and region criteria keep the legacy keys this
// endpoint has always used; every other criterion is reported under its own
// reason, so a caller can tell the new criteria apart.
func previewExclusionKey(reason string) string {
	switch reason {
	case platform.ReasonTagFilter:
		return PreviewExcludedRegex
	case platform.ReasonRegionFilter:
		return PreviewExcludedRegion
	default:
		return reason
	}
}

// geoLookupFunc returns the GeoIP region lookup of this build, or nil when the
// service has no GeoIP database.
func (s *ControlPlaneService) geoLookupFunc() platform.GeoLookupFunc {
	if s == nil || s.GeoIP == nil {
		return nil
	}
	return s.GeoIP.Lookup
}

// PreviewScopeSampleNode is one sample entry of a preview: enough to recognise a
// node in the UI without paying for a full NodeSummary.
type PreviewScopeSampleNode struct {
	NodeHash          string   `json:"node_hash"`
	DisplayTag        string   `json:"display_tag,omitempty"`
	Region            string   `json:"region,omitempty"`
	Protocol          string   `json:"protocol,omitempty"`
	IPTYPE            string   `json:"ip_type,omitempty"`
	PurityBand        string   `json:"purity_band,omitempty"`
	SubscriptionNames []string `json:"subscription_names"`
}

// PreviewScopeResult is the live preview of the platform node-selection form:
// how many nodes the criteria would load, and what they look like.
//
// Matched counts the nodes admitted inside the scanned window. Scanned is how
// many pool entries were evaluated and Truncated is set when the scan stopped at
// previewScanLimit, in which case Matched is a lower bound ("at least N") and
// the UI must say so.
type PreviewScopeResult struct {
	Matched    int                      `json:"matched"`
	Scanned    int                      `json:"scanned"`
	Truncated  bool                     `json:"truncated"`
	Sample     []PreviewScopeSampleNode `json:"sample"`
	ExcludedBy map[string]int           `json:"excluded_by"`
}

// PreviewPlatformScope evaluates one criteria spec against the live pool and
// reports the bounded count plus a small sample.
//
// The count comes from platform.MatchNodeCriteria, the same predicate the
// routable-view rebuild uses, so "匹配 N 个节点" is exactly what the platform
// would load — never a second implementation of the filter.
func (s *ControlPlaneService) PreviewPlatformScope(req PreviewFilterRequest) (PreviewScopeResult, error) {
	if s == nil || s.Pool == nil {
		return PreviewScopeResult{}, internal("preview platform scope", fmt.Errorf("service not initialized"))
	}
	criteria, svcErr := s.previewCriteria(req)
	if svcErr != nil {
		return PreviewScopeResult{}, svcErr
	}

	subLookup := s.Pool.MakeSubLookup()
	var snap platform.QualitySnapshotReader
	if projection := s.intelProjection(); projection != nil {
		snap = projection
	}
	now := time.Now().UTC()
	geoLookup := s.geoLookupFunc()

	out := PreviewScopeResult{
		Sample:     make([]PreviewScopeSampleNode, 0, previewSampleLimit),
		ExcludedBy: make(map[string]int, 8),
	}
	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		if out.Scanned >= previewScanLimit {
			out.Truncated = true
			return false
		}
		out.Scanned++
		if ok, reason := platform.MatchNodeCriteria(criteria, entry, subLookup, geoLookup, nil, snap, now); !ok {
			out.ExcludedBy[reason]++
			return true
		}
		out.Matched++
		if len(out.Sample) < previewSampleLimit {
			out.Sample = append(out.Sample, s.previewSampleNode(h, entry, geoLookup))
		}
		return true
	})
	return out, nil
}

// previewSampleNode renders one preview sample row. It reads only in-memory
// state: the display tag, the egress region and the projected assessment.
func (s *ControlPlaneService) previewSampleNode(h node.Hash, entry *node.NodeEntry, geoLookup platform.GeoLookupFunc) PreviewScopeSampleNode {
	sample := PreviewScopeSampleNode{
		NodeHash:   h.Hex(),
		Protocol:   entry.Protocol,
		Region:     entry.GetRegion(geoLookup),
		IPTYPE:     intel.IPTypeName(intel.IPTypeUnknown),
		PurityBand: intel.BandName(intel.BandUnknown),
	}
	if s.Pool != nil {
		sample.DisplayTag = s.Pool.ResolveNodeDisplayTag(h)
	}
	if lite, ok := assessmentOfEntry(entry, s.intelProjection()); ok {
		sample.IPTYPE = intel.IPTypeName(lite.IPType)
		sample.PurityBand = intel.BandName(lite.Band)
	}
	if s.SubMgr != nil {
		for _, subID := range entry.SubscriptionIDs() {
			if sub := s.SubMgr.Lookup(subID); sub != nil {
				sample.SubscriptionNames = append(sample.SubscriptionNames, sub.Name())
			}
		}
	}
	if sample.SubscriptionNames == nil {
		sample.SubscriptionNames = []string{}
	}
	return sample
}

// assessmentOfEntry resolves the projected assessment of one node's egress IP.
// It is the service-side twin of platform.assessmentOf, which is unexported.
func assessmentOfEntry(entry *node.NodeEntry, snap platform.QualitySnapshotReader) (intel.AssessmentLite, bool) {
	if snap == nil || entry == nil {
		return intel.AssessmentLite{}, false
	}
	ip := entry.GetEgressIP()
	if !ip.IsValid() {
		return intel.AssessmentLite{}, false
	}
	return snap.Assessment(ip)
}

// PlatformFacetSubscription is one selectable subscription of the platform
// node-selection form, with the number of pool nodes that currently reference it.
type PlatformFacetSubscription struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	NodeCount int    `json:"node_count"`
}

// PlatformNodeFacets is the inventory-derived option list of the platform
// node-selection form. Every value here is a value the live pool actually
// carries, so the operator can only pick criteria that can match something.
//
// The option sets are collected from the same bounded walk as the preview
// (previewScanLimit entries); TotalNodes is the pool size, and Scanned reports
// how many of them the option sets were derived from.
type PlatformNodeFacets struct {
	TotalNodes    int                         `json:"total_nodes"`
	Scanned       int                         `json:"scanned"`
	Truncated     bool                        `json:"truncated"`
	Regions       []string                    `json:"regions"`
	IPTypes       []string                    `json:"ip_types"`
	PurityBands   []string                    `json:"purity_bands"`
	Protocols     []string                    `json:"protocols"`
	Subscriptions []PlatformFacetSubscription `json:"subscriptions"`
}

// PlatformNodeFacets reports the option lists the platform form offers.
func (s *ControlPlaneService) PlatformNodeFacets() (PlatformNodeFacets, error) {
	if s == nil || s.Pool == nil {
		return PlatformNodeFacets{}, internal("platform node facets", fmt.Errorf("service not initialized"))
	}
	out := PlatformNodeFacets{
		TotalNodes:  s.Pool.Size(),
		Regions:     []string{},
		IPTypes:     []string{},
		PurityBands: []string{},
		Protocols:   []string{},
	}
	geoLookup := s.geoLookupFunc()
	snap := s.intelProjection()
	regions := map[string]struct{}{}
	ipTypes := map[string]struct{}{}
	bands := map[string]struct{}{}
	protocols := map[string]struct{}{}
	subCounts := map[string]int{}

	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		if out.Scanned >= previewScanLimit {
			out.Truncated = true
			return false
		}
		out.Scanned++
		if region := entry.GetRegion(geoLookup); region != "" {
			regions[region] = struct{}{}
		}
		if protocol := strings.TrimSpace(entry.Protocol); protocol != "" {
			protocols[protocol] = struct{}{}
		}
		if lite, ok := assessmentOfEntry(entry, snap); ok {
			ipTypes[intel.IPTypeName(lite.IPType)] = struct{}{}
			bands[intel.BandName(lite.Band)] = struct{}{}
		}
		for _, subID := range entry.SubscriptionIDs() {
			subCounts[subID]++
		}
		return true
	})

	out.Regions = sortedKeys(regions)
	out.IPTypes = sortedKeys(ipTypes)
	out.PurityBands = sortedKeys(bands)
	out.Protocols = sortedKeys(protocols)

	// Every registered subscription is offered, with the number of pool nodes
	// that reference it: a subscription with node_count 0 is listed honestly
	// instead of being hidden (its nodes may simply be absent right now).
	if s.SubMgr != nil {
		s.SubMgr.Range(func(id string, sub *subscription.Subscription) bool {
			if sub == nil {
				return true
			}
			out.Subscriptions = append(out.Subscriptions, PlatformFacetSubscription{
				ID:        id,
				Name:      sub.Name(),
				Enabled:   sub.Enabled(),
				NodeCount: subCounts[id],
			})
			return true
		})
	}
	sort.Slice(out.Subscriptions, func(i, j int) bool {
		if out.Subscriptions[i].Name == out.Subscriptions[j].Name {
			return out.Subscriptions[i].ID < out.Subscriptions[j].ID
		}
		return out.Subscriptions[i].Name < out.Subscriptions[j].Name
	})
	return out, nil
}

// sortedKeys renders a set as a sorted slice.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
