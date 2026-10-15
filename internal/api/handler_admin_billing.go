package api

import (
	"fmt"
	"net/http"
	"strconv"

	"prism/internal/service"
)

// The billing side of the administration surface: subscriptions, the invite
// codes that open accounts, and the traffic reports the console draws its
// dashboard from. Like the rest of /api/v1/admin, these routes are behind the
// administrator role gate.

// intQuery reads an optional integer query parameter.
func intQuery(r *http.Request, key string, fallback int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: must be an integer", key)
	}
	return n, nil
}

// int64Query reads an optional 64-bit integer query parameter, which is what a
// nanosecond timestamp is.
func int64Query(r *http.Request, key string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: must be an integer", key)
	}
	return n, nil
}

// --- Subscriptions ---

// HandleAdminListSubscriptions implements GET /api/v1/admin/subscriptions.
func HandleAdminListSubscriptions(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, err := ParsePagination(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		views, total, err := cp.ListTenantSubscriptions(service.TenantSubscriptionFilter{
			UserID: r.URL.Query().Get("user_id"),
			Status: r.URL.Query().Get("status"),
			Limit:  pagination.Limit,
			Offset: pagination.Offset,
		})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantPage(w, views, total, pagination)
	}
}

// adminSubscriptionCreate is the accepted body of
// POST /api/v1/admin/subscriptions.
//
// duration_days is optional: a plan states how long it runs for, and an
// operator only overrides it when the sale was for something else.
type adminSubscriptionCreate struct {
	UserID       string `json:"user_id"`
	PlanID       string `json:"plan_id"`
	DurationDays int    `json:"duration_days"`
}

// HandleAdminCreateSubscription implements POST /api/v1/admin/subscriptions.
//
// Assigning opens the new subscription and expires whatever the user had, so a
// customer never holds two live subscriptions and the keys that were narrowed
// by the old plan are republished against the new one.
func HandleAdminCreateSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in adminSubscriptionCreate
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if in.UserID == "" {
			writeInvalidArgument(w, "user_id: required")
			return
		}
		view, err := cp.AssignSubscription(in.UserID, in.PlanID, in.DurationDays, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, view)
	}
}

// HandleAdminGetSubscription implements GET /api/v1/admin/subscriptions/{id}.
func HandleAdminGetSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		view, err := cp.GetTenantSubscription(PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleAdminUpdateSubscription implements
// PATCH /api/v1/admin/subscriptions/{id}.
//
// status, extend_days and expires_at_ns, and nothing else: the traffic counter
// is a record of what happened, not a field an operator edits.
func HandleAdminUpdateSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		patch, err := readRawBody(r)
		if err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, err := cp.UpdateTenantSubscription(PathParam(r, "id"), patch, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleAdminDeleteSubscription implements
// DELETE /api/v1/admin/subscriptions/{id}.
//
// This removes the record, not the access: a key whose user no longer has a
// subscription is refused by the plan snapshot that the store already holds.
func HandleAdminDeleteSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteTenantSubscription(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Invite codes ---

// HandleAdminListInvites implements GET /api/v1/admin/invites.
func HandleAdminListInvites(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, err := ParsePagination(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		views, total, err := cp.ListInvites(pagination.Limit, pagination.Offset, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantPage(w, views, total, pagination)
	}
}

// HandleAdminCreateInvite implements POST /api/v1/admin/invites.
//
// The code comes back in the response because it is the only time it is
// minted; the row keeps it in the clear, since an operator has to be able to
// read a code out to the person they are letting in.
func HandleAdminCreateInvite(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in service.InviteInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		createdBy := ""
		if actor, ok := ActorFromContext(r.Context()); ok {
			createdBy = actor.Username
		}
		view, err := cp.CreateInvite(in, createdBy, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, view)
	}
}

// HandleAdminDeleteInvite implements DELETE /api/v1/admin/invites/{id}.
func HandleAdminDeleteInvite(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteInvite(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Traffic reports ---

// HandleAdminUsageSummary implements GET /api/v1/admin/usage/summary.
func HandleAdminUsageSummary(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		summary, err := cp.UsageSummary(adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, summary)
	}
}

// HandleAdminUsageUsers implements GET /api/v1/admin/usage/users.
//
// from_ns and to_ns are absolute instants: the console sends the window it is
// drawing, and an absent pair means the service's own default window rather
// than "everything".
func HandleAdminUsageUsers(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fromNs, err := int64Query(r, "from_ns", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		toNs, err := int64Query(r, "to_ns", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		limit, err := intQuery(r, "limit", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		rows, err := cp.UsageByUser(fromNs, toNs, limit, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, rows)
	}
}

// HandleAdminUsageDaily implements GET /api/v1/admin/usage/daily.
func HandleAdminUsageDaily(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		days, err := intQuery(r, "days", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		rows, err := cp.UsageDaily(days, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, rows)
	}
}

// HandleAdminUsagePlatforms implements GET /api/v1/admin/usage/platforms.
func HandleAdminUsagePlatforms(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		days, err := intQuery(r, "days", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		rows, err := cp.UsageByPlatform(days, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, rows)
	}
}

// HandleAdminGetUserUsage implements GET /api/v1/admin/users/{id}/usage.
//
// The user detail page charts one account over a window, which the platform-wide
// views cannot answer: they aggregate, this one does not.
func HandleAdminGetUserUsage(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if cp == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "control plane not configured")
			return
		}
		days, err := intQuery(r, "days", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		rows, svcErr := cp.TenantUserUsage(PathParam(r, "id"), days, adminNowNs())
		if svcErr != nil {
			writeServiceError(w, svcErr)
			return
		}
		writeTenantItems(w, rows)
	}
}
