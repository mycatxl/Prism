package assess

import (
	"net/netip"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestMissingAndInvalidEvidenceCannotBecomeFavorable(t *testing.T) {
	for mask := 0; mask < 15; mask++ {
		e := ev(SourceIPAPIIs).e
		fields := []**bool{&e.Signals.Proxy, &e.Signals.VPN, &e.Signals.Tor, &e.Signals.Compromised}
		for i, field := range fields {
			if mask&(1<<i) != 0 {
				*field = bp(false)
			}
		}
		if _, _, ok := componentScore(SourceIPAPIIs, e); ok {
			t.Fatalf("partial vector %04b scored", mask)
		}
	}
	for _, bad := range []int{-999, -1, 101, 256} {
		for _, source := range []string{SourceProxycheck, SourceIPPure, SourceIPQS, SourceAbuseIPDB} {
			e := ev(source).risk(bad).fraud(bad).abuse(bad, 1).e
			if _, _, ok := componentScore(source, e); ok {
				t.Fatalf("invalid %s=%d scored", source, bad)
			}
		}
	}
	a := Assess(netip.MustParseAddr(testIPv4), evidence(ev(SourceIPPure).fraud(0)), testNow, nil)
	if a.PurityScore == nil || *a.PurityScore != 100 || a.Verdict != VerdictIncomplete {
		t.Fatalf("real score retained but gaps not favorable: %+v", a)
	}
}

func TestUntimedEvidenceCannotProduceCurrentPurity(t *testing.T) {
	for _, mode := range []string{"missing_expiry", "missing_observation", "future_observation"} {
		e := ev(SourceIPPure).fraud(0).e
		switch mode {
		case "missing_expiry":
			e.ValidUntil = time.Time{}
		case "missing_observation":
			e.ObservedAt = time.Time{}
		case "future_observation":
			e.ObservedAt = testNow.Add(time.Minute)
		}
		if a := Assess(netip.MustParseAddr(testIPv4), []quality.Evidence{e}, testNow, nil); a.PurityScore != nil {
			t.Errorf("%s manufactured a current score", mode)
		}
	}
}

func TestNetworkConflictResolvesWithFreshIndependentEvidence(t *testing.T) {
	input := Input{IP: netip.MustParseAddr(testIPv4), Now: testNow, Evidence: evidence(
		ev(SourceProxycheck).risk(10).typeOf("residential"),
		ev(SourceProxycheckNode).risk(10).typeOf("residential"),
		ev(SourceIPAPI).typeOf("datacenter"),
	)}
	if a := AssessInput(input); a.IPType != IPTypeConflicting {
		t.Fatalf("same vendor aliases fabricated majority: %s", a.IPType)
	}
	// The corrected newer vendor record replaces its old vote. Nothing is
	// deleted and no administrative type override is required to clear conflict.
	input.Evidence = append(input.Evidence, ev(SourceIPAPI).typeOf("residential").observedAt(testNow.Add(time.Minute)).e)
	input.Now = testNow.Add(2 * time.Minute)
	if a := AssessInput(input); a.IPType != IPTypeResidential {
		t.Fatalf("conflict is terminal: %s", a.IPType)
	}
	input.Evidence = []quality.Evidence{ev(SourceProxycheck).typeOf("conflicting").e, ev(SourceIPPure).residential(true).e}
	if a := AssessInput(input); a.IPType != IPTypeConflicting {
		t.Fatal("provider internal conflict vanished")
	}
	input.Evidence[0] = ev(SourceProxycheck).typeOf("residential").e
	if a := AssessInput(input); a.IPType != IPTypeResidential {
		t.Fatal("corrected provider conflict did not clear")
	}
	input.Now = testNow
	input.Evidence = evidence(ev(SourceProxycheck).typeOf("residential"), ev(SourceIPAPI).typeOf("datacenter"))
	input.Evidence[1].ValidUntil = testNow
	if a := AssessInput(input); a.IPType != IPTypeResidential {
		t.Fatal("expired vote retained the conflict")
	}
	input.Evidence = evidence(ev(SourceProxycheck).typeOf("residential"), ev(SourceIPPure).residential(true), ev(SourceIPAPI).typeOf("datacenter"))
	if a := AssessInput(input); a.IPType != IPTypeConflicting {
		t.Fatal("majority concealed conflict")
	}
}
