package assess

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"prism/internal/quality"
)

func primaryEvidence(in Input) (*quality.Evidence, quality.Fact) {
	missing := func(code string) (*quality.Evidence, quality.Fact) { return nil, quality.PurityDiagnostic(code) }
	if !in.IP.IsValid() {
		return missing("NO_EGRESS")
	}
	if failure, ok := in.Failed[SourceIPPure]; ok {
		code := map[string]string{"PROVIDER_TIMEOUT": "IPPURE_TIMEOUT", "PROVIDER_LIMIT": "IPPURE_RATE_LIMITED", "PROVIDER_AUTH": "IPPURE_AUTH_FAILED", "EGRESS_MISMATCH": "IPPURE_EGRESS_MISMATCH", "PROVIDER_RESPONSE": "IPPURE_INVALID_RESPONSE", "PROVIDER_UNAVAILABLE": "IPPURE_PROVIDER_UNAVAILABLE", "PROVIDER_REQUEST": "IPPURE_PROVIDER_UNAVAILABLE", "UNSUPPORTED_IP": "IPPURE_IPV6_SCORE_UNAVAILABLE"}[failure]
		if code == "" {
			code = "NO_VALID_IPPURE_EVIDENCE"
		}
		f := quality.PurityDiagnostic(code)
		f.ErrorCode = quality.SafeCode(failure)
		return nil, f
	}
	ev, ok := lookup(independentEvidence(in.Evidence), SourceIPPure)
	if !ok {
		if in.Enabled != nil && !in.Enabled[SourceIPPure] {
			return missing("IPPURE_PROVIDER_UNAVAILABLE")
		}
		return missing("IPPURE_NOT_RUN")
	}
	code := ""
	ip, err := netip.ParseAddr(ev.IP)
	switch {
	case !ev.ValidUntil.IsZero() && !in.Now.Before(ev.ValidUntil):
		code = "EVIDENCE_EXPIRED"
	case err != nil:
		code = "IPPURE_INVALID_RESPONSE"
	case ip.Unmap() != in.IP.Unmap():
		code = "IPPURE_EGRESS_MISMATCH"
	case ev.ObservedAt.IsZero() || ev.ObservedAt.After(in.Now) || ev.ValidUntil.IsZero():
		code = "IPPURE_INVALID_RESPONSE"
	case !ev.ValidUntil.After(ev.ObservedAt):
		code = "IPPURE_INVALID_RESPONSE"
	case ip.Is6() && !ip.Is4In6():
		code = "IPPURE_IPV6_SCORE_UNAVAILABLE"
	case ev.FraudScore == nil:
		code = "IPPURE_NO_FRAUD_SCORE"
	case *ev.FraudScore < 0 || *ev.FraudScore > 100:
		code = "IPPURE_INVALID_RESPONSE"
	}
	f := quality.Known("derived")
	if code != "" {
		f = quality.PurityDiagnostic(code)
	} else {
		f.Explanation = "纯净度 = 100 − fraudScore；仅使用当前出口的有效来源证据。"
	}
	f.Provider = SourceIPPure
	f.Sources = []string{SourceIPPure}
	if !ev.ObservedAt.IsZero() {
		f.ObservedAt = &ev.ObservedAt
	}
	if !ev.ValidUntil.IsZero() {
		f.ValidUntil = &ev.ValidUntil
	}
	f.FraudScore = ev.FraudScore
	if code != "" {
		return nil, f
	}
	return &ev, f
}

// Priorities select a provenance owner among agreeing observations. They never
// suppress a conflicting value. A broad non_residential statement can be refined
// only by an explicit, compatible network type from a different source.
var networkPriority = []string{SourceProxycheck, SourceIPAPIIs, SourceIPAPI, SourceIPQS, SourceIPPure}

func observation(ev quality.Evidence, value any) quality.FieldObservation {
	return quality.FieldObservation{Provider: ev.Provider, Value: value, ObservedAt: ev.ObservedAt, ValidUntil: ev.ValidUntil}
}

func observedFact(observations []quality.FieldObservation) quality.Fact {
	f := quality.Known("vendor_claim")
	f.Observations = observations
	for _, o := range observations {
		f.Sources = append(f.Sources, o.Provider)
	}
	if len(observations) > 0 {
		o := observations[0]
		f.Provider = o.Provider
		f.ObservedAt = &o.ObservedAt
		f.ValidUntil = &o.ValidUntil
	}
	return f
}

func resolveNetwork(evidence []quality.Evidence) (string, quality.Fact) {
	var observations []quality.FieldObservation
	kinds := map[string]bool{}
	for _, provider := range networkPriority {
		ev, ok := lookup(evidence, provider)
		if !ok {
			continue
		}
		kind := voteOf(ev)
		if kind == "" {
			continue
		}
		kinds[kind] = true
		observations = append(observations, observation(ev, kind))
	}
	f := observedFact(observations)
	if len(kinds) == 0 {
		return IPTypeUnknown, quality.Missing("vendor_claim", "FIELD_NOT_REPORTED", "刷新提供明确网络类型的来源；ASN 或组织名称不能证明线路类型。")
	}
	specific := 0
	for k := range kinds {
		if k != IPTypeNonResidential {
			specific++
		}
	}
	if kinds[IPTypeConflicting] || specific > 1 || kinds[IPTypeResidential] && kinds[IPTypeNonResidential] {
		f.State = "conflicting"
		f.Cause = "PROVIDER_TYPE_DISAGREEMENT"
		f.Explanation = "网络类型来源存在冲突。"
		f.Action = "查看各来源原值并刷新；冲突解决前不承诺具体类型。"
		f.Provider = ""
		return IPTypeConflicting, f
	}
	kind := IPTypeNonResidential
	for _, o := range observations {
		if o.Value != IPTypeNonResidential {
			kind = o.Value.(string)
			f.Provider = o.Provider
			f.ObservedAt = &o.ObservedAt
			f.ValidUntil = &o.ValidUntil
			break
		}
	}
	return kind, f
}

func networkType(evidence []quality.Evidence, reasons *[]string) string {
	kind, _ := resolveNetwork(evidence)
	if kind == IPTypeUnknown {
		*reasons = append(*reasons, ReasonIPTypeUnknown)
	}
	if kind == IPTypeConflicting {
		*reasons = append(*reasons, ReasonIPTypeConflicting)
	}
	return kind
}

func fieldValue(f quality.Fact) any {
	if len(f.Observations) == 0 {
		return nil
	}
	return f.Observations[0].Value
}

func fieldFacts(in Input, current []quality.Evidence) map[string]quality.Fact {
	facts := map[string]quality.Fact{}
	// Keep expiry and provenance for each independent positive risk flag.
	for _, entry := range []struct {
		name string
		flag Flags
	}{{"compromised", FlagCompromised}, {"tor", FlagTor}, {"vpn", FlagVPN}, {"proxy", FlagProxy}, {"scraper", FlagScraper}, {"abuse", FlagAbuse}, {"anonymous", FlagAnonymous}, {"hosting", FlagHosting}, {"dnsbl", FlagDNSBL}, {"attack_history", FlagAttackHistory}} {
		var observations []quality.FieldObservation
		for _, ev := range current {
			flags, _ := detectFlags([]quality.Evidence{ev})
			if flags&entry.flag != 0 {
				observations = append(observations, observation(ev, true))
			}
		}
		if len(observations) > 0 {
			facts["flag:"+entry.name] = observedFact(observations)
		}
	}
	getters := map[string]func(quality.Evidence) any{
		"asn": func(e quality.Evidence) any {
			if e.ASNNumber > 0 {
				return e.ASNNumber
			}
			return nil
		},
		"as_org":  func(e quality.Evidence) any { return textValue(e.Organization) },
		"country": func(e quality.Evidence) any { return textValue(strings.ToUpper(e.CountryCode)) },
		"city":    func(e quality.Evidence) any { return textValue(e.City) },
		"region":  func(e quality.Evidence) any { return textValue(e.Region) },
		"is_residential": func(e quality.Evidence) any {
			if e.IsResidential != nil {
				return *e.IsResidential
			}
			return nil
		},
		"is_mobile": func(e quality.Evidence) any {
			if e.IsMobile != nil {
				return *e.IsMobile
			}
			return nil
		},
	}
	for name, get := range getters {
		var observations []quality.FieldObservation
		for _, provider := range identityPriority {
			if ev, ok := lookup(current, provider); ok {
				if v := get(ev); v != nil {
					observations = append(observations, observation(ev, v))
				}
			}
		}
		f := observedFact(observations)
		if len(observations) == 0 {
			f = missingField(in)
		} else {
			first := fmt.Sprint(observations[0].Value)
			for _, o := range observations[1:] {
				if !strings.EqualFold(first, fmt.Sprint(o.Value)) {
					f.State = "conflicting"
					f.Cause = "PROVIDER_FIELD_DISAGREEMENT"
					f.Explanation = "字段来源存在冲突。"
					f.Action = "查看各来源原值，更新来源后重新评估。"
					f.Provider = ""
					break
				}
			}
		}
		facts[name] = f
	}
	_, facts["network_type"] = resolveNetwork(current)
	if facts["network_type"].State == "unknown" {
		facts["network_type"] = missingField(in)
	}
	facts["native"] = missingField(in)
	if ev, ok := lookup(current, SourceIPPure); ok && ev.Native != nil {
		facts["native"] = observedFact([]quality.FieldObservation{observation(ev, *ev.Native)})
	} else {
		for _, provider := range []string{"maxmind_geolite2", "dbip_lite", "ipinfo_lite", "geo_country"} {
			if ev, ok := lookup(current, provider); ok && ev.CountryCode != "" && ev.RegisteredCC != "" {
				f := observedFact([]quality.FieldObservation{observation(ev, strings.EqualFold(ev.CountryCode, ev.RegisteredCC))})
				f.SourceClass = "heuristic"
				f.Explanation = "由位置国家与注册国家比较推断，不是实测原生属性。"
				facts["native"] = f
				break
			}
		}
	}
	return facts
}

func textValue(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func missingField(in Input) quality.Fact {
	f := quality.Missing("vendor_claim", "FIELD_NOT_REPORTED", "刷新支持该字段的来源；没有明确返回值时保持未知。")
	providers := make([]string, 0, len(in.Failed))
	for p := range in.Failed {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		f.Sources = append(f.Sources, p+":unobserved:"+quality.SafeCode(in.Failed[p]))
	}
	if len(in.Failed) > 0 {
		f.Cause = "NO_CURRENT_FIELD_EVIDENCE"
		f.Explanation = "来源无响应或未返回此字段。"
	}
	for _, ev := range independentEvidence(in.Evidence) {
		if !ev.ValidUntil.IsZero() && !in.Now.Before(ev.ValidUntil) {
			f.Sources = append(f.Sources, ev.Provider+":stale")
			if len(in.Failed) == 0 {
				f.State = "stale"
				f.Cause = "EVIDENCE_EXPIRED"
				f.Explanation = "字段证据已过期。"
			}
		}
	}
	return f
}
