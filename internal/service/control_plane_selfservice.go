package service

import (
	"sort"
	"strings"

	"prism/internal/auth"
	"prism/internal/model"
)

// The user's own view of their account.
//
// Every method here takes the user id from the authenticated session and never
// from the request body, because that is what makes the surface safe: a user can
// only ever read or change what belongs to them, and no request has a field in
// which to name someone else.

// SelfPlatform is one platform a user may reach, named for display.
type SelfPlatform struct {
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
}

// SelfOverview is the user's landing page: what they bought, what is left of it,
// and what they can connect through.
type SelfOverview struct {
	Subscription *SubscriptionView `json:"subscription"`
	// TrafficBytes is the plan's period allowance, 0 when unlimited.
	TrafficBytes    int64  `json:"traffic_bytes"`
	RemainingBytes  *int64 `json:"remaining_bytes"`
	PeriodUsedBytes int64  `json:"period_used_bytes"`
	MaxConcurrent   int    `json:"max_concurrent"`
	MaxKeys         int    `json:"max_keys"`
	ActiveKeys      int    `json:"active_keys"`
	// LiveConcurrent is how many connections the user is using right now,
	// summed over their keys. It is 0 when no data plane is running, which is
	// the same answer as an idle account and is why the console labels it as
	// live rather than as a quota.
	LiveConcurrent int            `json:"live_concurrent"`
	UsageDaily     []UsageDay     `json:"usage_daily"`
	Platforms      []SelfPlatform `json:"platforms"`
}

// SessionView is one of the user's console sessions.
//
// The identifier is the stored hash of the session id, not the id itself: the
// value that was handed out at login is not in the database, so it cannot be
// listed. It is still what a revocation addresses - the hash names the session
// for its owner, and the owner is the only one who can see this list.
type SessionView struct {
	ID          string `json:"id"`
	CreatedAtNs int64  `json:"created_at_ns"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	IP          string `json:"ip"`
	UserAgent   string `json:"user_agent"`
	// Current marks the session that made the request.
	Current bool `json:"current"`
}

// SelfOverview reads everything the user's landing page shows.
func (s *ControlPlaneService) SelfOverview(userID string, nowNs int64) (SelfOverview, error) {
	if s == nil || s.Engine == nil {
		return SelfOverview{}, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return SelfOverview{}, mapUserLookupError(err)
	}

	sub, err := s.activeSubscriptionFor(userID)
	if err != nil {
		return SelfOverview{}, err
	}

	overview := SelfOverview{UsageDaily: []UsageDay{}, Platforms: []SelfPlatform{}}

	var plan model.Plan
	if sub != nil {
		views, err := s.subscriptionViews([]model.UserSubscription{*sub})
		if err != nil {
			return SelfOverview{}, err
		}
		view := views[0]
		overview.Subscription = &view
		overview.PeriodUsedBytes = sub.PeriodUsedBytes

		plan, err = planFromSnapshotJSON(sub.PlanSnapshotJSON)
		if err != nil {
			return SelfOverview{}, internal("read plan snapshot", err)
		}
		overview.TrafficBytes = plan.TrafficBytes
		overview.RemainingBytes = remainingBytes(plan.TrafficBytes, sub.PeriodUsedBytes)
		overview.MaxConcurrent = plan.MaxConcurrent
		overview.MaxKeys = plan.MaxKeys
	}

	keys, err := s.Engine.ListAccessKeysByUser(userID)
	if err != nil {
		return SelfOverview{}, internal("list access keys", err)
	}
	for _, key := range keys {
		if key.Status != model.KeyStatusActive {
			continue
		}
		overview.ActiveKeys++
		if s.Conns != nil {
			overview.LiveConcurrent += s.Conns.Count(key.ID)
		}
	}

	usage, err := s.userUsageDaily(userID, defaultUsageDays, nowNs)
	if err != nil {
		return SelfOverview{}, err
	}
	overview.UsageDaily = usage

	platforms, err := s.reachablePlatforms(userID, keys, plan)
	if err != nil {
		return SelfOverview{}, err
	}
	overview.Platforms = platforms
	return overview, nil
}

// SelfSubscription returns the user's subscription, or nil when they have none.
func (s *ControlPlaneService) SelfSubscription(userID string) (*SubscriptionView, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	sub, err := s.activeSubscriptionFor(userID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, nil
	}
	views, err := s.subscriptionViews([]model.UserSubscription{*sub})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// SelfUsage returns the user's own traffic over the last days.
func (s *ControlPlaneService) SelfUsage(userID string, days int, nowNs int64) ([]UsageDay, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return nil, mapUserLookupError(err)
	}
	return s.userUsageDaily(userID, clampUsageDays(days), nowNs)
}

// ListUserSessions returns the user's sessions, newest first.
//
// Expired rows are included while they are still in the table, because "signed
// in from a place I do not recognize" is exactly the question this page is
// opened to answer; a session that has since lapsed is still evidence.
func (s *ControlPlaneService) ListUserSessions(userID, currentSessionID string, nowNs int64) ([]SessionView, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	rows, err := s.Engine.ListSessionsByUser(userID)
	if err != nil {
		return nil, internal("list sessions", err)
	}

	currentHash := ""
	if strings.TrimSpace(currentSessionID) != "" {
		currentHash = auth.HashSessionID(currentSessionID)
	}

	result := make([]SessionView, 0, len(rows))
	for _, row := range rows {
		result = append(result, SessionView{
			ID:          row.IDHash,
			CreatedAtNs: row.CreatedAtNs,
			ExpiresAtNs: row.ExpiresAtNs,
			IP:          row.IP,
			UserAgent:   row.UA,
			Current:     currentHash != "" && row.IDHash == currentHash,
		})
	}
	return result, nil
}

// RevokeUserSession ends one of the user's own sessions.
//
// The ownership test is in the delete, so an id learnt elsewhere matches
// nothing. A session that is not there is reported as not found rather than as
// a refusal, because from the caller's point of view both mean "it is gone".
func (s *ControlPlaneService) RevokeUserSession(userID, sessionID string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return invalidArg("session id: required")
	}
	removed, err := s.Engine.DeleteUserSession(userID, sessionID)
	if err != nil {
		return internal("end session", err)
	}
	if removed == 0 {
		return notFound("session not found")
	}
	return nil
}

// ListUserAccessKeys is the self-service alias of ListAccessKeys, named for what
// the user's own key page shows.
func (s *ControlPlaneService) ListUserAccessKeys(userID string) ([]AccessKeyView, error) {
	return s.ListAccessKeys(userID)
}

// reachablePlatforms resolves the platforms a user may connect through.
//
// Three sets meet here: the plan's platform list, the key's own narrowing, and -
// when neither restricts anything - every platform the deployment knows. The
// union of the keys is what the user can actually use, which is what the access
// guide on the other side of this call is for.
func (s *ControlPlaneService) reachablePlatforms(userID string, keys []model.AccessKey, plan model.Plan) ([]SelfPlatform, error) {
	names, err := s.platformNames()
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool)
	for _, id := range plan.Platforms {
		allowed[id] = true
	}
	if len(allowed) == 0 {
		for id := range names {
			allowed[id] = true
		}
	}

	// A key narrower than the plan can only shrink the set, never grow it.
	narrowed := false
	for _, key := range keys {
		if key.Status != model.KeyStatusActive || len(key.Platforms) == 0 {
			continue
		}
		narrowed = true
		for id := range allowed {
			if !containsString(key.Platforms, id) {
				delete(allowed, id)
			}
		}
	}
	_ = narrowed

	result := make([]SelfPlatform, 0, len(allowed))
	for id := range allowed {
		result = append(result, SelfPlatform{PlatformID: id, PlatformName: names[id]})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PlatformName != result[j].PlatformName {
			return result[i].PlatformName < result[j].PlatformName
		}
		return result[i].PlatformID < result[j].PlatformID
	})
	return result, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
