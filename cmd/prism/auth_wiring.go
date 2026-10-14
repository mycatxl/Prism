package main

import (
	"time"

	"prism/internal/api"
	"prism/internal/keys"
	"prism/internal/model"
)

// adminKeyResolver accepts an access key with the admin scope as an automation
// credential for the management API.
//
// It is the second way into the management API, alongside a console session. It
// exists so a script does not need a browser: an operator mints an admin key,
// and the key can be revoked by deleting it, which a shared token never could.
//
// An admin key is deliberately not a session: it carries no CSRF requirement -
// there is no cookie for another site to ride on - and the middleware records it
// as an unattributed administrator, because a key names an owner but not a
// person.
type adminKeyResolver struct {
	verifier interface {
		VerifyCredential(credential string) *keys.KeyState
	}
}

// VerifyAdminKey resolves an admin-scope credential to its owner.
//
// Every failure answers the same way. A caller must not be able to tell a
// revoked key from one that exists but has the wrong scope: the difference would
// tell them they had found a real key id.
func (r adminKeyResolver) VerifyAdminKey(credential string) (api.AdminKeyIdentity, bool) {
	if r.verifier == nil {
		return api.AdminKeyIdentity{}, false
	}
	state := r.verifier.VerifyCredential(credential)
	if state == nil {
		return api.AdminKeyIdentity{}, false
	}
	// The scope says what the key is for, and the role says what its owner may
	// do. Both are required: an admin-scope key held by a plain user must not
	// reach the administrator surface, and a proxy key held by an administrator
	// must not either.
	if state.Scope != model.KeyScopeAdmin || state.UserRole != model.UserRoleAdmin {
		return api.AdminKeyIdentity{}, false
	}
	if !state.Usable(time.Now().UnixNano()) {
		return api.AdminKeyIdentity{}, false
	}
	return api.AdminKeyIdentity{
		KeyID:  state.KeyID,
		UserID: state.UserID,
		Role:   state.UserRole,
	}, true
}
