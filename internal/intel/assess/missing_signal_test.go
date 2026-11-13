package assess

import (
	"prism/internal/quality"
	"testing"
)

func TestIPAPIIdentityOnlyDoesNotFabricatePerfectScore(t *testing.T) {
	e := quality.Evidence{IP: "203.0.113.1", ASN: "AS123", Organization: "Example", CountryCode: "US"}
	if _, _, ok := componentScore(SourceIPAPIIs, e); ok {
		t.Fatal("identity-only response produced a score")
	}
	e.Signals.Proxy = bp(false)
	if _, _, ok := componentScore(SourceIPAPIIs, e); ok {
		t.Fatal("partial negatives fabricated a clean score")
	}
	e.Signals.VPN = bp(false)
	e.Signals.Tor = bp(false)
	e.Signals.Compromised = bp(false)
	if _, score, ok := componentScore(SourceIPAPIIs, e); ok || score != 0 {
		t.Fatal("auxiliary negatives fabricated primary purity")
	}
}
