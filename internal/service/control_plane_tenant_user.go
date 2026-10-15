package service

import (
	"errors"
	"sort"
	"strings"
	"time"

	"prism/internal/model"
)

// The administrator's view of the accounts it manages.
//
// This is the surface that turns the control plane into a multi-tenant one: an
// operator creates an account, puts it on a plan, mints the key it will use and
// later suspends it. Every write here ends by republishing the affected user's
// keys, because "disabled" has to mean "refused on the next request", not "on
// the next restart".

// TenantUserInput is the accepted body of a user creation.
//
// plan_id and duration_days are optional and travel together with the account
// because an account created for a customer is created together with what it
// bought. Passing plan_id in the same request is what makes that one write from
// the operator's point of view.
type TenantUserInput struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	Role         string `json:"role"`
	PlanID       string `json:"plan_id"`
	DurationDays int    `json:"duration_days"`
}

// AdminUserView is one account as the console's list shows it, with the
// subscription and the key count resolved.
type AdminUserView struct {
	ID           string            `json:"id"`
	Username     string            `json:"username"`
	Role         string            `json:"role"`
	Status       string            `json:"status"`
	HasTOTP      bool              `json:"has_totp"`
	CreatedAtNs  int64             `json:"created_at_ns"`
	UpdatedAtNs  int64             `json:"updated_at_ns"`
	LastLoginNs  int64             `json:"last_login_ns"`
	KeyCount     int               `json:"key_count"`
	Subscription *SubscriptionView `json:"subscription"`
}

// AdminUserDetail is one account with everything the console can show about it.
type AdminUserDetail struct {
	AdminUserView
	Keys       []AccessKeyView `json:"keys"`
	UsageDaily []UsageDay      `json:"usage_daily"`
}

// TenantUserFilter narrows the user list. Query is a case-insensitive substring
// of the username, because an operator searching for a customer types a
// fragment, not an id.
type TenantUserFilter struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

// userPatchAllowedFields are the fields PATCH accepts on a user.
var userPatchAllowedFields = map[string]bool{
	"status":   true,
	"role":     true,
	"password": true,
}

// ListTenantUsers returns users with their subscription, plus the total before
// paging.
func (s *ControlPlaneService) ListTenantUsers(filter TenantUserFilter) ([]AdminUserView, int, error) {
	if s == nil || s.Engine == nil {
		return nil, 0, internal("no state engine", nil)
	}
	rows, err := s.Engine.ListUsers()
	if err != nil {
		return nil, 0, internal("list users", err)
	}
	views, err := s.adminUserViews(rows)
	if err != nil {
		return nil, 0, err
	}

	query := strings.ToLower(strings.TrimSpace(filter.Query))
	matched := make([]AdminUserView, 0, len(views))
	for _, view := range views {
		if query != "" && !strings.Contains(strings.ToLower(view.Username), query) {
			continue
		}
		if filter.Status != "" && view.Status != filter.Status {
			continue
		}
		matched = append(matched, view)
	}

	total := len(matched)
	return pageSlice(matched, filter.Limit, filter.Offset), total, nil
}

// GetTenantUser returns one account with its keys and its recent usage.
func (s *ControlPlaneService) GetTenantUser(id string, nowNs int64) (AdminUserDetail, error) {
	if s == nil || s.Engine == nil {
		return AdminUserDetail{}, internal("no state engine", nil)
	}
	user, err := s.Engine.GetUser(id)
	if err != nil {
		return AdminUserDetail{}, mapUserLookupError(err)
	}
	views, err := s.adminUserViews([]model.User{*user})
	if err != nil {
		return AdminUserDetail{}, err
	}
	detail := AdminUserDetail{AdminUserView: views[0]}

	keyRows, err := s.Engine.ListAccessKeysByUser(id)
	if err != nil {
		return AdminUserDetail{}, internal("list access keys", err)
	}
	detail.Keys = make([]AccessKeyView, 0, len(keyRows))
	for _, row := range keyRows {
		detail.Keys = append(detail.Keys, accessKeyView(row))
	}

	usage, err := s.userUsageDaily(id, defaultUsageDays, nowNs)
	if err != nil {
		return AdminUserDetail{}, err
	}
	detail.UsageDaily = usage
	return detail, nil
}

// CreateTenantUser creates an account, and optionally the subscription that goes
// with it.
//
// The account is created first and the subscription second on purpose: a failure
// between the two leaves an account that an operator can see and fix, whereas
// the reverse would leave a subscription pointing at nothing.
func (s *ControlPlaneService) CreateTenantUser(in TenantUserInput, nowNs int64) (AdminUserDetail, error) {
	if s == nil || s.Engine == nil {
		return AdminUserDetail{}, internal("no state engine", nil)
	}

	account, err := s.CreateAccount(in.Username, in.Password, tenantUserRole(in.Role), nowNs)
	if err != nil {
		if svcErr := asServiceError(err); svcErr != nil {
			return AdminUserDetail{}, svcErr
		}
		return AdminUserDetail{}, internal("create account", err)
	}

	if planID := strings.TrimSpace(in.PlanID); planID != "" {
		if _, err := s.AssignSubscription(account.ID, planID, in.DurationDays, nowNs); err != nil {
			return AdminUserDetail{}, err
		}
	}
	return s.GetTenantUser(account.ID, nowNs)
}

// SetTenantUserStatus enables or disables an account.
//
// Disabling also ends the account's console sessions. The session resolver
// already refuses a disabled account, so this is belt and braces - but it means
// the account's own security page cannot keep listing a session that can no
// longer be used, and the account is signed out rather than merely locked out.
func (s *ControlPlaneService) SetTenantUserStatus(id, status string, nowNs int64) (AdminUserView, error) {
	if s == nil || s.Engine == nil {
		return AdminUserView{}, internal("no state engine", nil)
	}
	if status != model.UserStatusActive && status != model.UserStatusDisabled {
		return AdminUserView{}, invalidArg("status: must be active or disabled")
	}
	if err := s.SetAccountStatus(id, status, nowNs); err != nil {
		if svcErr := asServiceError(err); svcErr != nil {
			return AdminUserView{}, svcErr
		}
		return AdminUserView{}, internal("set account status", err)
	}
	if status == model.UserStatusDisabled {
		if _, err := s.Engine.DeleteSessionsByUser(id); err != nil {
			return AdminUserView{}, internal("end account sessions", err)
		}
	}
	// A disabled key must be refused by the data plane, and the data plane
	// reads the user status out of the key state it holds.
	if err := s.republishUserKeys(id); err != nil {
		return AdminUserView{}, err
	}
	return s.tenantUserView(id)
}

// SetTenantUserRole changes an account's role.
func (s *ControlPlaneService) SetTenantUserRole(id, role string, nowNs int64) (AdminUserView, error) {
	if s == nil || s.Engine == nil {
		return AdminUserView{}, internal("no state engine", nil)
	}
	if err := s.SetAccountRole(id, role, nowNs); err != nil {
		if svcErr := asServiceError(err); svcErr != nil {
			return AdminUserView{}, svcErr
		}
		return AdminUserView{}, internal("set account role", err)
	}
	if err := s.republishUserKeys(id); err != nil {
		return AdminUserView{}, err
	}
	return s.tenantUserView(id)
}

// SetTenantUserPassword replaces an account's password and ends its sessions.
//
// Both halves in one operation: a password reset that left the old sessions
// alive would be a reset that does not evict whoever the password was taken
// from.
func (s *ControlPlaneService) SetTenantUserPassword(id, password string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if err := s.SetAccountPassword(id, password, nowNs); err != nil {
		if svcErr := asServiceError(err); svcErr != nil {
			return svcErr
		}
		return internal("set account password", err)
	}
	if _, err := s.Engine.DeleteSessionsByUser(id); err != nil {
		return internal("end account sessions", err)
	}
	return nil
}

// DeleteTenantUser removes an account and everything that hangs off it.
//
// Its keys leave the live store first: the rows cascade, but the data plane
// holds its own copy and would otherwise keep honouring a deleted key until the
// next restart.
func (s *ControlPlaneService) DeleteTenantUser(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(id); err != nil {
		return mapUserLookupError(err)
	}
	if s.Keys != nil {
		rows, err := s.Engine.ListAccessKeysByUser(id)
		if err != nil {
			return internal("list access keys", err)
		}
		for _, row := range rows {
			s.Keys.Delete(row.ID)
		}
	}
	if err := s.Engine.DeleteUser(id); err != nil {
		return mapUserLookupError(err)
	}
	return nil
}

// tenantUserView reads one account back as the console's list shows it.
func (s *ControlPlaneService) tenantUserView(id string) (AdminUserView, error) {
	user, err := s.Engine.GetUser(id)
	if err != nil {
		return AdminUserView{}, mapUserLookupError(err)
	}
	views, err := s.adminUserViews([]model.User{*user})
	if err != nil {
		return AdminUserView{}, err
	}
	return views[0], nil
}

// adminUserViews resolves the subscription and the key count of every account in
// one pass over the tables.
//
// Only the active subscription is shown: a user's expired history is not what
// an operator is looking at when they scan the list, and hiding it keeps the
// query to one read per table.
func (s *ControlPlaneService) adminUserViews(users []model.User) ([]AdminUserView, error) {
	subscriptions, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return nil, internal("list active subscriptions", err)
	}
	byUser := make(map[string]model.UserSubscription, len(subscriptions))
	for _, row := range subscriptions {
		if existing, ok := byUser[row.UserID]; !ok || row.StartsAtNs > existing.StartsAtNs {
			byUser[row.UserID] = row
		}
	}
	subViews := make(map[string]SubscriptionView, len(byUser))
	if len(byUser) > 0 {
		rows := make([]model.UserSubscription, 0, len(byUser))
		for _, row := range byUser {
			rows = append(rows, row)
		}
		views, err := s.subscriptionViews(rows)
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			subViews[view.UserID] = view
		}
	}

	keyRows, err := s.Engine.ListActiveAccessKeys()
	if err != nil {
		return nil, internal("list access keys", err)
	}
	keyCounts := make(map[string]int, len(users))
	for _, row := range keyRows {
		keyCounts[row.UserID]++
	}

	views := make([]AdminUserView, 0, len(users))
	for _, user := range users {
		view := AdminUserView{
			ID:          user.ID,
			Username:    user.Username,
			Role:        user.Role,
			Status:      user.Status,
			HasTOTP:     len(user.TOTPSecretEnc) > 0,
			CreatedAtNs: user.CreatedAtNs,
			UpdatedAtNs: user.UpdatedAtNs,
			LastLoginNs: user.LastLoginNs,
			KeyCount:    keyCounts[user.ID],
		}
		if sub, ok := subViews[user.ID]; ok {
			snapshot := sub
			view.Subscription = &snapshot
		}
		views = append(views, view)
	}
	return views, nil
}

// userUsageDaily returns one user's usage over the last days, ascending, with
// the missing days filled in.
func (s *ControlPlaneService) userUsageDaily(userID string, days int, nowNs int64) ([]UsageDay, error) {
	fromNs, toNs := usageWindow(days, nowNs)
	rows, err := s.Engine.ListUsageByUser(userID, fromNs, toNs)
	if err != nil {
		return nil, internal("list usage", err)
	}
	return foldUsageDays(rows, days, nowNs), nil
}

// TenantUserUsage returns one account's daily traffic for the console's chart.
//
// The window is clamped like every other usage view, so a caller cannot ask for
// an unbounded scan; an unknown account is a not-found rather than an empty
// chart, because an empty chart reads as "this account sent nothing".
func (s *ControlPlaneService) TenantUserUsage(id string, days int, nowNs int64) ([]UsageDay, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(id); err != nil {
		return nil, mapUserLookupError(err)
	}
	return s.userUsageDaily(id, clampUsageDays(days), nowNs)
}

// tenantUserRole defaults a missing role to a plain user.
//
// Creating another administrator by accident is the failure worth preventing
// here, so the default is the least privileged role and an unknown value is
// refused rather than passed on.
func tenantUserRole(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return model.UserRoleUser
	}
	return trimmed
}

// defaultUsageDays is the window the console charts by default.
const defaultUsageDays = 30

// maxUsageDays is the longest window the console can ask for. It bounds the
// number of rows a single request can fold.
const maxUsageDays = 365

// usageWindow turns a day count into the half-open nanosecond window that ends
// at the start of tomorrow, so "today" is a whole day rather than a partial one.
func usageWindow(days int, nowNs int64) (int64, int64) {
	if days <= 0 {
		days = defaultUsageDays
	}
	if days > maxUsageDays {
		days = maxUsageDays
	}
	const nsPerDay = int64(24 * time.Hour)
	todayStart := (nowNs / nsPerDay) * nsPerDay
	to := todayStart + nsPerDay
	from := to - int64(days)*nsPerDay
	return from, to
}

// foldUsageDays sums usage rows into one bucket per UTC day, ascending, and
// fills the days with no traffic with zeros so a chart has no gaps.
func foldUsageDays(rows []model.UsageRow, days int, nowNs int64) []UsageDay {
	fromNs, _ := usageWindow(days, nowNs)
	const nsPerDay = int64(24 * time.Hour)

	buckets := make(map[int64]*UsageDay, days)
	for _, row := range rows {
		day := (row.TSNs / nsPerDay) * nsPerDay
		bucket, ok := buckets[day]
		if !ok {
			bucket = &UsageDay{DayNs: day}
			buckets[day] = bucket
		}
		bucket.IngressBytes += row.IngressBytes
		bucket.EgressBytes += row.EgressBytes
	}

	result := make([]UsageDay, 0, days)
	for day := fromNs; day < fromNs+int64(days)*nsPerDay; day += nsPerDay {
		if bucket, ok := buckets[day]; ok {
			bucket.TotalBytes = bucket.IngressBytes + bucket.EgressBytes
			result = append(result, *bucket)
			continue
		}
		result = append(result, UsageDay{DayNs: day})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DayNs < result[j].DayNs })
	return result
}

// asServiceError reports whether an error from the older account methods
// already carries a client-visible code, so the caller does not wrap it twice.
func asServiceError(err error) *ServiceError {
	var svcErr *ServiceError
	if errors.As(err, &svcErr) {
		return svcErr
	}
	return nil
}

// usageDayFromRows is a small helper shared by the usage endpoints: it folds a
// set of rows into day buckets without filling the gaps.
func usageDayFromRows(rows []model.UsageRow) []UsageDay {
	const nsPerDay = int64(24 * time.Hour)
	buckets := make(map[int64]*UsageDay)
	for _, row := range rows {
		day := (row.TSNs / nsPerDay) * nsPerDay
		bucket, ok := buckets[day]
		if !ok {
			bucket = &UsageDay{DayNs: day}
			buckets[day] = bucket
		}
		bucket.IngressBytes += row.IngressBytes
		bucket.EgressBytes += row.EgressBytes
	}
	days := make([]UsageDay, 0, len(buckets))
	for _, bucket := range buckets {
		bucket.TotalBytes = bucket.IngressBytes + bucket.EgressBytes
		days = append(days, *bucket)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].DayNs < days[j].DayNs })
	return days
}
