package proxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/keys"
)

// basicAuthRaw encodes a credential that is already in its wire form, so a test
// can send a shape basicAuth cannot express.
func basicAuthRaw(raw string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

// The transitional contract: during P2 the data plane accepts both an access
// key and the global proxy token, and each entry point must agree. These tests
// pin that agreement for the forward proxy; the SOCKS5 and reverse cases follow
// the same table in their own files.

func TestForwardProxy_AcceptsAnAccessKey(t *testing.T) {
	fx := newKeyFixture(t)
	fp := &ForwardProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	// The identity stays where the token path keeps it: before the separator.
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat.acct", fx.credential))

	identity, err := fp.authenticate(req)
	if err != nil {
		t.Fatalf("an access key was refused: %v", err)
	}
	if identity.Key == nil {
		t.Fatal("the identity does not carry the key")
	}
	if identity.PlatformName != "plat" || identity.Account != "acct" {
		t.Fatalf("got plat=%q acct=%q, want plat=plat acct=acct", identity.PlatformName, identity.Account)
	}
}

// A key with no identity in the credential still authenticates: the platform is
// resolved later by name.
func TestForwardProxy_AcceptsAnAccessKeyWithoutAnIdentity(t *testing.T) {
	fx := newKeyFixture(t)
	fp := &ForwardProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("", fx.credential))

	identity, err := fp.authenticate(req)
	if err != nil {
		t.Fatalf("a bare access key was refused: %v", err)
	}
	if identity.Key == nil {
		t.Fatal("the identity does not carry the key")
	}
	if identity.PlatformName != "" || identity.Account != "" {
		t.Fatalf("got plat=%q acct=%q, want both empty", identity.PlatformName, identity.Account)
	}
}

// The regression that matters most for P2: a deployment that has not issued any
// key yet must keep working exactly as before.
func TestForwardProxy_StillAcceptsTheGlobalToken(t *testing.T) {
	fx := newKeyFixture(t)
	fp := &ForwardProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat.acct", "global-token"))

	identity, err := fp.authenticate(req)
	if err != nil {
		t.Fatalf("the global token was refused: %v", err)
	}
	if identity.Key != nil {
		t.Fatal("the global-token path must not carry a key")
	}
	if identity.PlatformName != "plat" || identity.Account != "acct" {
		t.Fatalf("got plat=%q acct=%q", identity.PlatformName, identity.Account)
	}
}

// A key-shaped credential must never be retried as a token. Otherwise a revoked
// key would authenticate on any deployment whose token happened to match, and
// the "revoke a key" operation would silently not revoke.
func TestForwardProxy_RejectsARevokedKeyRatherThanFallingBack(t *testing.T) {
	fx := newKeyFixture(t)

	revokedID, revokedSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	revoked := keys.FormatCredential(revokedID, revokedSecret)

	// The global token is set to the revoked credential itself: the worst case
	// for a fallback, and it must still fail.
	fp := &ForwardProxy{
		token:      revoked,
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat.acct", revoked))

	if _, err := fp.authenticate(req); err != ErrAuthFailed {
		t.Fatalf("a revoked key produced %v, want ErrAuthFailed", err)
	}
}

func TestForwardProxy_RejectsBothWhenNeitherMatches(t *testing.T) {
	fx := newKeyFixture(t)
	fp := &ForwardProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	unknownID, unknownSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	for name, credential := range map[string]string{
		"unknown key":  keys.FormatCredential(unknownID, unknownSecret),
		"wrong secret": keys.FormatCredential(fx.keyID, unknownSecret),
		"wrong token":  "not-the-token",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat.acct", credential))
			if _, err := fp.authenticate(req); err != ErrAuthFailed {
				t.Fatalf("got %v, want ErrAuthFailed", err)
			}
		})
	}
}

// With authentication disabled the credential may omit the identity, and a key
// must still be recognized in either shape.
func TestForwardProxy_AcceptsAKeyWhenAuthenticationIsDisabled(t *testing.T) {
	fx := newKeyFixture(t)
	fp := &ForwardProxy{
		token:      "",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	cases := map[string]struct {
		raw  string
		plat string
		acct string
	}{
		"bare key":          {fx.credential, "", ""},
		"key with identity": {"plat.acct:" + fx.credential, "plat", "acct"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.Header.Set("Proxy-Authorization", basicAuthRaw(tc.raw))
			identity, err := fp.authenticate(req)
			if err != nil {
				t.Fatalf("a key was refused with authentication disabled: %v", err)
			}
			if identity.Key == nil {
				t.Fatal("the identity does not carry the key")
			}
			if identity.PlatformName != tc.plat || identity.Account != tc.acct {
				t.Fatalf("got plat=%q acct=%q, want plat=%q acct=%q",
					identity.PlatformName, identity.Account, tc.plat, tc.acct)
			}
		})
	}
}
