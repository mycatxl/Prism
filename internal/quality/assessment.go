package quality

import "time"

// Assessment keeps IPPure's score and ProxyCheck's network evidence separate.
// The verdict never averages the two providers' different risk models.
type Assessment struct {
	State         string   `json:"state"`
	ScoreSource   string   `json:"score_source"`
	PurityScore   *int     `json:"purity_score"`
	PurityBand    string   `json:"purity_band"`
	NetworkType   string   `json:"network_type"`
	NetworkSource string   `json:"network_source"`
	Native        *bool    `json:"native"`
	Verdict       string   `json:"verdict"`
	Reasons       []string `json:"reasons"`
	TorRoles      []string `json:"tor_roles,omitempty"`
}

func currentSource(s Summary, provider string, now time.Time) *Evidence {
	for _, source := range s.Sources {
		if source.Provider == provider && source.Evidence != nil && source.Evidence.Provider == provider && source.Evidence.IP == s.IP &&
			(source.State == "valid" || source.State == "conflicting") && now.Before(source.Evidence.ValidUntil) {
			return source.Evidence
		}
	}
	return nil
}

func Assess(s Summary, now time.Time) Assessment {
	a := Assessment{State: "unobserved", ScoreSource: "ippure", PurityBand: "unknown", NetworkType: "unknown", Verdict: "pending", Reasons: []string{}}
	proxy := currentSource(s, ProviderID, now)
	pure := currentSource(s, "ippure", now)
	if s.State == "unsupported" {
		a.State = "unsupported"
	}
	if s.State == "pending" { a.State = "pending" }
	if proxy != nil {
		a.NetworkType, a.NetworkSource = proxy.IPType, ProviderID
		a.State = "partial"
	}
	if pure != nil {
		a.Native = pure.Native
		if a.NetworkType == "unknown" {
			if pure.SourceType == "Residential" {
				a.NetworkType, a.NetworkSource = "residential", "ippure"
			}
			if pure.SourceType == "Non-residential" {
				a.NetworkType, a.NetworkSource = "non_residential", "ippure"
			}
		}
		a.PurityScore = PurityScore(pure.RiskScore)
		a.PurityBand = PurityBand(a.PurityScore)
		if a.PurityScore != nil {
			a.State = "valid"
			if proxy == nil {
				a.State = "partial"
			}
		} else {
			a.State = "unsupported"
			a.Reasons = append(a.Reasons, "IPPURE_SCORE_UNAVAILABLE")
		}
	} else {
		a.Reasons = append(a.Reasons, "IPPURE_REQUIRED")
		for _, source := range s.Sources {
			if source.Provider == "ippure" && source.Evidence != nil && source.Evidence.IP == s.IP && !now.Before(source.Evidence.ValidUntil) {
				a.State = "stale"
			}
		}
	}
	if proxy == nil {
		a.Reasons = append(a.Reasons, "NETWORK_EVIDENCE_MISSING")
	}
	networkComplete := proxy != nil && proxy.Signals.Proxy != nil && proxy.Signals.VPN != nil && proxy.Signals.Tor != nil && proxy.Signals.Compromised != nil
	if a.State == "valid" && !networkComplete { a.State = "partial" }
	review, highRisk, conflict := false, false, false
	if tor := currentSource(s, "torproject", now); tor != nil && len(tor.TorRoles) > 0 {
		a.TorRoles = append([]string(nil), tor.TorRoles...)
		review = true
		a.Reasons = append(a.Reasons, "TOR_PUBLIC_RELAY")
	}
	if proxy != nil {
		for _, signal := range []struct {
			value  *bool
			reason string
		}{
			{proxy.Signals.Proxy, "PROXY_DETECTED"}, {proxy.Signals.VPN, "VPN_DETECTED"},
			{proxy.Signals.Tor, "TOR_DETECTED"}, {proxy.Signals.Scraper, "SCRAPER_DETECTED"},
			{proxy.Signals.Anonymous, "ANONYMOUS_DETECTED"},
		} {
			if signal.value != nil && *signal.value {
				review = true
				a.Reasons = append(a.Reasons, signal.reason)
			}
		}
		if proxy.Signals.Compromised != nil && *proxy.Signals.Compromised {
			highRisk = true
			a.Reasons = append(a.Reasons, "COMPROMISED")
		}
		if proxy.IPType == "conflicting" {
			conflict = true
		}
		if pure != nil && ((pure.SourceType == "Residential" && proxy.IPType == "datacenter") ||
			(pure.SourceType == "Non-residential" && proxy.IPType == "residential")) {
			conflict = true
		}
		if len(proxy.AttackHistory) > 0 {
			review = true
			a.Reasons = append(a.Reasons, "ATTACK_HISTORY")
		}
	}
	if abuse := currentSource(s, AbuseProviderID, now); abuse != nil && abuse.AbuseConfidence != nil && *abuse.AbuseConfidence >= 75 && abuse.TotalReports != nil && *abuse.TotalReports > 0 {
		highRisk = true
		a.Reasons = append(a.Reasons, "RECENT_ABUSE")
	}
	if a.PurityScore != nil && *a.PurityScore < 60 {
		highRisk = true
		a.Reasons = append(a.Reasons, "IPPURE_HIGH_RISK")
	}
	if conflict {
		a.NetworkType = "conflicting"
		a.State = "conflicting"
		a.Reasons = append(a.Reasons, "NETWORK_TYPE_CONFLICT")
	}
	switch {
	case highRisk:
		a.Verdict = "high_risk"
	case conflict:
		a.Verdict = "conflicting"
	case review:
		a.Verdict = "review"
	case a.PurityScore == nil:
		a.Verdict = "pending"
	case proxy == nil:
		a.Verdict = "incomplete"
	case !networkComplete:
		a.Verdict = "incomplete"
		a.Reasons = append(a.Reasons, "NETWORK_EVIDENCE_INCOMPLETE")
	case *a.PurityScore < 80:
		a.Verdict = "caution"
	default:
		a.Verdict = "favorable"
	}
	return a
}
