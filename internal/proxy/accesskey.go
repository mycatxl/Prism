package proxy

import (
	"crypto/sha256"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"prism/internal/auth"
	"prism/internal/keys"
	"prism/internal/model"
)

// unknownAccessKeyHash is compared for every key-shaped credential whose ID is
// absent. It gives a miss the same HMAC and constant-time comparison work as a
// wrong secret for a stored key, so the public ID cannot be enumerated by
// timing. No real HMAC is expected to equal an all-zero digest.
var unknownAccessKeyHash = make([]byte, sha256.Size)

// AccessKeyVerifier resolves a presented credential to the access key it names.
//
// It answers nil for every kind of failure - unknown key, wrong secret, wrong
// scope, disabled, expired, or a revoked owner - without saying which. Callers
// turn all of them into the same authentication failure, so a client cannot use
// the response to learn whether a key id exists.
//
// A credential that is not shaped like an access key also answers nil, and it
// does so without touching the pepper. Callers use the shape result to reject
// non-key credentials before any proxy work starts.
type AccessKeyVerifier interface {
	// VerifyCredential returns the key the credential names, or nil.
	//
	// clientAddr is where the request came from; the key's own IP allowlist is
	// checked against it, and the zero Addr satisfies no allowlist.
	VerifyCredential(credential string, clientAddr netip.Addr) *keys.KeyState
}

// keyVerifier is the production verifier: the in-memory key store plus the
// pepper the secrets were hashed with.
type keyVerifier struct {
	store  *keys.Store
	pepper *auth.Pepper
	// scope is the only credential class this verifier resolves. The data
	// plane and the management API share the same key store, but they must not
	// share a verifier that accepts both scopes: doing so would let an
	// admin-scope key authenticate a proxy connection (or vice versa).
	scope string
	// now is the clock the expiry checks read, in Unix nanoseconds. It is a
	// field so tests can pin it.
	now func() int64
}

// NewAccessKeyVerifier returns a verifier over store and pepper.
//
// It never returns nil, not even when a dependency is missing. An unconfigured
// deployment then fails every key check, so proxy authentication remains closed
// until the key store and pepper are available.
func NewAccessKeyVerifier(store *keys.Store, pepper *auth.Pepper) AccessKeyVerifier {
	return NewAccessKeyVerifierForScope(store, pepper, model.KeyScopeProxy)
}

// NewAccessKeyVerifierForScope returns a verifier restricted to one key scope.
//
// The default constructor is intentionally proxy-only because its callers are
// data-plane entrypoints. Management authentication uses the same verifier
// implementation with the admin scope explicitly selected.
func NewAccessKeyVerifierForScope(store *keys.Store, pepper *auth.Pepper, scope string) AccessKeyVerifier {
	return &keyVerifier{
		store:  store,
		pepper: pepper,
		scope:  scope,
		now:    func() int64 { return time.Now().UnixNano() },
	}
}

// VerifyCredential parses credential and checks it against the store.
//
// The secret is verified before the key's own state is examined, so a disabled
// or expired key costs an attacker the same work as a live one.
func (v *keyVerifier) VerifyCredential(credential string, clientAddr netip.Addr) *keys.KeyState {
	if v == nil || v.store == nil || v.pepper == nil {
		return nil
	}

	keyID, secret, ok := keys.ParseCredential(credential)
	if !ok {
		return nil
	}
	state, ok := v.store.Get(keyID)
	storedHash := unknownAccessKeyHash
	if ok && state != nil {
		storedHash = state.SecretHash
	}
	secretOK := v.pepper.VerifySecret(storedHash, secret)
	if !ok || state == nil {
		return nil
	}
	// An admin key must not be usable as a data-plane credential, and a
	// proxy key must not be usable at the console. The scope is selected by the
	// caller when the verifier is constructed, so both entrypoints fail closed
	// while still being able to resolve their own credential class.
	if v.scope == "" || state.Scope != v.scope || !secretOK {
		return nil
	}
	if !state.Usable(v.now()) {
		return nil
	}
	// The allowlist is part of the credential's validity, not a later policy
	// step, so it is checked here and answers the same nil as a bad secret: a
	// caller cannot tell "wrong address" from "wrong credential".
	if !state.AllowsIP(clientAddr) {
		return nil
	}
	return state
}

// splitAccessKeyCredential finds the access key in a presented credential and
// returns the identity part that is left once the key is taken out.
//
// Two shapes carry a key, and both occur in practice:
//
//   - "<identity>:pk_<id>_<secret>" — the key sits in the token position, which
//     is what every existing client already fills in
//   - "pk_<id>_<secret>" — the whole credential is the key, which is what a
//     client sends when it has no identity to declare
//
// isKey separates "no key here" from "a key was named and did not verify",
// which the caller must refuse. Collapsing the two would let a revoked key be
// retried as another credential.
func splitAccessKeyCredential(verifier AccessKeyVerifier, credential string, clientAddr netip.Addr) (identity string, state *keys.KeyState, isKey bool) {
	identity = ""
	candidate := credential
	if idx := strings.LastIndexByte(credential, ':'); idx >= 0 {
		candidate = credential[idx+1:]
		identity = credential[:idx]
	}
	if !keys.IsCredentialShape(candidate) {
		return credential, nil, false
	}
	if verifier == nil {
		return identity, nil, true
	}
	return identity, verifier.VerifyCredential(candidate, clientAddr), true
}

// proxyIdentity is what an inbound request authenticated as.
//
// Key is populated for every authenticated proxy request. Unauthenticated
// requests never reach routing, metering, or quota enforcement.
type proxyIdentity struct {
	PlatformName string
	Account      string
	Key          *keys.KeyState
}

// clientAddr resolves the source address of a proxy request for the per-key
// allowlist check.
//
// It reads the same address the auth-failure limiter buckets on (see
// ProxyClientIP), so a key restricted to a list sees the real client and not
// the reverse proxy in front of it. An address that does not parse stays
// invalid, and an allowlisted key then refuses the request.
func clientAddr(r *http.Request) netip.Addr {
	addr, err := netip.ParseAddr(ProxyClientIP(r))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// connPeerAddr is the client address of a raw TCP connection. SOCKS5 has no
// forwarded-header concept, so the peer is the client: an allowlisted key is
// checked against the address the socket actually came from.
func connPeerAddr(conn net.Conn) netip.Addr {
	if conn == nil || conn.RemoteAddr() == nil {
		return netip.Addr{}
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		host = conn.RemoteAddr().String()
	}
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}
