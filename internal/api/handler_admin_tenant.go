package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"prism/internal/service"
)

// The administration surface of the multi-tenant console.
//
// Every route here sits behind the role gate in rolegate.go, so a handler does
// not re-check who is calling: an administrator is the only actor that can
// reach it. The records the request acts on are named by the path, not by the
// session, which is what makes the console able to manage somebody else's
// account at all.
//
// What the role gate cannot tell apart is the two kinds of administrator: a
// person at a console, and a script holding an admin-scope access key. The rule
// that separates them lives in requireSessionActor (handler_auth.go): a key may
// read this surface and it may revoke, but it may not mint a credential. The
// handlers that would do the latter - create an account, set a password, issue
// or widen a key - therefore call it right after resolving the actor, and it is
// the reason each of them can be reached by a session only.

// adminNowNs is the clock a tenant write is stamped with.
//
// The services take the time as an argument rather than reading the clock
// themselves so that a test can pin it, and so that one request that touches
// several records - a plan edit that rewrites the subscriptions on it, say -
// stamps them all with the same instant.
func adminNowNs() int64 { return time.Now().UnixNano() }

// readRawBody hands a body to a service method that applies its own patch
// semantics.
//
// The patch cannot be decoded into a struct first: which fields are legal, and
// what "absent" means for each of them, is the service's decision, and it
// reads the raw object to tell an omitted field from a null one. The body
// limit middleware has already bounded the read.
func readRawBody(r *http.Request) (json.RawMessage, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &requestBodyTooLargeError{Limit: maxErr.Limit}
		}
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(body)), nil
}

// writeTenantPage writes a list the service has already paged.
//
// WritePage cannot be used for these: it pages the slice it is given, and these
// services return the total alongside an already-sliced page, so applying the
// window twice would return an empty page for every offset past the first.
func writeTenantPage[T any](w http.ResponseWriter, items []T, total int, p Pagination) {
	if items == nil {
		// An empty list is an empty array in JSON. A null would make every
		// console table guard against a value it should not have to.
		items = []T{}
	}
	WriteJSON(w, http.StatusOK, PageResponse[T]{
		Items:  items,
		Total:  total,
		Limit:  p.Limit,
		Offset: p.Offset,
	})
}

// writeTenantItems writes a bare items envelope for the lists that are not
// paged: the console reads them whole, and the service already bounded them.
func writeTenantItems[T any](w http.ResponseWriter, items []T) {
	if items == nil {
		items = []T{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- Plans ---

// HandleAdminListPlans implements GET /api/v1/admin/plans.
func HandleAdminListPlans(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		plans, err := cp.ListPlans()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, plans)
	}
}

// HandleAdminCreatePlan implements POST /api/v1/admin/plans.
func HandleAdminCreatePlan(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in service.PlanInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		plan, err := cp.CreatePlan(in, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, plan)
	}
}

// HandleAdminGetPlan implements GET /api/v1/admin/plans/{id}.
func HandleAdminGetPlan(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		plan, err := cp.GetPlan(PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, plan)
	}
}

// HandleAdminUpdatePlan implements PATCH /api/v1/admin/plans/{id}.
//
// The body is a merge patch. apply_to_existing is the one field that is an
// instruction rather than a plan property: without it an edit changes what the
// next subscription buys, and leaves the users already on the plan alone.
func HandleAdminUpdatePlan(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		patch, err := readRawBody(r)
		if err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		plan, err := cp.UpdatePlan(PathParam(r, "id"), patch, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, plan)
	}
}

// HandleAdminDeletePlan implements DELETE /api/v1/admin/plans/{id}.
func HandleAdminDeletePlan(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeletePlan(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Users ---

// HandleAdminListUsers implements GET /api/v1/admin/users.
func HandleAdminListUsers(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, err := ParsePagination(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		views, total, err := cp.ListTenantUsers(service.TenantUserFilter{
			Query:  r.URL.Query().Get("q"),
			Status: r.URL.Query().Get("status"),
			Role:   r.URL.Query().Get("role"),
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

// adminActor resolves the caller of an administrative write, or answers 401.
//
// The role gate already refused an anonymous request, but the handlers that
// follow with requireSessionActor hand it an Actor, and a decision taken on an
// unresolved actor would be a decision about nobody. It is the same resolution
// the self-service surface makes (meActor); it is named for the surface it
// serves because the rule applied to the answer differs.
func adminActor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	return meActor(w, r)
}

// HandleAdminCreateUser implements POST /api/v1/admin/users.
//
// plan_id is optional. Given one, the account is opened with a subscription
// attached in the same call, because that is what an operator is doing when
// they add a customer: the account is useless without access.
//
// A console session is required (requireSessionActor), and this is the handler
// that made the rule necessary: the body may carry a password, and the role it
// may carry is admin. An administrator's access key that could call this could
// open an account it controls, sign into it through /auth/login, and go on using
// the console as that account - a console session is a credential separate from
// the key, so revoking the key would revoke nothing at all.
func HandleAdminCreateUser(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var in service.TenantUserInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		detail, err := cp.CreateTenantUser(in, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, detail)
	}
}

// HandleAdminGetUser implements GET /api/v1/admin/users/{id}.
func HandleAdminGetUser(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		detail, err := cp.GetTenantUser(PathParam(r, "id"), adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}

// adminUserPatch is the accepted body of PATCH /api/v1/admin/users/{id}.
//
// Pointers, so that a field left out is not the same as a field set to "". The
// decoder rejects an unknown name, which is what keeps a typo in the console
// from silently changing nothing.
type adminUserPatch struct {
	Status   *string `json:"status"`
	Role     *string `json:"role"`
	Password *string `json:"password"`
}

// HandleAdminUpdateUser implements PATCH /api/v1/admin/users/{id}.
//
// A status change ends the account's sessions, a role change is refused when it
// would leave no administrator, and a password change ends every session of
// that account: those are the consequences worth stating, and the service is
// where they are enforced. The response is the full detail, so the console
// re-renders from what was actually stored rather than from what it sent.
//
// A console session is required (requireSessionActor), for a sharper reason than
// on the create path: this patch can set an arbitrary account's password and its
// role. An administrator's key that could call this could reset the password of
// the account it is already authenticated as, sign in with it, and keep a session
// that no key revocation reaches - the reset is exactly the "revocation does not
// work" case, because the session it buys is independent of every key on record.
func HandleAdminUpdateUser(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var patch adminUserPatch
		if err := DecodeBody(r, &patch); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		// A patch that names no field is refused by the service, with the same
		// message and before it reads the account, so there is nothing to
		// pre-check here.

		id := PathParam(r, "id")
		nowNs := adminNowNs()
		// One call, so the three fields are one decision: the service validates
		// all of them against the account before writing any, which is what lets
		// a refusal promise that nothing was applied. Done as three calls, a
		// request carrying a status change and a role change could commit the
		// first and be refused for the second.
		if _, err := cp.UpdateTenantUser(id, service.TenantUserPatch{
			Status:   patch.Status,
			Role:     patch.Role,
			Password: patch.Password,
		}, nowNs); err != nil {
			writeServiceError(w, err)
			return
		}

		detail, err := cp.GetTenantUser(id, nowNs)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}

// HandleAdminDeleteUser implements DELETE /api/v1/admin/users/{id}.
//
// Deleting an account takes its access keys out of the running store as well,
// which is why this is a service call and not a row delete.
//
// It deliberately does not require a session (compare HandleAdminCreateUser).
// Closing an account is revocation, and revocation is not an escalation: it takes
// reach away instead of granting any, and an operator who spots a rogue key must
// be able to kill what it made from a script. The same holds for the key-delete
// handlers below.
func HandleAdminDeleteUser(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteTenantUser(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Access keys of one user ---

// HandleAdminListUserKeys implements GET /api/v1/admin/users/{id}/keys.
func HandleAdminListUserKeys(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		keys, err := cp.ListAccessKeys(PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, keys)
	}
}

// HandleAdminCreateUserKey implements POST /api/v1/admin/users/{id}/keys.
//
// The plaintext credential is in this response and in no other: only its hash
// is stored, so the console has to show it once and tell the user to keep it.
// The key is published to the running store before the response is written, so
// a key that was handed out already works.
//
// A console session is required (requireSessionActor). This is the shortest path
// from an automation credential to one that survives its revocation: a key that
// could call this could issue another key - an admin-scope one, to its own owner
// - and that copy would keep working after the first was deleted.
func HandleAdminCreateUserKey(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var in service.AccessKeyInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, credential, err := cp.CreateAccessKey(PathParam(r, "id"), in, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"key":        view,
			"credential": credential,
		})
	}
}

// HandleAdminUpdateUserKey implements
// PATCH /api/v1/admin/users/{id}/keys/{keyID}.
//
// Narrowing a key - its platforms, its allowlist - reaches the running store on
// the spot, so a change means the next request from that key sees it.
//
// A console session is required (requireSessionActor): a patch is a widening
// operation as well as a narrowing one - scopes, platforms and the address
// allowlist are all in the body - so a key could otherwise edit its own limits,
// or another key's, and make itself outlive the revocation that was meant to
// bound it.
func HandleAdminUpdateUserKey(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		patch, err := readRawBody(r)
		if err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, err := cp.UpdateAccessKey(PathParam(r, "id"), PathParam(r, "keyID"), patch, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}

// HandleAdminDeleteUserKey implements
// DELETE /api/v1/admin/users/{id}/keys/{keyID}.
//
// No session is required, on purpose. Deleting a key is revocation, the one
// write an automation credential must be able to make without a browser: the key
// being deleted is often the compromised one, and a holder of a leaked admin key
// that cannot revoke anything is a leak nobody can stop from the automation that
// noticed it. It grants no reach - it removes reach - so it cannot outlive
// anything by making the caller stronger.
func HandleAdminDeleteUserKey(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteAccessKey(PathParam(r, "id"), PathParam(r, "keyID")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
