package quality

import (
	"slices"
	"testing"
	"time"
)

func TestAssessmentUsesIPPureScoreAndIndependentRiskEvidence(t *testing.T) {
	now := time.Now().UTC()
	low, perfect := 5, 0
	no, yes := false, true
	proxy := &Evidence{IP: "8.8.8.8", Provider: ProviderID, IPType: "residential", RiskScore: &perfect, ValidUntil: now.Add(time.Hour),
		Signals: Signals{Proxy: &no, VPN: &no, Tor: &no, Compromised: &no}}
	summary := Summary{IP: proxy.IP, State: "valid", Evidence: proxy, Sources: []SourceSummary{{Provider: ProviderID, State: "valid", Evidence: proxy}}}
	a := Assess(summary, now)
	if a.PurityScore != nil || a.PurityBand != "unknown" || a.Verdict != "pending" {
		t.Fatal("missing IPPure result was replaced by ProxyCheck score")
	}
	pure := &Evidence{IP: proxy.IP, Provider: "ippure", SourceType: "Residential", RiskScore: &low, ValidUntil: now.Add(time.Hour)}
	summary.Sources = append(summary.Sources, SourceSummary{Provider: "ippure", State: "valid", Evidence: pure})
	a = Assess(summary, now)
	if a.PurityScore == nil || *a.PurityScore != 95 || a.PurityBand != "excellent" || a.Verdict != "favorable" {
		t.Fatalf("IPPure score was not preserved: %+v", a)
	}
	proxy.Signals.Tor = nil
	if a = Assess(summary, now); a.State != "partial" || a.Verdict != "incomplete" {
		t.Fatal("partial network evidence was presented as complete")
	}
	proxy.Signals.Tor = &no
	proxy.Signals.VPN = &yes
	a = Assess(summary, now)
	if *a.PurityScore != 95 || a.Verdict != "review" || !slices.Contains(a.Reasons, "VPN_DETECTED") {
		t.Fatal("cross-check overwrote score or hid VPN evidence")
	}
	proxy.Signals.VPN = &no
	proxy.IPType = "datacenter"
	a = Assess(summary, now)
	if a.NetworkType != "conflicting" || a.Verdict != "conflicting" || *a.PurityScore != 95 {
		t.Fatal("classification conflict was flattened")
	}
	proxy.IPType = "residential"
	pure.IP = "1.1.1.1"
	if a = Assess(summary, now); a.PurityScore != nil {
		t.Fatal("evidence from another IP was attached")
	}
	pure.IP = proxy.IP
	pure.ValidUntil = now
	if a = Assess(summary, now); a.PurityScore != nil || a.State != "stale" {
		t.Fatal("expired primary evidence stayed qualified")
	}
	pure.ValidUntil = now.Add(time.Hour)
	pure.RiskScore = nil
	if a = Assess(summary, now); a.PurityScore != nil || a.State != "unsupported" {
		t.Fatal("missing provider score became perfect purity")
	}
}

func TestQueuedInspectionStateRemainsPending(t *testing.T) {
	summary := Summary{IP: "8.8.8.8", State: "pending", Task: &Task{State: "queued"}}
	if got := Assess(summary, time.Now()); got.State != "pending" || got.PurityScore != nil {
		t.Fatal("queued inspection was lost from the waiting filter")
	}
}

func TestPublicTorEvidenceAddsReviewWithoutChangingIPPure(t *testing.T) {
	now := time.Now()
	risk := 1
	summary := Summary{IP: "8.8.8.8", Sources: []SourceSummary{
		{Provider: "ippure", State: "valid", Evidence: &Evidence{IP: "8.8.8.8", Provider: "ippure", RiskScore: &risk, ValidUntil: now.Add(time.Hour)}},
		{Provider: "torproject", State: "valid", Evidence: &Evidence{IP: "8.8.8.8", Provider: "torproject", TorRoles: []string{"exit"}, ValidUntil: now.Add(time.Hour)}},
	}}
	a := Assess(summary, now)
	if a.PurityScore == nil || *a.PurityScore != 99 || a.Verdict != "review" || !slices.Contains(a.TorRoles, "exit") {
		t.Fatalf("registry evidence lost: %+v", a)
	}
}
