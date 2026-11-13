package service

import (
	"prism/internal/intel/assess"
	"prism/internal/quality"
	"strings"
)

func qualityStorageError(code string, err error) *ServiceError {
	fact := quality.PurityDiagnostic(code)
	return &ServiceError{Code: code, Message: fact.Explanation + " " + fact.Action, Err: err}
}

// Refresh coverage after every enrichment stage, including failed DB reads.
// It is safe on a completely empty view: no missing fact becomes favorable.
func (n *NodeIntel) explainFacts() {
	facts := map[string]quality.Fact{}
	hasExit := n.EgressIPv4 != "" || n.EgressIPv6 != ""
	missing := func(class string) quality.Fact {
		if n.evidenceReadFailed && class != "active_probe" {
			return quality.Missing(class, "EVIDENCE_READ_FAILED", "恢复证据存储读取后刷新；读取失败不能解释为供应商未提供。")
		}
		if n.measurementReadFailed {
			return quality.Missing(class, "MEASUREMENT_STATE_READ_FAILED", "恢复检测状态存储读取后刷新，以确定任务是否完成或失败。")
		}
		if n.egressReadFailed && !hasExit {
			return quality.Missing(class, "EGRESS_READ_FAILED", "恢复出口记录读取后刷新；不能把读取失败当作未运行探测。")
		}
		if n.MeasurementState == "measured_failed" {
			cause := n.MeasurementError
			if cause == "" {
				cause = "MEASUREMENT_FAILED"
			}
			return quality.Missing(class, cause, "在检测任务中处理该错误（连接、证书、认证或来源额度），然后手动重新检测；不会无限等待。")
		}
		if !hasExit {
			return quality.Missing(class, "NO_EGRESS", "修复节点连接后运行出口探测，再运行完整检测。")
		}
		if n.MeasurementState == "queued" || n.MeasurementState == "running" {
			f := quality.Missing(class, "MEASUREMENT_"+strings.ToUpper(n.MeasurementState), "查看检测任务；有界重试结束后按最终错误修复并重测。")
			f.State = n.MeasurementState
			return f
		}
		if n.State == IntelStateStale {
			return quality.Missing(class, "EVIDENCE_EXPIRED", "重新检测当前出口，取得未过期证据。")
		}
		if n.State == IntelStateUnassessed {
			return quality.Missing(class, "CURRENT_EXIT_UNASSESSED", "为当前出口运行完整检测；若已关闭自动检测，请手动安排。")
		}
		for _, reason := range n.Reasons {
			switch reason {
			case assess.ReasonAllSourcesFailed:
				return quality.Missing(class, reason, "按来源诊断修复失败的认证、额度、响应或出口匹配后重新检测；失败不能算作干净。")
			case assess.ReasonNoScoringSource:
				return quality.Missing(class, reason, "启用能提供真实评分证据的来源并完成检测后重算。")
			case assess.ReasonNoValidEvidence:
				return quality.Missing(class, reason, "检查来源错误和有效期，为当前出口取得真实有效的评分证据后重算。")
			}
		}
		if n.State == IntelStatePending || n.State == IntelStateUnsupported {
			return quality.Missing(class, "ASSESSMENT_DIAGNOSTIC_MISSING", "刷新评估与来源诊断；当前记录未说明失败原因，不能声称正在排队。")
		}
		return quality.Missing(class, "FIELD_NOT_REPORTED", "刷新支持此字段的来源；若供应商仍不提供，则撤下这项产品承诺，保留缺失原因。")
	}
	put := func(name, class string, known bool) {
		if known && n.State == IntelStateStale && class != "active_probe" {
			f := quality.Missing(class, "EVIDENCE_EXPIRED", "仅保留历史值；重新检测当前出口取得未过期证据后恢复使用。")
			f.State = "stale"
			facts[name] = f
		} else if known {
			facts[name] = quality.Known(class)
		} else {
			facts[name] = missing(class)
		}
	}
	put("purity", "derived", n.PurityScore != nil)
	if n.PurityScore == nil && hasExit && n.State == IntelStateValid && facts["purity"].Cause == "FIELD_NOT_REPORTED" {
		facts["purity"] = quality.Missing("derived", "NO_VALID_SCORE_COMPONENT", "刷新提供风险数值或完整评分信号的来源后重算；仅身份资料不能评分。")
	}
	put("network_type", "vendor_claim", n.IPType != "" && n.IPType != "unknown" && n.IPType != "conflicting")
	if n.IPType == "conflicting" && n.State != IntelStateStale {
		f := quality.Missing("vendor_claim", "PROVIDER_TYPE_DISAGREEMENT", "查看来源分类，刷新冲突供应商；更正或过期的旧证据被替换后自动重算。未解决前不承诺住宅或机房类型。")
		f.State = "conflicting"
		facts["network_type"] = f
	}
	if n.IPType == "conflicting" && n.State == IntelStateStale {
		f := quality.Missing("vendor_claim", "EVIDENCE_EXPIRED", "刷新过期的分类来源后重新判定分歧；历史冲突不能当作当前结论。")
		f.State = "stale"
		facts["network_type"] = f
	}
	put("egress_ipv4", "active_probe", n.EgressIPv4 != "")
	put("egress_ipv6", "active_probe", n.EgressIPv6 != "")
	for _, f := range []string{"egress_ipv4", "egress_ipv6"} {
		if facts[f].State != "known" && n.MeasurementState != "measured_failed" && !n.measurementReadFailed {
			facts[f] = quality.Missing("active_probe", "ADDRESS_FAMILY_NOT_OBSERVED", "使用该地址族可达的出口探测目标复测；未观测到不能宣称不支持，也不能承诺双栈。")
			if n.egressReadFailed {
				facts[f] = quality.Missing("active_probe", "EGRESS_READ_FAILED", "恢复出口记录读取后刷新该地址族结果。")
			}
		}
	}
	put("colo", "active_probe", n.Colo != "")
	if n.Colo == "" && n.MeasurementState != "measured_failed" && !n.measurementReadFailed {
		facts["colo"] = quality.Missing("active_probe", "TRACE_COLO_NOT_OBSERVED", "运行出口 trace 探测；仅展示响应中实际返回的接入点。")
		if n.egressReadFailed {
			facts["colo"] = quality.Missing("active_probe", "EGRESS_READ_FAILED", "恢复出口记录读取后刷新接入点结果。")
		}
	}
	put("asn", "vendor_claim", n.ASN > 0)
	put("as_org", "vendor_claim", n.ASOrg != "")
	put("country", "vendor_claim", n.Country != "")
	put("city", "vendor_claim", n.City != "")
	put("native", "vendor_claim", n.Native != nil)
	for _, r := range n.Reasons {
		if r == "NATIVE_HEURISTIC" {
			f := facts["native"]
			f.SourceClass = "derived"
			facts["native"] = f
		}
		if r == "ASN_HOSTING_HEURISTIC" {
			f := facts["network_type"]
			f.SourceClass = "derived"
			facts["network_type"] = f
		}
	}
	for key, value := range map[string]*bool{"proxy": n.Signals.Proxy, "vpn": n.Signals.VPN, "tor": n.Signals.Tor, "hosting": n.Signals.Hosting, "compromised": n.Signals.Compromised, "scraper": n.Signals.Scraper, "anonymous": n.Signals.Anonymous} {
		put(key, "vendor_claim", value != nil)
	}
	put("confidence", "derived", n.Confidence != "" && n.Confidence != "none")
	put("assessed_at", "derived", n.AssessedAt != "")
	put("verdict", "derived", n.Verdict != "" && n.Verdict != "pending" && n.Verdict != "incomplete" && n.Verdict != "conflicting")
	if n.Verdict == "incomplete" {
		facts["verdict"] = quality.Missing("derived", "INCOMPLETE_EVIDENCE", "补齐缺失的供应商风险旗标和评分覆盖后重算；当前分数仅描述已返回的证据。")
	}
	if n.Verdict == "conflicting" {
		facts["verdict"] = facts["network_type"]
	}
	put("checks", "active_probe", len(n.Checks) > 0)
	if len(n.Checks) == 0 {
		facts["checks"] = quality.Missing("active_probe", "NO_CURRENT_DESTINATION_CHECK", "运行完整检测或所需目的地检查；仅对成功探测的目的地和有效期作承诺。")
	}
	for id, outcome := range n.Checks {
		if outcome == "unknown" || outcome == "error" {
			facts["check:"+id] = quality.Missing("active_probe", "DESTINATION_CHECK_"+strings.ToUpper(outcome), "查看该目的地检查的响应诊断，修复访问或检测规则后重测。")
		}
	}
	// Preserve sanitized provider provenance assembled by enrichment.
	for key, old := range n.Facts {
		if key != "purity" && (old.Provider != "" || len(old.Observations) > 0 || old.Explanation != "") && !n.evidenceReadFailed {
			facts[key] = old
			continue
		}
		if len(old.Sources) > 0 {
			f := facts[key]
			f.Sources = old.Sources
			if f.Cause == "FIELD_NOT_REPORTED" {
				for _, source := range old.Sources {
					if strings.Contains(source, ":unobserved:") || strings.Contains(source, ":unsupported:") || strings.Contains(source, ":stale") {
						f.Cause = "NO_CURRENT_FIELD_EVIDENCE"
						f.Action = "按下列逐来源状态修复出口匹配、响应、认证或额度错误，刷新过期来源；成功来源仍未提供此字段时保留未知。"
						break
					}
				}
			}
			facts[key] = f
		}
	}
	facts["purity"] = n.purityFact(hasExit)
	if n.PurityScore == nil {
		n.PurityBand = "unknown"
		if n.Verdict == "favorable" {
			n.Verdict = "pending"
		}
	}
	n.Facts = facts
}

func (n *NodeIntel) purityFact(hasExit bool) quality.Fact {
	old := n.Facts["purity"]
	missing := func(code string) quality.Fact { return quality.PurityDiagnostic(code) }
	if n.evidenceReadFailed {
		n.PurityScore = nil
		return missing("EVIDENCE_READ_FAILED")
	}
	if !hasExit {
		n.PurityScore = nil
		if n.egressReadFailed {
			return missing("EGRESS_READ_FAILED")
		}
		if n.MeasurementState == "measured_failed" {
			f := missing("MEASUREMENT_FAILED")
			f.ErrorCode = quality.SafeCode(n.MeasurementError)
			return f
		}
		return missing("NO_EGRESS")
	}
	if n.MeasurementState == "queued" || n.MeasurementState == "running" {
		n.PurityScore = nil
		return missing("MEASUREMENT_" + strings.ToUpper(n.MeasurementState))
	}
	if n.State == IntelStateStale {
		n.PurityScore = nil
		f := missing("EVIDENCE_EXPIRED")
		f.ObservedAt = old.ObservedAt
		f.ValidUntil = old.ValidUntil
		f.FraudScore = old.FraudScore
		return f
	}
	if n.PurityScore != nil {
		if old.State == "known" {
			return old
		}
		f := quality.Known("derived")
		f.Provider = "ippure"
		f.Explanation = "纯净度 = 100 − fraudScore。"
		return f
	}
	if n.measurementReadFailed {
		return missing("MEASUREMENT_STATE_READ_FAILED")
	}
	// Specific source failures survive generic job failure states.
	if old.Cause != "" && old.Cause != "IPPURE_NOT_RUN" && old.Cause != "CURRENT_EXIT_UNASSESSED" && old.Cause != "NO_EGRESS" && !strings.HasPrefix(old.Cause, "MEASUREMENT_") {
		return old
	}
	for _, reason := range n.Reasons {
		if strings.HasPrefix(reason, "IPPURE_") && !strings.HasPrefix(reason, "IPPURE_ERROR:") || reason == "NO_VALID_IPPURE_EVIDENCE" || reason == "EVIDENCE_EXPIRED" || reason == "ASSESSMENT_PROFILE_OBSOLETE" || reason == "EVIDENCE_READ_FAILED" || reason == "ASSESSMENT_WRITE_FAILED" {
			f := missing(reason)
			for _, r := range n.Reasons {
				if strings.HasPrefix(r, "IPPURE_ERROR:") {
					f.ErrorCode = quality.SafeCode(strings.TrimPrefix(r, "IPPURE_ERROR:"))
				}
			}
			if reason != "IPPURE_NOT_RUN" {
				return f
			}
			old = f
		}
	}
	if n.currentExitChanged {
		return missing("CURRENT_EXIT_CHANGED")
	}
	if n.measurementReadFailed {
		return missing("MEASUREMENT_STATE_READ_FAILED")
	}
	if n.MeasurementState == "queued" || n.MeasurementState == "running" {
		return missing("MEASUREMENT_" + strings.ToUpper(n.MeasurementState))
	}
	if n.MeasurementState == "measured_failed" {
		f := missing("MEASUREMENT_FAILED")
		f.ErrorCode = quality.SafeCode(n.MeasurementError)
		return f
	}
	if old.Cause == "IPPURE_NOT_RUN" {
		return old
	}
	if n.State == IntelStateUnassessed {
		return missing("CURRENT_EXIT_UNASSESSED")
	}
	return missing("NO_VALID_IPPURE_EVIDENCE")
}
