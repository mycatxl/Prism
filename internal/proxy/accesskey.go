package proxy

import (
	"time"

	"prism/internal/auth"
	"prism/internal/keys"
	"prism/internal/model"
)

// AccessKeyVerifier resolves a presented credential to the access key it names.
//
// It answers nil for every kind of failure - unknown key, wrong secret, wrong
// scope, disabled, expired, or a revoked owner - without saying which. Callers
// turn all of them into the same authentication failure, so a client cannot use
// the response to learn whether a key id exists.
//
// A credential that is not shaped like an access key also answers nil, and it
// does so without touching the pepper. That is what lets a deployment still
// running on the transitional global proxy token keep working: the entry points
// try the key path first, and an ordinary token simply falls through to the
// token comparison.
type AccessKeyVerifier interface {
	VerifyCredential(credential string) *keys.KeyState
}

// keyVerifier is the production verifier: the in-memory key store plus the
// pepper the secrets were hashed with.
type keyVerifier struct {
	store  *keys.Store
	pepper *auth.Pepper
	// now is the clock the expiry checks read, in Unix nanoseconds. It is a
	// field so tests can pin it.
	now func() int64
}

// NewAccessKeyVerifier returns a verifier over store and pepper.
//
// It never returns nil, not even when a dependency is missing. An unconfigured
// deployment then fails every key check and falls back to the global proxy
// token, which is exactly the transitional behaviour; that is worth more than
// saving a nil check at each of the entry points.
func NewAccessKeyVerifier(store *keys.Store, pepper *auth.Pepper) AccessKeyVerifier {
	return &keyVerifier{
		store:  store,
		pepper: pepper,
		now:    func() int64 { return time.Now().UnixNano() },
	}
}

// VerifyCredential parses credential and checks it against the store.
//
// The secret is verified before the key's own state is examined, so a disabled
// or expired key costs an attacker the same work as a live one.
func (v *keyVerifier) VerifyCredential(credential string) *keys.KeyState {
	if v == nil || v.store == nil || v.pepper == nil {
		return nil
	}

	keyID, secret, ok := keys.ParseCredential(credential)
	if !ok {
		return nil
	}
	state, ok := v.store.Get(keyID)
	if !ok {
		return nil
	}
	// An admin key must not be usable as a data-plane credential, and a
	// proxy key must not be usable at the console.
	if state.Scope != model.KeyScopeProxy {
		return nil
	}
	if !v.pepper.VerifySecret(state.SecretHash, secret) {
		return nil
	}
	if !state.Usable(v.now()) {
		return nil
	}
	return state
}

// proxyIdentity is what an inbound request authenticated as.
//
// Key is nil when the request authenticated with the transitional global proxy
// token, or when authentication is disabled. A nil Key means no per-key
// restriction applies, which is what keeps the global-token path behaving
// exactly as it did before access keys existed.
type proxyIdentity struct {
	PlatformName string
	Account      string
	Key          *keys.KeyState
}
