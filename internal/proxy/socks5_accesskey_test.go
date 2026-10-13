package proxy

import (
	"testing"

	"prism/internal/keys"
)

// socks5AuthOutcome drives one RFC 1929 exchange against a fresh session and
// reports whether the server accepted the credential.
func socks5AuthOutcome(t *testing.T, inbound *Socks5Inbound, username, password string) bool {
	t.Helper()

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}

	writeAll(t, clientConn, socks5UserPassPacket(username, password))
	reply := readExactly(t, reader, 2)
	if reply[0] != socks5UserPassVersion {
		t.Fatalf("auth reply version: got %d, want %d", reply[0], socks5UserPassVersion)
	}

	// The handshake continues to the CONNECT request once authentication
	// succeeds; closing the connection is how these tests end the session.
	_ = clientConn.Close()
	<-done

	return reply[1] == socks5UserPassStatusSuccess
}

// The transitional contract on the SOCKS5 inbound: a key in the password field
// authenticates, and the username still carries the identity.
func TestSocks5Inbound_AcceptsAnAccessKey(t *testing.T) {
	fx := newKeyFixture(t)
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "global-token",
		AccessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
	})

	if !socks5AuthOutcome(t, inbound, "plat.acct", fx.credential) {
		t.Fatal("a valid access key was refused")
	}
}

func TestSocks5Inbound_StillAcceptsTheGlobalToken(t *testing.T) {
	fx := newKeyFixture(t)
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "global-token",
		AccessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
	})

	if !socks5AuthOutcome(t, inbound, "plat.acct", "global-token") {
		t.Fatal("the global token was refused")
	}
}

// A revoked key must not fall back to the token comparison, even when the
// token is set to the revoked credential itself.
func TestSocks5Inbound_RejectsARevokedKeyRatherThanFallingBack(t *testing.T) {
	fx := newKeyFixture(t)

	revokedID, revokedSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	revoked := keys.FormatCredential(revokedID, revokedSecret)

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: revoked,
		AccessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
	})

	if socks5AuthOutcome(t, inbound, "plat.acct", revoked) {
		t.Fatal("a revoked key authenticated")
	}
}

func TestSocks5Inbound_RejectsBothWhenNeitherMatches(t *testing.T) {
	fx := newKeyFixture(t)
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "global-token",
		AccessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
	})

	unknownID, unknownSecret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}

	cases := map[string]string{
		"unknown key":  keys.FormatCredential(unknownID, unknownSecret),
		"wrong secret": keys.FormatCredential(fx.keyID, unknownSecret),
		"wrong token":  "not-the-token",
	}
	for name, credential := range cases {
		t.Run(name, func(t *testing.T) {
			if socks5AuthOutcome(t, inbound, "plat.acct", credential) {
				t.Fatalf("%s authenticated", name)
			}
		})
	}
}

// With authentication disabled the password may be anything, and a key must
// still be resolved rather than ignored.
func TestSocks5Inbound_AcceptsAKeyWhenAuthenticationIsDisabled(t *testing.T) {
	fx := newKeyFixture(t)
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		AccessKeys: NewAccessKeyVerifier(fx.store, fx.pepper),
	})

	if !socks5AuthOutcome(t, inbound, "plat.acct", fx.credential) {
		t.Fatal("a key was refused with authentication disabled")
	}
}

// A key that names no key at all is still refused when a verifier is wired in,
// because the shape says the client meant to present a key.
func TestSocks5Inbound_RejectsAKeyShapedPasswordWithoutAVerifier(t *testing.T) {
	fx := newKeyFixture(t)
	inbound := NewSocks5Inbound(Socks5InboundConfig{})

	if socks5AuthOutcome(t, inbound, "plat.acct", fx.credential) {
		t.Fatal("a key-shaped password was accepted with no verifier configured")
	}
}
