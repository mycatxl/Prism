package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

// The mirror URL of an HTTP source is admin-supplied and lives in
// provider_private config, so it round-trips through the settings API. A URL
// that carries credentials in its userinfo ("https://user:pass@host/") would
// therefore be echoed back verbatim: publicConfig only drops keys whose *name*
// looks secret, and "url" is not one of them.
//
// The fix is two-sided. Writing rejects the shape outright, because a password
// belongs in a credential field where it is fingerprinted and redacted. Reading
// strips any userinfo that predates the check, so a deployment that already
// stored one stops leaking it without having to rewrite the row by hand.

func TestValidateProviderConfigRejectsURLUserinfo(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "user and password", url: "https://user:pass@mirror.example.com/check"},
		{name: "user only", url: "https://user@mirror.example.com/check"},
		{name: "percent encoded", url: "https://user%40corp:p%40ss@mirror.example.com/check"},
		{name: "http scheme", url: "http://user:pass@mirror.example.com/check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProviderConfig(Spec{ID: "proxycheck"}, map[string]any{"url": tc.url})
			if err == nil {
				t.Fatalf("config.url %q was accepted; a URL that carries credentials must be refused", tc.url)
			}
		})
	}
}

// The refusal must not become a blanket ban on the field: a plain mirror URL is
// the normal case and has to keep working.
func TestValidateProviderConfigKeepsPlainURLs(t *testing.T) {
	cases := []string{
		"https://mirror.example.com/check",
		"http://127.0.0.1:8080/check", // private is deliberate: an intranet mirror
		"https://mirror.example.com:8443/check?a=1",
	}
	for _, raw := range cases {
		if err := validateProviderConfig(Spec{ID: "proxycheck"}, map[string]any{"url": raw}); err != nil {
			t.Fatalf("config.url %q was refused: %v", raw, err)
		}
	}
}

func TestPublicConfigStripsURLUserinfo(t *testing.T) {
	const secret = "s3cr3t-mirror-password"
	config := map[string]any{
		"url":     "https://mirroruser:" + secret + "@mirror.example.com/check",
		"timeout": "5s",
	}
	spec := Spec{ID: "proxycheck"}

	got := publicConfig(spec, config)

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "mirroruser") {
		t.Fatalf("publicConfig leaked URL userinfo: %s", raw)
	}
	urlValue, _ := got["url"].(string)
	if strings.Contains(urlValue, "@") {
		t.Fatalf("url = %q, want no userinfo", urlValue)
	}
	if !strings.Contains(urlValue, "mirror.example.com") {
		t.Fatalf("url = %q, want the host preserved", urlValue)
	}
	if got["timeout"] != "5s" {
		t.Fatalf("an unrelated key was dropped: %+v", got)
	}

	// The stored config must not be rewritten by rendering it.
	stored, _ := config["url"].(string)
	if !strings.Contains(stored, secret) {
		t.Fatalf("publicConfig mutated the caller's config: %q", stored)
	}
}

// A URL with no userinfo must come back byte-identical: the redaction path
// cannot reformat query strings, ports or paths.
func TestPublicConfigKeepsURLsWithoutUserinfo(t *testing.T) {
	cases := []string{
		"https://mirror.example.com/check",
		"https://mirror.example.com:8443/check?a=1&b=%2F",
		"http://127.0.0.1:8080/",
	}
	for _, raw := range cases {
		got := publicConfig(Spec{ID: "proxycheck"}, map[string]any{"url": raw})
		if got["url"] != raw {
			t.Fatalf("url = %v, want %q unchanged", got["url"], raw)
		}
	}
}
