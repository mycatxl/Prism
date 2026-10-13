package proxy

import (
	"net/url"
	"strings"
	"testing"

	"prism/internal/keys"
)

// testLogCredential returns a freshly generated credential. Nothing verifies it
// here: the redaction works on shape alone, which is the point - it has to hold
// for a credential the process has never seen.
func testLogCredential(t *testing.T) string {
	t.Helper()
	id, secret, err := keys.GenerateCredential()
	if err != nil {
		t.Fatalf("keys.GenerateCredential: %v", err)
	}
	return keys.FormatCredential(id, secret)
}

// The reverse proxy carries its credential as the first path segment, so a
// caller that forgets to redact one would write a usable credential into the
// request log. This is the backstop that makes that impossible.
func TestSanitizeLogURLRedactsAKeyInThePath(t *testing.T) {
	credential := testLogCredential(t)

	cases := []struct {
		name string
		raw  string
	}{
		{"first segment", "https://example.com/" + credential + "/plat:acct/https/target/path"},
		{"middle segment", "https://example.com/prefix/" + credential + "/path"},
		{"last segment", "https://example.com/plat:acct/" + credential},
		{"escaped form", "https://example.com/" + url.PathEscape(credential) + "/plat:acct/https/target"},
		{"with a query", "https://example.com/" + credential + "/plat:acct/https/target?key=secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLogURL(tc.raw)
			if strings.Contains(got, credential) {
				t.Fatalf("the credential survived redaction: %q", got)
			}
			// The shape must be gone too, not just this instance: a partial
			// redaction would still leave a recognisable key.
			if strings.Contains(got, keys.CredentialPrefix) {
				t.Fatalf("a key-shaped segment survived redaction: %q", got)
			}
			// The marker is percent-escaped once the URL is serialized, which
			// is what keeps the result a parseable path.
			if !strings.Contains(got, url.PathEscape(redactedLogValue)) {
				t.Fatalf("the redacted marker is missing: %q", got)
			}
		})
	}
}

// An ordinary path must come through untouched: the backstop exists for
// credentials, not for everything that looks unusual.
func TestSanitizeLogURLKeepsOrdinaryPaths(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://example.com/api/v1/nodes", "https://example.com/api/v1/nodes"},
		{"https://example.com/plat:acct/https/target/path", "https://example.com/plat:acct/https/target/path"},
		{"https://example.com/a%2Fb", "https://example.com/a%2Fb"},
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com/"},
		// A segment that merely starts with the prefix is not a credential.
		{"https://example.com/pk_not-a-real-key/path", "https://example.com/pk_not-a-real-key/path"},
	}
	for _, tc := range cases {
		if got := sanitizeLogURL(tc.raw); got != tc.want {
			t.Errorf("sanitizeLogURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// The escaped rendering is what a URL round trip emits, so redacting only the
// decoded path would leave the credential in the log of any request whose path
// needed escaping.
func TestRedactAccessKeySegmentsCoversBothRenderings(t *testing.T) {
	credential := testLogCredential(t)

	u := &url.URL{
		Scheme:   "https",
		Host:     "example.com",
		Path:     "/" + credential + "/plat:acct/a b",
		RawPath:  "/" + credential + "/plat:acct/a%20b",
		RawQuery: "token=secret",
	}
	u.User = nil
	u.RawQuery = ""
	redactAccessKeySegments(u)

	if strings.Contains(u.Path, credential) {
		t.Fatalf("Path still holds the credential: %q", u.Path)
	}
	if strings.Contains(u.RawPath, credential) {
		t.Fatalf("RawPath still holds the credential: %q", u.RawPath)
	}
	// The escaped rendering has to stay a valid path: the marker must be
	// escaped there, or the URL round trip produces something unparseable.
	if _, err := url.Parse(u.String()); err != nil {
		t.Fatalf("the redacted URL does not parse: %v", err)
	}
}

// A segment that cannot be decoded cannot be proven not to be a credential, so
// it is redacted rather than kept.
func TestRedactAccessKeyPathRedactsUndecodableSegments(t *testing.T) {
	got := redactAccessKeyPath("/prefix/%zz/after")
	if strings.Contains(got, "%zz") {
		t.Fatalf("an undecodable segment survived: %q", got)
	}
	if !strings.Contains(got, redactedLogValue) {
		t.Fatalf("the redacted marker is missing: %q", got)
	}
}

func TestRedactAccessKeyPathIsCheapForOrdinaryPaths(t *testing.T) {
	// The sweep is a no-op when no segment carries the prefix, which is the
	// common case on every logged URL.
	path := "/api/v1/nodes/00000000-0000-0000-0000-000000000000"
	if got := redactAccessKeyPath(path); got != path {
		t.Fatalf("an ordinary path was rewritten: %q", got)
	}
}
