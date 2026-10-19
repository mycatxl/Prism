package api

import (
	"net/http"
	"strconv"

	"prism/internal/service"
)

// The money surface: a user's own balance and the operator's control over it.
//
// Nothing here takes an amount as a float or a decimal string. Amounts are int64
// micro-units - 1e-6 USDT each - all the way through, and the console divides by
// 1e6 only when it renders. A handler that parsed "10.5" into a float would be
// the one place money could drift.

// HandleMeBalance implements GET /api/v1/me/balance.
//
// The balance is the session actor's, so there is no id to point elsewhere. The
// response carries the recent ledger with it: a number with no history is a
// number a user has to take on faith.
func HandleMeBalance(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		limit, ok := balanceLedgerLimit(w, r)
		if !ok {
			return
		}
		view, err := cp.SelfBalance(actor.UserID, limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleMeRedeem implements POST /api/v1/me/redeem.
//
// The code is spent and the balance credited in one transaction, so a failure
// here never leaves a user holding a code that no longer works. An unknown,
// revoked, expired and spent code all produce the same 400: telling them apart
// would make this endpoint a way to search for codes that work.
func HandleMeRedeem(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, err := cp.Redeem(actor.UserID, in.Code, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleAdminListRedeemCodes implements GET /api/v1/admin/redeem-codes.
//
// Only the hint of each code is returned. The plaintext exists once, in the
// response that minted it, and is not recoverable afterwards.
func HandleAdminListRedeemCodes(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, err := ParsePagination(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		views, total, err := cp.ListRedeemCodes(pagination.Limit, pagination.Offset, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantPage(w, views, total, pagination)
	}
}

// HandleAdminCreateRedeemCodes implements POST /api/v1/admin/redeem-codes.
//
// Codes come back in the response because that is the only time the plaintext
// exists; nothing stores it. An operator minting a batch gets the batch.
//
// A console session is required (requireSessionActor). A redeem code is money in
// a string: it credits a balance the moment any account spends it, so a key that
// could mint one could mint funds for an account it controls, then sign in and
// spend them without ever touching a key again. That is a balance whose origin
// no longer traces back to a credential that can be revoked. Redeeming
// (/me/redeem), listing and deleting codes stay available to a key, so a script
// that settles a batch it was handed is unaffected.
func HandleAdminCreateRedeemCodes(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var in service.RedeemCodeInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		createdBy := actor.Username
		created, err := cp.CreateRedeemCodes(in, createdBy, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{"codes": created})
	}
}

// HandleAdminDeleteRedeemCode implements DELETE /api/v1/admin/redeem-codes/{id}.
func HandleAdminDeleteRedeemCode(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteRedeemCode(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleAdminGetUserBalance implements GET /api/v1/admin/users/{id}/balance.
func HandleAdminGetUserBalance(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		limit, ok := balanceLedgerLimit(w, r)
		if !ok {
			return
		}
		view, err := cp.AdminBalance(PathParam(r, "id"), limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleAdminAdjustUserBalance implements POST /api/v1/admin/users/{id}/balance.
//
// This is the only movement with no external record behind it, so the note is
// required and the acting operator is recorded with the row. A debit that would
// take the balance negative is refused, not applied.
//
// A console session is required (requireSessionActor). A credit mints money in
// the same sense a redeem code does - the row this writes is the balance - and
// the account holding it can spend it on a plan with no code, card or payment
// behind it. A key that could credit could therefore fund an account it
// controls, and the funds would still be there after the key was deleted: a
// balance is not a credential revocation reaches. Reading a balance and its
// ledger stays open to a key (the GET next to this one), which is what a
// reconciliation script actually needs, and so does redeeming a code.
func HandleAdminAdjustUserBalance(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var in struct {
			DeltaMicro int64  `json:"delta_micro"`
			Note       string `json:"note"`
		}
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, err := cp.AdjustBalance(PathParam(r, "id"), in.DeltaMicro, in.Note, actor.Username, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// balanceLedgerLimit reads the optional limit query parameter, answering the
// client and returning false when it is not a number.
func balanceLedgerLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeInvalidArgument(w, "limit: must be a non-negative integer")
		return 0, false
	}
	return limit, true
}
