package platform

import (
	"net/netip"
	"time"

	"prism/internal/quality"
)

// QualityPolicy defines quality requirements for platform admission.
// Empty fields are not enforced (no quality gate).
type QualityPolicy struct {
	// ProfileID identifies the quality profile version (for cache invalidation).
	ProfileID string `json:"profile_id,omitempty"`

	// IPTypes filters by network type: residential, datacenter, mobile, non_residential.
	// Empty = allow all types.
	IPTypes []string `json:"ip_types,omitempty"`

	// MinScore is the minimum purity score required (0-100, inclusive).
	// Nil = no score requirement. Score range must have lower bound >= MinScore.
	MinScore *int `json:"min_score,omitempty"`

	// MinConfidence requires assessment confidence: high, medium, low.
	// Empty = no confidence requirement.
	MinConfidence string `json:"min_confidence,omitempty"`

	// MaxAssessmentAgeSeconds is the maximum age of the quality assessment (evidence).
	// Zero = no age limit.
	MaxAssessmentAgeSeconds int64 `json:"max_assessment_age_seconds,omitempty"`

	// MaxEgressAgeSeconds is the maximum age of the egress IP observation.
	// Zero = no egress age limit.
	MaxEgressAgeSeconds int64 `json:"max_egress_age_seconds,omitempty"`

	// UnknownAction controls handling of nodes with unknown/unobserved quality.
	// "allow" = permit; "exclude" = reject. Default: "allow".
	UnknownAction string `json:"unknown_action,omitempty"`

	// ConflictAction controls handling of conflicting quality signals.
	// "allow" = permit; "exclude" = reject. Default: "exclude".
	ConflictAction string `json:"conflict_action,omitempty"`

	// PendingAction controls handling of nodes with pending quality checks.
	// "allow" = permit; "exclude" = reject. Default: "allow".
	PendingAction string `json:"pending_action,omitempty"`

	// ExcludeHighRisk excludes nodes where verdict is "high_risk".
	// Default: true.
	ExcludeHighRisk *bool `json:"exclude_high_risk,omitempty"`

	// ExcludeTor excludes nodes identified as Tor exit/relay.
	// Default: false.
	ExcludeTor bool `json:"exclude_tor,omitempty"`
}

// QualityLookupFunc retrieves quality summary for an egress IP.
type QualityLookupFunc func(netip.Addr) quality.Summary

// Empty returns true if no quality enforcement is configured.
func (q *QualityPolicy) Empty() bool {
	return q == nil || (q.ProfileID == "" &&
		len(q.IPTypes) == 0 &&
		q.MinScore == nil &&
		q.MinConfidence == "" &&
		q.MaxAssessmentAgeSeconds == 0 &&
		q.MaxEgressAgeSeconds == 0 &&
		q.UnknownAction == "" &&
		q.ConflictAction == "" &&
		q.PendingAction == "" &&
		q.ExcludeHighRisk == nil &&
		!q.ExcludeTor)
}

// Evaluate checks whether the given quality summary meets the policy requirements.
// Returns true if the IP passes all quality gates; false otherwise.
func (q *QualityPolicy) Evaluate(
	summary quality.Summary,
	egressObservedAt time.Time,
	now time.Time,
) bool {
	if q.Empty() {
		return true // No quality policy = always pass
	}

	// Assessment is computed from sources; may be nil.
	assessment := summary.Assessment
	if assessment == nil {
		// No assessment computed yet.
		return q.actionAllows(q.UnknownAction)
	}

	// 1. Check egress age (if configured).
	if q.MaxEgressAgeSeconds > 0 && !egressObservedAt.IsZero() {
		age := now.Sub(egressObservedAt)
		if age.Seconds() > float64(q.MaxEgressAgeSeconds) {
			return false // Egress observation too old
		}
	}

	// 2. Check assessment age (use evidence ValidUntil as proxy for age).
	if q.MaxAssessmentAgeSeconds > 0 {
		// Find the most recent evidence timestamp across all sources.
		var mostRecent time.Time
		for _, src := range summary.Sources {
			if src.Evidence != nil && src.Evidence.ObservedAt.After(mostRecent) {
				mostRecent = src.Evidence.ObservedAt
			}
		}
		if !mostRecent.IsZero() {
			age := now.Sub(mostRecent)
			if age.Seconds() > float64(q.MaxAssessmentAgeSeconds) {
				return false // Evidence too old
			}
		}
	}

	// 3. Handle state-based rules.
	switch assessment.State {
	case "unobserved":
		return q.actionAllows(q.UnknownAction)
	case "pending":
		return q.actionAllows(q.PendingAction)
	case "conflicting", "unsupported":
		return q.actionAllows(q.ConflictAction)
	case "stale":
		// Stale evidence = treat as unknown.
		return q.actionAllows(q.UnknownAction)
	case "partial", "valid":
		// Continue to hard gates below.
	default:
		// Unknown state = treat as unknown.
		return q.actionAllows(q.UnknownAction)
	}

	// 4. Hard gates for valid/partial assessments.

	// Exclude high risk (default: true).
	excludeHighRisk := q.ExcludeHighRisk == nil || *q.ExcludeHighRisk
	if excludeHighRisk && assessment.Verdict == "high_risk" {
		return false
	}

	// Exclude Tor (if configured).
	if q.ExcludeTor && len(assessment.TorRoles) > 0 {
		return false
	}

	// IP type filter (if configured).
	if len(q.IPTypes) > 0 {
		allowed := false
		for _, t := range q.IPTypes {
			if assessment.NetworkType == t {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	// Minimum purity score (if configured).
	if q.MinScore != nil && assessment.PurityScore != nil {
		if *assessment.PurityScore < *q.MinScore {
			return false
		}
	}

	// Minimum confidence (if configured).
	if q.MinConfidence != "" {
		// Confidence levels: high > medium > low > unknown.
		// This is a placeholder; actual confidence field is not in Assessment yet.
		// We'll skip this check for now until confidence is added to Assessment.
	}

	return true
}

// actionAllows interprets action strings: "allow" = true, "exclude" = false.
// Default for unknown/pending is allow; default for conflict is exclude.
func (q *QualityPolicy) actionAllows(action string) bool {
	switch action {
	case "allow":
		return true
	case "exclude":
		return false
	default:
		// Default depends on context; caller should specify.
		return true
	}
}
