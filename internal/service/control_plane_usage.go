package service

import (
	"sort"
	"time"

	"prism/internal/model"
)

// Traffic accounting for the console.
//
// Usage is counted in both directions and summed, because that is what the plan
// limits: a quota expressed in bytes that only counted egress would surprise
// every user the first time they uploaded something. The rows live in two tables
// - recent days are still hourly, older ones have been summarized - and every
// read here goes through the repository method that reads both, so nothing is
// counted twice or missed.

// UsageDay is one UTC day of traffic.
type UsageDay struct {
	DayNs        int64 `json:"day_ns"`
	IngressBytes int64 `json:"ingress_bytes"`
	EgressBytes  int64 `json:"egress_bytes"`
	TotalBytes   int64 `json:"total_bytes"`
}

// UsageSummary is the dashboard's top row.
type UsageSummary struct {
	// ActiveUsers counts accounts that are enabled and on an active
	// subscription: the accounts that can actually send traffic.
	ActiveUsers int `json:"active_users"`
	// TotalUsers counts every account, including administrators.
	TotalUsers int `json:"total_users"`
	// TodayBytes is traffic so far today, in both directions.
	TodayBytes int64 `json:"today_bytes"`
	// PeriodBytes is what every active subscription has used in its current
	// period.
	PeriodBytes int64 `json:"period_bytes"`
	// ExpiringSoonUsers counts subscriptions ending within
	// ExpiringWithinDays, which is the number worth acting on.
	ExpiringSoonUsers  int `json:"expiring_soon_users"`
	ExpiringWithinDays int `json:"expiring_within_days"`
}

// UsageUserRow is one account's traffic over a window.
type UsageUserRow struct {
	UserID          string `json:"user_id"`
	Username        string `json:"username"`
	PlanName        string `json:"plan_name"`
	TotalBytes      int64  `json:"total_bytes"`
	IngressBytes    int64  `json:"ingress_bytes"`
	EgressBytes     int64  `json:"egress_bytes"`
	PeriodUsedBytes int64  `json:"period_used_bytes"`
	TrafficBytes    int64  `json:"traffic_bytes"`
	RemainingBytes  *int64 `json:"remaining_bytes"`
	ExpiresAtNs     int64  `json:"expires_at_ns"`
}

// UsagePlatformRow is one platform's traffic over a window.
type UsagePlatformRow struct {
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	TotalBytes   int64  `json:"total_bytes"`
}

// UsagePlanRow is one plan's footprint over a window: how many accounts ride on
// it, what they spent, and how much of the current period they have used.
type UsagePlanRow struct {
	// PlanID and PlanName are empty for the row that collects accounts with
	// traffic but no active subscription: they are real traffic, and dropping
	// them would make the table add up to less than the deployment's total.
	PlanID      string `json:"plan_id"`
	PlanName    string `json:"plan_name"`
	Subscribers int    `json:"subscribers"`
	TotalBytes  int64  `json:"total_bytes"`
	PeriodBytes int64  `json:"period_bytes"`
}

// expiringSoonDays is the horizon behind "expiring soon". A week is the window
// in which a renewal conversation can still happen before access stops.
const expiringSoonDays = 7

// usageRange is the window the user-level view reads by default.
const defaultUserUsageDays = 30

// UsageSummary reports the numbers the dashboard opens with.
func (s *ControlPlaneService) UsageSummary(nowNs int64) (UsageSummary, error) {
	if s == nil || s.Engine == nil {
		return UsageSummary{}, internal("no state engine", nil)
	}
	users, err := s.Engine.ListUsers()
	if err != nil {
		return UsageSummary{}, internal("list users", err)
	}
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return UsageSummary{}, internal("list active subscriptions", err)
	}

	byUser := make(map[string]model.UserSubscription, len(subs))
	summary := UsageSummary{TotalUsers: len(users), ExpiringWithinDays: expiringSoonDays}
	var periodBytes int64
	for _, sub := range subs {
		byUser[sub.UserID] = sub
		periodBytes += sub.PeriodUsedBytes
		if sub.ExpiresAtNs > 0 && sub.ExpiresAtNs <= nowNs+int64(expiringSoonDays)*int64(24*time.Hour) {
			summary.ExpiringSoonUsers++
		}
	}
	for _, user := range users {
		if user.Status != model.UserStatusActive {
			continue
		}
		if _, ok := byUser[user.ID]; ok {
			summary.ActiveUsers++
		}
	}
	summary.PeriodBytes = periodBytes

	fromNs, toNs := usageWindow(1, nowNs)
	rows, err := s.Engine.ListUsageDailyRange(fromNs, toNs)
	if err != nil {
		return UsageSummary{}, internal("list usage", err)
	}
	for _, row := range rows {
		summary.TodayBytes += row.IngressBytes + row.EgressBytes
	}
	return summary, nil
}

// UsageByUser returns per-account traffic in a window, heaviest first.
//
// A window of zero means the last thirty days. The limit caps the list because
// this is a top-consumers view: an operator looks at the head of the
// distribution, and a deployment with thousands of users does not need to send
// thousands of rows to render that.
func (s *ControlPlaneService) UsageByUser(fromNs, toNs int64, limit int, nowNs int64) ([]UsageUserRow, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if fromNs <= 0 && toNs <= 0 {
		fromNs, toNs = usageWindow(defaultUserUsageDays, nowNs)
	}
	if fromNs < 0 || toNs < 0 {
		return nil, invalidArg("from_ns and to_ns: must not be negative")
	}
	if toNs > 0 && fromNs >= toNs {
		return nil, invalidArg("from_ns: must be earlier than to_ns")
	}

	rows, err := s.Engine.ListUsageDailyRange(fromNs, toNs)
	if err != nil {
		return nil, internal("list usage", err)
	}
	users, err := s.userDirectory()
	if err != nil {
		return nil, err
	}
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return nil, internal("list active subscriptions", err)
	}

	type totals struct{ ingress, egress int64 }
	byUser := make(map[string]totals, len(users))
	for _, row := range rows {
		accumulated := byUser[row.UserID]
		accumulated.ingress += row.IngressBytes
		accumulated.egress += row.EgressBytes
		byUser[row.UserID] = accumulated
	}

	views, err := s.subscriptionViews(subs)
	if err != nil {
		return nil, err
	}
	activeByUser := make(map[string]SubscriptionView, len(views))
	for _, view := range views {
		activeByUser[view.UserID] = view
	}

	result := make([]UsageUserRow, 0, len(byUser))
	for userID, accumulated := range byUser {
		entry := UsageUserRow{
			UserID:       userID,
			TotalBytes:   accumulated.ingress + accumulated.egress,
			IngressBytes: accumulated.ingress,
			EgressBytes:  accumulated.egress,
		}
		if user, ok := users[userID]; ok {
			entry.Username = user.Username
		}
		if sub, ok := activeByUser[userID]; ok {
			entry.PlanName = sub.PlanName
			entry.PeriodUsedBytes = sub.PeriodUsedBytes
			entry.TrafficBytes = sub.TrafficBytes
			entry.RemainingBytes = sub.RemainingBytes
			entry.ExpiresAtNs = sub.ExpiresAtNs
		}
		result = append(result, entry)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalBytes != result[j].TotalBytes {
			return result[i].TotalBytes > result[j].TotalBytes
		}
		return result[i].UserID < result[j].UserID
	})
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

// UsageDaily returns the deployment's traffic over the last days, ascending,
// with the days that had none present as zeros.
func (s *ControlPlaneService) UsageDaily(days int, nowNs int64) ([]UsageDay, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	days = clampUsageDays(days)
	fromNs, toNs := usageWindow(days, nowNs)
	rows, err := s.Engine.ListUsageDailyRange(fromNs, toNs)
	if err != nil {
		return nil, internal("list usage", err)
	}
	return foldUsageDays(rows, days, nowNs), nil
}

// UsageByPlatform returns traffic per platform over the last days, heaviest
// first.
func (s *ControlPlaneService) UsageByPlatform(days int, nowNs int64) ([]UsagePlatformRow, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	days = clampUsageDays(days)
	fromNs, toNs := usageWindow(days, nowNs)
	rows, err := s.Engine.ListUsageDailyRange(fromNs, toNs)
	if err != nil {
		return nil, internal("list usage", err)
	}

	names, err := s.platformNames()
	if err != nil {
		return nil, err
	}
	totals := make(map[string]int64)
	for _, row := range rows {
		totals[row.PlatformID] += row.IngressBytes + row.EgressBytes
	}

	result := make([]UsagePlatformRow, 0, len(totals))
	for platformID, total := range totals {
		result = append(result, UsagePlatformRow{
			PlatformID:   platformID,
			PlatformName: names[platformID],
			TotalBytes:   total,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalBytes != result[j].TotalBytes {
			return result[i].TotalBytes > result[j].TotalBytes
		}
		return result[i].PlatformID < result[j].PlatformID
	})
	return result, nil
}

// UsageByPlan returns traffic per plan over the last days, heaviest first.
//
// The plan is a promise about a group of accounts, so the number that matters to
// an operator is not one user's traffic but the footprint of everyone riding on
// it. Accounts with traffic and no active subscription are collected into one
// extra row with an empty plan id, so the table still adds up.
func (s *ControlPlaneService) UsageByPlan(days int, nowNs int64) ([]UsagePlanRow, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	days = clampUsageDays(days)
	fromNs, toNs := usageWindow(days, nowNs)
	rows, err := s.Engine.ListUsageDailyRange(fromNs, toNs)
	if err != nil {
		return nil, internal("list usage", err)
	}
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return nil, internal("list active subscriptions", err)
	}
	views, err := s.subscriptionViews(subs)
	if err != nil {
		return nil, err
	}

	byPlan := make(map[string]*UsagePlanRow)
	bucket := func(planID, planName string) *UsagePlanRow {
		entry, ok := byPlan[planID]
		if !ok {
			entry = &UsagePlanRow{PlanID: planID, PlanName: planName}
			byPlan[planID] = entry
		}
		if entry.PlanName == "" {
			entry.PlanName = planName
		}
		return entry
	}
	planByUser := make(map[string]string, len(views))
	for _, view := range views {
		entry := bucket(view.PlanID, view.PlanName)
		entry.Subscribers++
		entry.PeriodBytes += view.PeriodUsedBytes
		planByUser[view.UserID] = view.PlanID
	}
	for _, row := range rows {
		bucket(planByUser[row.UserID], "").TotalBytes += row.IngressBytes + row.EgressBytes
	}

	result := make([]UsagePlanRow, 0, len(byPlan))
	for _, entry := range byPlan {
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalBytes != result[j].TotalBytes {
			return result[i].TotalBytes > result[j].TotalBytes
		}
		return result[i].PlanID < result[j].PlanID
	})
	return result, nil
}

// platformNames returns every known platform id to its display name.
func (s *ControlPlaneService) platformNames() (map[string]string, error) {
	rows, err := s.Engine.ListPlatforms()
	if err != nil {
		return nil, internal("list platforms", err)
	}
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

// clampUsageDays applies the window bounds every usage read shares.
func clampUsageDays(days int) int {
	if days <= 0 {
		return defaultUsageDays
	}
	if days > maxUsageDays {
		return maxUsageDays
	}
	return days
}
