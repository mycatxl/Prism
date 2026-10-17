package api

import (
	"net/http"

	"prism/internal/service"
)

// The self-service purchase surface: what a signed-in user may buy and renew
// with their own balance.
//
// Everything here is keyed by the session's actor, exactly like the rest of
// /me. The one id a path carries is a subscription id, and the service checks
// that it belongs to the caller before touching it, so a learnt id matches
// nothing.

// HandleMePlans implements GET /api/v1/me/plans.
//
// Only plans that are for sale are listed: active and priced. A plan with no
// price is granted by an operator, and offering it here would be offering a
// purchase that has to be refused.
func HandleMePlans(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, ok := meActor(w, r); !ok {
			return
		}
		plans, err := cp.SelfPlans()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, plans)
	}
}

// HandleMePurchase implements POST /api/v1/me/purchases.
//
// The charge and the subscription commit together, so a 200 means the customer
// paid and is on the plan. A replay carrying the same request_id answers with
// the first call's result and charged_micro = 0 rather than charging again.
func HandleMePurchase(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		var in service.PurchaseInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, err := cp.SelfPurchase(actor.UserID, in, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, view)
	}
}

// HandleMeAutoRenew implements PATCH /api/v1/me/subscription.
//
// The switch is the customer's own decision and lives on the subscription
// rather than in the plan snapshot, so an operator's apply-to-existing cannot
// silently flip it.
func HandleMeAutoRenew(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		var in struct {
			SubscriptionID string `json:"subscription_id"`
			AutoRenew      *bool  `json:"auto_renew"`
		}
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if in.AutoRenew == nil {
			writeInvalidArgument(w, "auto_renew: required")
			return
		}
		view, err := cp.SetAutoRenew(actor.UserID, in.SubscriptionID, *in.AutoRenew)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"subscription": view})
	}
}

// HandleMeLeases implements GET /api/v1/me/leases.
func HandleMeLeases(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		leases, err := cp.SelfLeases(actor.UserID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, leases)
	}
}

// HandleMeReleaseLease implements
// DELETE /api/v1/me/leases/{platformID}/{account}.
//
// The owner is the session's, so this can only ever remove the caller's own
// lease. A lease that is not there answers 404 - the same answer as one that
// belongs to somebody else, which is what keeps the route from confirming that
// an account name is in use.
func HandleMeReleaseLease(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		platformID := PathParam(r, "platformID")
		account := PathParam(r, "account")
		if err := cp.ReleaseSelfLease(actor.UserID, platformID, account); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
