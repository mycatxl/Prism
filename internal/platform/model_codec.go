package platform

import (
	"fmt"
	"regexp"
	"strings"

	"prism/internal/model"
	"prism/internal/node"
)

func isLowerAlpha2(s string) bool {
	if len(s) != 2 {
		return false
	}
	return s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
}

// ValidateRegionFilters validates region filters against lowercase ISO alpha-2 format.
// Entries may optionally be prefixed with "!" to indicate negation (e.g. !hk).
func ValidateRegionFilters(regionFilters []string) error {
	for i, r := range regionFilters {
		code := r
		if len(r) > 0 && r[0] == '!' {
			code = r[1:]
		}
		if !isLowerAlpha2(code) {
			return fmt.Errorf("region_filters[%d]: must be a 2-letter lowercase ISO 3166-1 alpha-2 code (e.g. us, jp) or negation (e.g. !hk)", i)
		}
	}
	return nil
}

// CompileRegexFilters compiles line-oriented tag regex rules.
// Plain rules are ANY, "*" prefixes MUST, and "!" prefixes MUST_NOT.
// Only the first byte is interpreted; the remaining regex body is left unchanged.
func CompileRegexFilters(regexFilters []string) (node.TagFilter, error) {
	var compiled node.TagFilter
	for i, rule := range regexFilters {
		pattern := rule
		kind := byte(0)
		if len(rule) > 0 && (rule[0] == '*' || rule[0] == '!') {
			kind = rule[0]
			pattern = rule[1:]
		}

		c, err := regexp.Compile(pattern)
		if err != nil {
			return node.TagFilter{}, fmt.Errorf("regex_filters[%d]: invalid regex: %v", i, err)
		}
		switch kind {
		case '*':
			compiled.Must = append(compiled.Must, c)
		case '!':
			compiled.MustNot = append(compiled.MustNot, c)
		default:
			compiled.Any = append(compiled.Any, c)
		}
	}
	return compiled, nil
}

// nodeIPTypes and nodePurityBands mirror the vocabularies the quality model
// emits (internal/intel/snapshot.go bandNames/ipTypeNames), plus the legacy
// "review" band value the node list accepts for the review/conflicting verdicts.
// A criterion value outside them can never match a node, so it is rejected at
// the API boundary instead of silently selecting nothing.
var (
	nodeIPTypes = map[string]bool{
		"unknown": true, "residential": true, "mobile": true, "business": true,
		"wireless": true, "datacenter": true, "non_residential": true, "conflicting": true,
	}
	nodePurityBands = map[string]bool{
		"unknown": true, "excellent": true, "clean": true, "fair": true,
		"mixed": true, "poor": true, "review": true,
	}
)

// ValidateNodeCriteria validates the explicit node-selection criteria lists.
// An empty list is always valid: it means "do not restrict on this criterion".
func ValidateNodeCriteria(ipTypes, purityBands, subscriptionIDs, protocols []string) error {
	for i, value := range ipTypes {
		if !nodeIPTypes[strings.ToLower(strings.TrimSpace(value))] {
			return fmt.Errorf("ip_types[%d]: unsupported network type %q", i, value)
		}
	}
	for i, value := range purityBands {
		if !nodePurityBands[strings.ToLower(strings.TrimSpace(value))] {
			return fmt.Errorf("purity_bands[%d]: unsupported band %q", i, value)
		}
	}
	for i, value := range subscriptionIDs {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("subscription_filters[%d]: must not be empty", i)
		}
	}
	for i, value := range protocols {
		if !isProtocolName(value) {
			return fmt.Errorf("protocols[%d]: %q is not a protocol name", i, value)
		}
	}
	return nil
}

// isProtocolName mirrors the syntax the node list accepts for its protocol
// filter: a lowercase identifier such as "vless" or "openvpn-client"
// (internal/api/handler_node.go:18).
func isProtocolName(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// NewConfiguredPlatform builds a runtime platform with non-filter settings applied.
func NewConfiguredPlatform(
	id, name string,
	regexFilters node.TagFilter,
	regionFilters []string,
	qualityPolicy model.QualityPolicy,
	stickyTTLNs int64,
	missAction string,
	emptyAccountBehavior string,
	fixedAccountHeader string,
	allocationPolicy string,
	passiveCircuitBreakerDisabled bool,
	scheduledRotationEnabled bool,
	scheduledRotationIntervalNs int64,
	rotationAvoidPreviousIP bool,
) *Platform {
	normalizedFixedHeaders, fixedHeaders, err := NormalizeFixedAccountHeaders(fixedAccountHeader)
	if err != nil {
		normalizedFixedHeaders = strings.TrimSpace(fixedAccountHeader)
		fixedHeaders = nil
	}
	plat := NewPlatformWithTagFilter(id, name, regexFilters, regionFilters)
	plat.QualityPolicy = qualityPolicy
	plat.StickyTTLNs = stickyTTLNs
	plat.ReverseProxyMissAction = missAction
	plat.ReverseProxyEmptyAccountBehavior = emptyAccountBehavior
	plat.ReverseProxyFixedAccountHeader = normalizedFixedHeaders
	plat.ReverseProxyFixedAccountHeaders = append([]string(nil), fixedHeaders...)
	plat.AllocationPolicy = ParseAllocationPolicy(allocationPolicy)
	plat.PassiveCircuitBreakerDisabled = passiveCircuitBreakerDisabled
	// WP10 §3: the scheduled rotator reads these fields from the live pool
	// objects, so they must be applied at construction time — dropping them here
	// silently turns the rotator into a no-op.
	plat.ScheduledRotationEnabled = scheduledRotationEnabled
	plat.ScheduledRotationIntervalNs = scheduledRotationIntervalNs
	plat.RotationAvoidPreviousIP = rotationAvoidPreviousIP
	return plat
}

// CompileModelRegexFilters compiles regex filters from persisted model values.
func CompileModelRegexFilters(platformID string, regexFilters []string) (node.TagFilter, error) {
	compiled, err := CompileRegexFilters(regexFilters)
	if err != nil {
		return node.TagFilter{}, fmt.Errorf("decode platform %s regex_filters: %w", platformID, err)
	}
	return compiled, nil
}

// BuildFromModel builds a runtime platform from a persisted model.Platform.
func BuildFromModel(mp model.Platform) (*Platform, error) {
	regexFilters, err := CompileModelRegexFilters(mp.ID, mp.RegexFilters)
	if err != nil {
		return nil, err
	}
	if err := ValidateRegionFilters(mp.RegionFilters); err != nil {
		return nil, err
	}
	emptyAccountBehavior := mp.ReverseProxyEmptyAccountBehavior
	if !ReverseProxyEmptyAccountBehavior(emptyAccountBehavior).IsValid() {
		emptyAccountBehavior = string(ReverseProxyEmptyAccountBehaviorRandom)
	}
	missAction := NormalizeReverseProxyMissAction(mp.ReverseProxyMissAction)
	if missAction == "" {
		return nil, fmt.Errorf(
			"decode platform %s reverse_proxy_miss_action: invalid value %q",
			mp.ID,
			mp.ReverseProxyMissAction,
		)
	}
	fixedHeader, _, err := NormalizeFixedAccountHeaders(mp.ReverseProxyFixedAccountHeader)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s reverse_proxy_fixed_account_header: %w", mp.ID, err)
	}
	if emptyAccountBehavior == string(ReverseProxyEmptyAccountBehaviorFixedHeader) && fixedHeader == "" {
		return nil, fmt.Errorf(
			"decode platform %s reverse_proxy_fixed_account_header: required when reverse_proxy_empty_account_behavior is %s",
			mp.ID,
			ReverseProxyEmptyAccountBehaviorFixedHeader,
		)
	}

	if err := ValidateNodeCriteria(mp.IPTypes, mp.PurityBands, mp.SubscriptionFilters, mp.Protocols); err != nil {
		return nil, fmt.Errorf("decode platform %s node criteria: %w", mp.ID, err)
	}

	plat := NewConfiguredPlatform(
		mp.ID,
		mp.Name,
		regexFilters,
		append([]string(nil), mp.RegionFilters...),
		mp.QualityPolicy,
		mp.StickyTTLNs,
		string(missAction),
		emptyAccountBehavior,
		fixedHeader,
		mp.AllocationPolicy,
		mp.PassiveCircuitBreakerDisabled,
		// WP10 §3: startup loads platforms through this path, so the scheduled
		// rotation and rotation-avoidance fields must survive the round trip.
		mp.ScheduledRotationEnabled,
		mp.ScheduledRotationIntervalNs,
		mp.RotationAvoidPreviousIP,
	)
	// The explicit criteria must be installed before the platform is published:
	// the routable view is built from them.
	plat.SetNodeCriteria(NodeCriteria{
		IPTypes:         mp.IPTypes,
		PurityBands:     mp.PurityBands,
		SubscriptionIDs: mp.SubscriptionFilters,
		Protocols:       mp.Protocols,
	})
	return plat, nil
}
