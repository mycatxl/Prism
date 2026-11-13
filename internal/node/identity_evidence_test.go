package node

import (
	"net/netip"
	"prism/internal/nodeprivacy"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdentityUnknownAlwaysExplainsCauseAndClearingAction(t *testing.T) {
	e := NewNodeEntry(Hash{}, []byte(`{"type":"shadowsocks"}`), time.Now(), 0)
	var policy atomic.Pointer[PrivacyPolicy]
	check := func(want string) {
		t.Helper()
		f := e.IdentityEvidence()
		if f.State != "unknown" || f.Cause != want || f.Action == "" || f.SourceClass != "active_probe" {
			t.Fatalf("missing explanation: %+v", f)
		}
	}
	check("POLICY_UNMANAGED")
	p, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready"})
	policy.Store(p)
	e.SetPrivacyPolicySource(&policy)
	check("ENDPOINT_UNCONFIGURED")
	p, _ = NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready", EchoURL: "https://echo.example/check"})
	policy.Store(p)
	check("EGRESS_UNMEASURED")
	ip := netip.MustParseAddr("203.0.113.1")
	e.SetEgressIP(ip)
	check("NOT_ATTEMPTED")
	for _, code := range []string{"TIMEOUT", "TLS_FAILED", "HTTP_FAILED", "NETWORK_FAILED", "INVALID_RESPONSE", "CHALLENGE_MISMATCH", "HEADER_OBSERVATION_MISSING", "OUTBOUND_NOT_READY"} {
		e.RecordIdentityMeasurement(ip, "measured_failed", p.echoURL, code)
		check(code)
	}
	e.SetIdentityMeasurement(ip, "measured")
	if f := e.IdentityEvidence(); f.State != "known" || f.ValidUntil == "" || f.Family != "ipv4" {
		t.Fatal(f)
	}
	e.SetEgressIP(netip.MustParseAddr("203.0.113.2"))
	check("EGRESS_CHANGED")
	e.SetEgressIP(ip)
	e.identityCheck.Store(&IdentityMeasurement{IP: ip, EchoURL: p.echoURL, Status: "measured", At: time.Now().Add(-25 * time.Hour)})
	check("EXPIRED")
	p2, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready", EchoURL: "https://new.example/check"})
	policy.Store(p2)
	check("ENDPOINT_CHANGED")
}

func TestIdentityRetryIsBoundedAndDoesNotBlockHealth(t *testing.T) {
	e := NewNodeEntry(Hash{}, []byte(`{"type":"shadowsocks"}`), time.Now(), 0)
	var policy atomic.Pointer[PrivacyPolicy]
	p, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready", EchoURL: "https://echo.example/check"})
	policy.Store(p)
	e.SetPrivacyPolicySource(&policy)
	ip := netip.MustParseAddr("203.0.113.1")
	e.SetEgressIP(ip)
	if !e.IdentityRetryDue(time.Now()) {
		t.Fatal("startup not scheduled")
	}
	for i := 1; i <= 5; i++ {
		e.RecordIdentityMeasurement(ip, "measured_failed", p.echoURL, "TIMEOUT")
	}
	if e.IdentityRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("unbounded accelerated retries")
	}
	if !e.AutomaticProbeAllowed() || e.PrivacyAllowed() {
		t.Fatal("echo failure changed health/sale contract")
	}
	e.SetEgressIP(netip.MustParseAddr("203.0.113.2"))
	if !e.IdentityRetryDue(time.Now()) {
		t.Fatal("new exit did not clear terminal attempt scope")
	}
}

func TestLateEchoCannotOverwriteNewExitAndTraceFailuresConsumeBudget(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	var policy atomic.Pointer[PrivacyPolicy]
	p, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready", EchoURL: "https://echo.example/check"})
	policy.Store(p)
	e.SetPrivacyPolicySource(&policy)
	a, b := netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2")
	e.SetEgressIP(a)
	started := time.Now()
	e.SetEgressIP(b)
	e.SetIdentityMeasurement(b, "measured")
	if e.CompleteIdentityMeasurement(a, "measured_failed", p.echoURL, "EGRESS_CHANGED", started) || e.IdentityStatus() != "measured" {
		t.Fatal("late old exit replaced current echo")
	}
	if e.CompleteIdentityMeasurement(b, "measured_failed", p.echoURL, "TIMEOUT", started) || e.IdentityStatus() != "measured" {
		t.Fatal("late failure replaced newer completed measurement")
	}
	e.SetEgressIP(a)
	for i := 0; i < 5; i++ {
		e.RecordIdentityPrerequisiteFailure()
	}
	if e.IdentityRetryDue(time.Now().Add(time.Hour)) {
		t.Fatal("trace failures bypassed bounded echo budget")
	}
}
