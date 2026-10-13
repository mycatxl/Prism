package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/keys"
)

// The dispatcher recognizes an access key by its shape, not by comparing it with
// the configured token: the key store lives in the handler, and the mux stays a
// pure dispatch layer. These tests pin the shape rule and the transitional
// behaviour that goes with it.

func TestInboundMuxRoutesAKeyShapedTokenToTokenAction(t *testing.T) {
	credential := testAccessKeyCredential(t)

	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodPost, "/"+credential+"/api/v1/plat/actions/inherit-lease", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Route"); got != "token-action" {
		t.Fatalf("route = %q, want token-action", got)
	}
}

// The reverse path must accept a key-shaped segment too: refusing it here would
// make the reverse parser's key support unreachable.
func TestInboundMuxRoutesAKeyShapedReversePath(t *testing.T) {
	credential := testAccessKeyCredential(t)

	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodGet, "/"+credential+"/plat:acct/https/example.com/path", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Route"); got != "reverse" {
		t.Fatalf("route = %q, want reverse", got)
	}
}

// A segment that is neither the token nor key-shaped is still refused before it
// reaches the reverse proxy, which is the pre-existing behaviour.
func TestInboundMuxStillRejectsAForeignFirstSegment(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodGet, "/not-the-token/plat:acct/https/example.com/path", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Route"); got != "" {
		t.Fatalf("route = %q, want a refusal", got)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// The shape rule itself: a key is a candidate, a token that merely starts with
// the prefix is not, and the configured token is always a candidate.
func TestTokenPositionCandidate(t *testing.T) {
	credential := testAccessKeyCredential(t)

	cases := []struct {
		name         string
		segment      string
		token        string
		wantOK       bool
		whyItMatters string
	}{
		{
			name:    "a key is a candidate",
			segment: credential,
			token:   "tok",
			wantOK:  true,
		},
		{
			name:    "the configured token is a candidate",
			segment: "tok",
			token:   "tok",
			wantOK:  true,
		},
		{
			name:    "a foreign value is not a candidate",
			segment: "not-the-token",
			token:   "tok",
			wantOK:  false,
		},
		{
			// The prefix alone must not open the gate: the shape test also
			// checks the two hex halves, so an ordinary token that happens to
			// start with "pk_" is compared as a token.
			name:    "a token that merely starts with the prefix is not a key",
			segment: "pk_admin-token-value",
			token:   "tok",
			wantOK:  false,
		},
		{
			name:    "with no token configured anything is a candidate",
			segment: "anything",
			token:   "",
			wantOK:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTokenPositionCandidate(tc.segment, tc.token); got != tc.wantOK {
				t.Fatalf("isTokenPositionCandidate(%q, %q) = %v, want %v",
					tc.segment, tc.token, got, tc.wantOK)
			}
		})
	}
}

// testAccessKeyCredential returns a freshly generated credential. The mux never
// verifies it, so it only has to have the right shape.
func testAccessKeyCredential(t *testing.T) string {
	t.Helper()
	id, secret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	return keys.FormatCredential(id, secret)
}
