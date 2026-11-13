package assess

import (
	"encoding/json"
	"net/netip"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestPuritySingleIPPureSource(t *testing.T) {
	for _, risk := range []int{0, 20, 100} {
		for _, flagged := range []bool{false, true} {
			rows := evidence(ev(SourceIPPure).fraud(20), ev(SourceProxycheck).risk(risk).flag("proxy", flagged).flag("vpn", flagged).flag("tor", flagged), ev(SourceIPAPIIs).completeNegatives(), ev(SourceDNSBL).dnsbl([]string{"zone"}, nil))
			got := Assess(testIPv4Addr(t), rows, testNow, nil)
			if got.PurityScore == nil || *got.PurityScore != 80 {
				t.Errorf("risk=%d flags=%v: purity=%v, want 80", risk, flagged, formatInt(got.PurityScore))
			}
			if flagged && got.Flags&(FlagProxy|FlagVPN|FlagTor) != FlagProxy|FlagVPN|FlagTor {
				t.Error("auxiliary flags lost")
			}
		}
	}
}

func TestPurityIPPureFailureReasons(t *testing.T) {
	cases := []struct {
		name, reason, failure string
		pure                  []quality.Evidence
	}{
		{name: "not_run", reason: "IPPURE_NOT_RUN"},
		{name: "expired", reason: "EVIDENCE_EXPIRED", pure: evidence(ev(SourceIPPure).fraud(20).expired())},
		{name: "mismatch", reason: "IPPURE_EGRESS_MISMATCH", pure: evidence(ev(SourceIPPure).fraud(20).on("8.8.4.4"))},
		{name: "no_ip", reason: "IPPURE_INVALID_RESPONSE", pure: evidence(ev(SourceIPPure).fraud(20).on(""))},
		{name: "missing_score", reason: "IPPURE_NO_FRAUD_SCORE", pure: evidence(ev(SourceIPPure).residential(true))},
		{name: "invalid_score", reason: "IPPURE_INVALID_RESPONSE", pure: evidence(ev(SourceIPPure).fraud(101))},
		{name: "429", reason: "IPPURE_RATE_LIMITED", failure: "PROVIDER_LIMIT"},
		{name: "timeout", reason: "IPPURE_TIMEOUT", failure: "PROVIDER_TIMEOUT"},
		{name: "auth", reason: "IPPURE_AUTH_FAILED", failure: "PROVIDER_AUTH"},
		{name: "response", reason: "IPPURE_INVALID_RESPONSE", failure: "PROVIDER_RESPONSE"},
		{name: "unavailable", reason: "IPPURE_PROVIDER_UNAVAILABLE", failure: "PROVIDER_UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := append(tc.pure, ev(SourceProxycheck).risk(0).e)
			failed := map[string]string{}
			if tc.failure != "" {
				failed[SourceIPPure] = tc.failure
			}
			got := AssessInput(Input{IP: testIPv4Addr(t), Evidence: rows, Now: testNow, Failed: failed})
			if got.PurityScore != nil || !hasReason(got, tc.reason) {
				t.Fatalf("score=%s reasons=%v; want null/%s", formatInt(got.PurityScore), got.Reasons, tc.reason)
			}
		})
	}
}

func TestPurityNetworkConflictNeverVotesAway(t *testing.T) {
	rows := evidence(ev(SourceIPPure).fraud(20).residential(true), ev(SourceProxycheck).typeOf("residential"), ev(SourceIPAPI).typeOf("datacenter"))
	got := Assess(testIPv4Addr(t), rows, testNow, nil)
	if got.IPType != IPTypeConflicting {
		t.Fatalf("majority hid conflict: %s", got.IPType)
	}
	rows = evidence(ev(SourceIPPure).fraud(20).residential(false), ev("dbip_lite").asn(16509))
	got = Assess(testIPv4Addr(t), rows, testNow, nil)
	if got.IPType != IPTypeNonResidential {
		t.Fatalf("false inferred specific type: %s", got.IPType)
	}
	raw, _ := json.Marshal(got)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["facts"]; !ok {
		t.Fatal("field provenance absent")
	}
}

func TestPurityPrimaryExpiryIndependentOfAuxiliaryTTL(t *testing.T) {
	rows := evidence(ev(SourceIPPure).fraud(20).until(testNow.Add(24*time.Hour)), ev(SourceProxycheck).risk(0).until(testNow.Add(time.Minute)))
	got := Assess(testIPv4Addr(t), rows, testNow, nil)
	if !got.ValidUntil.Equal(rows[0].ValidUntil) {
		t.Fatal("auxiliary TTL changed primary validity")
	}
}

func TestPurityIPv6AndChangedExitNeverFallback(t *testing.T) {
	for _, addr := range []string{testIPv6, "8.8.4.4"} {
		rows := evidence(ev(SourceIPPure).fraud(20), ev(SourceProxycheck).risk(0).on(addr))
		reason := "IPPURE_EGRESS_MISMATCH"
		if addr == testIPv6 {
			rows[0].IP = addr
			reason = "IPPURE_IPV6_SCORE_UNAVAILABLE"
		}
		got := Assess(netip.MustParseAddr(addr), rows, testNow, nil)
		if got.PurityScore != nil || !hasReason(got, reason) {
			t.Fatalf("%s: primary fallback or reason lost", addr)
		}
	}
}

func TestPurityExpiryPrecedesPayloadValidation(t *testing.T) {
	for _, row := range []quality.Evidence{
		ev(SourceIPPure).expired().on("").e,
		ev(SourceIPPure).expired().observedAt(testNow.Add(time.Hour)).e,
		ev(SourceIPPure).expired().fraud(101).e,
	} {
		got := Assess(testIPv4Addr(t), []quality.Evidence{row}, testNow, nil)
		if got.PurityScore != nil || got.Facts["purity"].Cause != "EVIDENCE_EXPIRED" {
			t.Errorf("expired payload masked expiry: %s", got.Facts["purity"].Cause)
		}
	}
}

func TestPurityLegacyISPLabelCannotCreateResidentialProvenance(t *testing.T) {
	for _, provider := range []string{SourceIPAPIIs, SourceIPAPIIsNode} {
		legacy := ev(provider).typeOf("residential").residential(true).e
		legacy.SourceType = "isp"
		got := Assess(testIPv4Addr(t), []quality.Evidence{legacy}, testNow, nil)
		if got.IPType != IPTypeUnknown || got.Facts["is_residential"].State == "known" || len(got.Facts["is_residential"].Observations) != 0 {
			t.Errorf("%s invented residential field provenance", provider)
		}
	}
}
