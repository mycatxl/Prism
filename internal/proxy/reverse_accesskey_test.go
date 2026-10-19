package proxy

import (
	"testing"

	"prism/internal/keys"
)

// The transitional contract on the reverse proxy: the first path segment
// carries either an access key or the global proxy token.
func TestReverseParsePath_AcceptsAnAccessKey(t *testing.T) {
	fx := newKeyFixture(t)
	rp := &ReverseProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	parsed, err := rp.parsePath("/"+fx.credential+"/myplat:acct/https/example.com/path", testClientAddr)
	if err != nil {
		t.Fatalf("an access key was refused: %v", err)
	}
	if parsed.Key == nil {
		t.Fatal("the parsed path does not carry the key")
	}
	if parsed.Key.KeyID != fx.keyID {
		t.Fatalf("resolved key %q, want %q", parsed.Key.KeyID, fx.keyID)
	}
	if parsed.PlatformName != "myplat" || parsed.Account != "acct" {
		t.Fatalf("got plat=%q acct=%q, want plat=myplat acct=acct", parsed.PlatformName, parsed.Account)
	}
}

func TestReverseParsePath_StillAcceptsTheGlobalToken(t *testing.T) {
	fx := newKeyFixture(t)
	rp := &ReverseProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	parsed, err := rp.parsePath("/global-token/myplat:acct/https/example.com/path", testClientAddr)
	if err != nil {
		t.Fatalf("the global token was refused: %v", err)
	}
	if parsed.Key != nil {
		t.Fatal("the global-token path must not carry a key")
	}
	if parsed.PlatformName != "myplat" || parsed.Account != "acct" {
		t.Fatalf("got plat=%q acct=%q", parsed.PlatformName, parsed.Account)
	}
}

// A revoked key must be refused outright rather than retried as a token.
func TestReverseParsePath_RejectsARevokedKeyRatherThanFallingBack(t *testing.T) {
	fx := newKeyFixture(t)

	revokedID, revokedSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	revoked := keys.FormatCredential(revokedID, revokedSecret)

	rp := &ReverseProxy{
		token:      revoked,
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	if _, err := rp.parsePath("/"+revoked+"/plat:acct/https/example.com/path", testClientAddr); err != ErrAuthFailed {
		t.Fatalf("a revoked key produced %v, want ErrAuthFailed", err)
	}
}

func TestReverseParsePath_RejectsBothWhenNeitherMatches(t *testing.T) {
	fx := newKeyFixture(t)
	rp := &ReverseProxy{
		token:      "global-token",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	unknownID, unknownSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	cases := map[string]string{
		"unknown key":  keys.FormatCredential(unknownID, unknownSecret),
		"wrong secret": keys.FormatCredential(fx.keyID, unknownSecret),
		"wrong token":  "not-the-token",
	}
	for name, segment := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := rp.parsePath("/"+segment+"/plat:acct/https/example.com/path", testClientAddr); err != ErrAuthFailed {
				t.Fatalf("got %v, want ErrAuthFailed", err)
			}
		})
	}
}

// With authentication disabled the first segment is ignored, and a key in it is
// still resolved rather than skipped.
func TestReverseParsePath_AcceptsAKeyWhenAuthenticationIsDisabled(t *testing.T) {
	fx := newKeyFixture(t)
	rp := &ReverseProxy{
		token:      "",
		accessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
		events:     NoOpEventEmitter{},
	}

	parsed, err := rp.parsePath("/"+fx.credential+"/plat:acct/https/example.com/path", testClientAddr)
	if err != nil {
		t.Fatalf("a key was refused with authentication disabled: %v", err)
	}
	if parsed.Key == nil {
		t.Fatal("the parsed path does not carry the key")
	}
}

// A key-shaped segment with no verifier configured must be refused rather than
// fall through to the "authentication disabled" path.
func TestReverseParsePath_RejectsAKeyShapedSegmentWithoutAVerifier(t *testing.T) {
	fx := newKeyFixture(t)
	rp := &ReverseProxy{token: "", events: NoOpEventEmitter{}}

	if _, err := rp.parsePath("/"+fx.credential+"/plat:acct/https/example.com/path", testClientAddr); err != ErrAuthFailed {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
}
