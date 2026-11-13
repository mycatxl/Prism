package platform

import (
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/nodeprivacy"
	"prism/internal/testutil"
)

func TestUnverifiedTLSIsDisclosedButNotADeletionOrSaleBan(t *testing.T) {
	for _, raw := range []string{
		`{"type":"http","tls":{"enabled":true,"insecure":true}}`,
		`{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"trojan","skip-cert-verify":true}}`,
	} {
		e := node.NewNodeEntry(node.HashFromRawOptions([]byte(raw)), []byte(raw), time.Now(), 16)
		e.AddSubscriptionID("source")
		e.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
		e.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{Ewma: time.Millisecond, LastUpdated: time.Now()})
		ob := testutil.NewNoopOutbound()
		e.Outbound.Store(&ob)
		var source atomic.Pointer[node.PrivacyPolicy]
		p, err := node.NewPrivacyPolicy(nodeprivacy.Config{EchoEnabled: true, ProbeStatus: "ready"})
		if err != nil {
			t.Fatal(err)
		}
		source.Store(p)
		e.SetPrivacyPolicySource(&source)
		e.SetIdentityMeasurement(e.GetEgressIP(), "measured")
		if node.PrivacyDeletionReason(e.RawOptions) != "" || !slices.Contains(e.Privacy().Warnings, "TLS_VERIFICATION_DISABLED") {
			t.Fatal("transport warning hidden or incorrectly classified as proven leakage")
		}
		snap := newFakeSnapshot().set("1.2.3.4", projectionAt(time.Now(), 0))
		pform := NewPlatform("default", "Default", nil, nil)
		pform.SetQualitySnapshot(snap)
		pform.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) { fn(e.Hash, e) }, alwaysLookup, nil, nil)
		if !pform.View().Contains(e.Hash) {
			t.Fatal("disclosed TLS warning silently blocked sale")
		}
		if ok, reason := MatchNodeCriteria(NodeCriteria{}, e, alwaysLookup, nil, nil, snap, time.Now()); !ok {
			t.Fatal("sale criteria rejected warning", reason)
		}
		// Warning-only does not waive actual risk findings or measurement requirements.
		e.SetIdentityMeasurement(e.GetEgressIP(), "measured_failed")
		if ok, _ := MatchNodeCriteria(NodeCriteria{}, e, alwaysLookup, nil, nil, snap, time.Now()); ok {
			t.Fatal("warning policy bypassed enabled echo gate")
		}
	}
}
