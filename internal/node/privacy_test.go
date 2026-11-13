package node

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/nodeprivacy"
)

func TestPrivacyClassifier(t *testing.T) {
	for _, tc := range []struct{ name, raw, reason string }{
		{"trojan plaintext", `{"type":"trojan","password":"fixture"}`, PrivacyPlaintext},
		{"http", `{"type":"http"}`, PrivacyPlaintext},
		{"authenticated socks", `{"type":"socks","username":"fixture","password":"fixture"}`, PrivacyPlaintext},
		{"vless websocket", `{"type":"vless","transport":{"type":"ws"}}`, PrivacyPlaintext},
		{"vmess none", `{"type":"vmess","security":"none"}`, PrivacyPlaintext},
		{"ss none", `{"type":"shadowsocks","method":"none"}`, PrivacyPlaintext},
		{"tls http", `{"type":"http","tls":{"enabled":true}}`, ""},
		{"encrypted vmess", `{"type":"vmess","security":"auto"}`, ""},
		{"encrypted ss", `{"type":"shadowsocks","method":"aes-128-gcm"}`, ""},
		{"identity", `{"type":"http","tls":{"enabled":true},"headers":{"X-Forwarded-For":"203.0.113.4"}}`, PrivacyIdentity},
		{"chain", `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"http","tls":{"enabled":true},"detour":"d0"},"deps":[{"type":"socks","tag":"d0"}]}`, PrivacyPlaintext},
		{"mihomo", `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"http","tls":true}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := ClassifyPrivacy([]byte(tc.raw))
			if tc.reason == "" && len(r) != 0 || tc.reason != "" && !slices.Contains(r, tc.reason) {
				t.Fatalf("reasons=%v want %s", r, tc.reason)
			}
		})
	}
	r, w := ClassifyPrivacy([]byte(`{"type":"http","tls":{"enabled":true,"insecure":true}}`))
	if len(r) != 0 || !slices.Contains(w, "TLS_VERIFICATION_DISABLED") {
		t.Fatalf("insecure TLS is an explicit advisory: %v %v", r, w)
	}
}

func TestPrivacyClassifierDirectCannotClaimTLS(t *testing.T) {
	for _, raw := range []string{
		`{"type":"direct","tls":{"enabled":true}}`,
		`{"type":"direct","tls":true}`,
		`{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"http","tls":{"enabled":true},"detour":"d0"},"deps":[{"type":"direct","tls":{"enabled":true},"tag":"d0"}]}`,
	} {
		if got := PrivacyDeletionReason([]byte(raw)); got != "DIRECT_EGRESS" {
			t.Errorf("direct must be rejected before import even with an irrelevant TLS field: reason=%q", got)
		}
	}
}

func TestPrivacyDenyMatcherAndLivePolicy(t *testing.T) {
	p, err := NewPrivacyPolicy(nodeprivacy.Config{ProbeStatus: "ready", OwnCIDRs: []string{"192.0.2.4", "203.0.113.7/24", "2001:db8::/32", "::ffff:198.51.100.0/120"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.0.2.4", "::ffff:192.0.2.4", "203.0.113.255", "2001:db8::4", "198.51.100.24"} {
		if !p.Denies(netip.MustParseAddr(ip)) {
			t.Fatal("missed", ip)
		}
	}
	for _, ip := range []string{"192.0.2.5", "203.0.114.1", "2001:db9::1"} {
		if p.Denies(netip.MustParseAddr(ip)) {
			t.Fatal("overblocked", ip)
		}
	}
	for _, cidr := range []string{"bad", "1.2.3.4/99", "fe80::1%eth0", "::ffff:1.2.3.4/80"} {
		if _, err := NewPrivacyPolicy(nodeprivacy.Config{OwnCIDRs: []string{cidr}}); err == nil {
			t.Fatal("accepted", cidr)
		}
	}
	e := NewNodeEntry(Hash{}, []byte(`{"type":"http","server":"192.0.2.5","tls":{"enabled":true}}`), time.Now(), 0)
	var source atomic.Pointer[PrivacyPolicy]
	source.Store(p)
	e.SetPrivacyPolicySource(&source)
	if e.PrivacyAllowed() {
		t.Fatal("unknown egress became sellable")
	}
	e.SetEgressIP(netip.MustParseAddr("192.0.2.5"))
	e.SetIdentityMeasurement(e.GetEgressIP(), "measured")
	if !e.PrivacyAllowed() {
		t.Fatal("legitimate self-egress rejected")
	}
	e.SetEgressIP(netip.MustParseAddr("192.0.2.4"))
	if e.PrivacyAllowed() || !slices.Contains(e.Privacy().Reasons, PrivacyOwnAsset) {
		t.Fatal("owned egress accepted")
	}
	e.SetEgressIP(netip.MustParseAddr("192.0.2.5"))
	e.SetIdentityMeasurement(e.GetEgressIP(), "measured")
	failed, _ := NewPrivacyPolicy(nodeprivacy.Config{})
	source.Store(failed)
	if !e.PrivacyAllowed() || !e.AutomaticProbeAllowed() || !slices.Contains(e.Privacy().Warnings, PrivacyDiscovery) {
		t.Fatal("discovery failure must warn without stopping sale or health probes")
	}
}

func TestEchoMeasurementSwitchNeverClaimsUnmeasuredAnonymity(t *testing.T) {
	e := NewNodeEntry(Hash{}, []byte(`{"type":"http","tls":{"enabled":true}}`), time.Now(), 0)
	e.SetEgressIP(netip.MustParseAddr("203.0.113.8"))
	var source atomic.Pointer[PrivacyPolicy]
	for _, enabled := range []bool{false, true} {
		p, err := NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: enabled, EchoURL: "https://echo.example/check", OwnCIDRs: []string{"192.0.2.0/24"}})
		if err != nil {
			t.Fatal(err)
		}
		source.Store(p)
		e.SetPrivacyPolicySource(&source)
		if e.PrivacyAllowed() == enabled || e.IdentityRetryDue(time.Now()) != enabled || !e.AutomaticProbeAllowed() {
			t.Fatal("measurement switch did not control sale/collection independently of discovery", enabled)
		}
		if e.IdentityStatus() == "measured" || e.IdentityEvidence().State != "unknown" {
			t.Fatal("switch manufactured anonymity")
		}
		if !enabled {
			e.RecordIdentityPrerequisiteFailure()
			if e.identityCheck.Load() != nil || e.IdentityEvidence().Cause != "MEASUREMENT_DISABLED" {
				t.Fatal("disabled echo still collects failures")
			}
		}
		e.SetEgressIP(netip.MustParseAddr("192.0.2.8"))
		if e.PrivacyAllowed() || e.AutomaticProbeAllowed() {
			t.Fatal("switch waived known own asset")
		}
		e.SetEgressIP(netip.MustParseAddr("203.0.113.8"))
	}
}

func TestPrivacyDeletionCancelsDialsAndClosesExistingStreams(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	ctx, cancel := e.BindPrivacyContext(context.Background())
	defer cancel()
	local, peer := net.Pipe()
	defer peer.Close()
	release, ok := e.trackPrivacyCloser(local)
	if !ok {
		t.Fatal("tracking failed")
	}
	defer release()
	e.CancelPrivacyWork()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("dial not canceled")
	}
	if _, err := local.Write([]byte("must not leak")); err == nil {
		t.Fatal("stream survived deletion")
	}
	other, otherPeer := net.Pipe()
	defer otherPeer.Close()
	if _, ok := e.trackPrivacyCloser(other); ok {
		t.Fatal("late dial survived deletion")
	}
}

func TestIdentityMeasurementCannotClaimNewEchoPolicy(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	ip := netip.MustParseAddr("203.0.113.8")
	e.SetEgressIP(ip)
	var source atomic.Pointer[PrivacyPolicy]
	p, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoURL: "https://old.example/check"})
	source.Store(p)
	e.SetPrivacyPolicySource(&source)
	next, _ := NewPrivacyPolicy(nodeprivacy.Config{EchoURL: "https://new.example/check"})
	source.Store(next)
	e.SetIdentityMeasurementFromEcho(ip, "measured", "https://old.example/check")
	if e.IdentityStatus() != "unmeasured" {
		t.Fatal("old request claimed new endpoint passed")
	}
}
