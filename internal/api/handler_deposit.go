package api

import (
	"net/http"

	"prism/internal/service"
)

// The self-service deposit surface: one fixed address, one amount per intent.
//
// Everything here is keyed by the session's actor. The one id a path carries is
// an intent id, and the service answers 404 for an intent that belongs to
// somebody else - the same answer as one that never existed, so the route cannot
// confirm that an id is in use.

// HandleMeDepositInfo implements GET /api/v1/me/deposit.
//
// It reports whether deposits are available at all. An operator who has not
// configured a receiving address gets enabled: false rather than an error, which
// is what lets the console hide the page instead of showing a broken one.
func HandleMeDepositInfo(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, ok := meActor(w, r); !ok {
			return
		}
		WriteJSON(w, http.StatusOK, cp.SelfDepositInfo())
	}
}

// HandleMeDeposits implements GET /api/v1/me/deposits (list) and
// POST /api/v1/me/deposits (open an intent).
func HandleMeDeposits(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		if r.Method == http.MethodPost {
			var in struct {
				AmountMicro int64 `json:"amount_micro"`
			}
			if err := DecodeBody(r, &in); err != nil {
				writeDecodeBodyError(w, err)
				return
			}
			view, err := cp.SelfDeposit(actor.UserID, in.AmountMicro, adminNowNs())
			if err != nil {
				writeServiceError(w, err)
				return
			}
			WriteJSON(w, http.StatusCreated, view)
			return
		}
		page, err := ParsePagination(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		intents, err := cp.SelfDeposits(actor.UserID, page.Limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, intents)
	}
}

// HandleMeDeposit implements GET /api/v1/me/deposits/{id}.
func HandleMeDeposit(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		view, err := cp.SelfDepositIntent(actor.UserID, PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}
