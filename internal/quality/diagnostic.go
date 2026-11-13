package quality

import "strings"

// SafeCode keeps only machine codes, never arbitrary upstream error text.
func SafeCode(code string) string {
	if len(code) == 0 || len(code) > 80 {
		return ""
	}
	for _, c := range code {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return ""
		}
	}
	return code
}

// PurityDiagnostic is the shared API/UI/export explanation for an absent score.
func PurityDiagnostic(code string) Fact {
	messages := map[string][2]string{
		"NO_EGRESS":                     {"尚未测得实际出口 IP。", "先运行出口探测，再运行完整检测。"},
		"CURRENT_EXIT_UNASSESSED":       {"当前出口尚未完成评估。", "为当前出口运行完整检测。"},
		"CURRENT_EXIT_CHANGED":          {"实际出口已变化，旧出口证据不再适用。", "为新的实际出口重新运行完整检测。"},
		"MEASUREMENT_QUEUED":            {"检测任务正在排队。", "等待任务执行，并查看任务进度。"},
		"MEASUREMENT_RUNNING":           {"检测任务正在执行。", "等待任务结束，再查看来源结果。"},
		"MEASUREMENT_FAILED":            {"检测已失败并结束。", "根据具体失败码修复连接或来源后重新检测。"},
		"EVIDENCE_EXPIRED":              {"评分证据已过期。", "重新运行检测以取得当前出口的有效证据。"},
		"IPPURE_NOT_RUN":                {"评分来源尚未返回当前出口的检测结果。", "运行完整检测或通过节点重新查询评分来源。"},
		"IPPURE_PROVIDER_UNAVAILABLE":   {"评分来源不可用或已停用。", "检查来源设置和节点连接后重新检测。"},
		"IPPURE_TIMEOUT":                {"评分来源请求超时。", "检查节点连接，稍后重新检测。"},
		"IPPURE_RATE_LIMITED":           {"评分来源限制了此次请求。", "等待来源限流解除后重新检测。"},
		"IPPURE_AUTH_FAILED":            {"评分来源拒绝认证或访问。", "检查来源授权与访问条件后重新检测。"},
		"IPPURE_EGRESS_MISMATCH":        {"来源返回的 IP 与当前实际出口不匹配。", "重新探测出口并检测；检查节点是否按目的地切换出口。"},
		"IPPURE_INVALID_RESPONSE":       {"评分来源响应或证据字段不合法。", "检查来源响应合同，修复后重新检测。"},
		"IPPURE_NO_FRAUD_SCORE":         {"评分来源未返回 fraudScore。", "重新检测；来源仍不提供时保持未评分。"},
		"IPPURE_IPV6_SCORE_UNAVAILABLE": {"评分来源暂不提供 IPv6 fraudScore。", "保持未评分；如节点支持 IPv4，先实际测得 IPv4 出口再检测。"},
		"NO_VALID_IPPURE_EVIDENCE":      {"没有有效的评分来源证据。", "查看来源状态及具体失败码后重新检测。"},
		"EVIDENCE_READ_FAILED":          {"证据存储读取失败。", "恢复存储读取后重新评估，期间不放行。"},
		"ASSESSMENT_WRITE_FAILED":       {"评估结果写入失败。", "恢复存储写入后重新评估，期间不放行。"},
		"MEASUREMENT_STATE_READ_FAILED": {"检测状态读取失败。", "恢复存储读取后刷新检测状态。"},
		"EGRESS_READ_FAILED":            {"出口记录读取失败。", "恢复存储读取后重新探测出口。"},
		"ASSESSMENT_PROFILE_OBSOLETE":   {"旧评估口径已失效。", "按当前评分规则重新评估已保存的证据。"},
	}
	pair, ok := messages[code]
	if !ok {
		code = "NO_VALID_IPPURE_EVIDENCE"
		pair = messages[code]
	}
	f := Missing("derived", code, pair[1])
	f.Explanation = pair[0]
	if strings.HasPrefix(code, "IPPURE_") || code == "NO_VALID_IPPURE_EVIDENCE" || code == "EVIDENCE_EXPIRED" {
		f.Provider = "ippure"
		f.Sources = []string{"ippure"}
	}
	if code == "MEASUREMENT_QUEUED" {
		f.State = "queued"
	}
	if code == "MEASUREMENT_RUNNING" {
		f.State = "running"
	}
	if code == "EVIDENCE_EXPIRED" {
		f.State = "stale"
	}
	return f
}
