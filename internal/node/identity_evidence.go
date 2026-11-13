package node

import (
	"net/netip"
	"prism/internal/quality"
	"time"
)

type IdentityEvidence struct {
	quality.Fact
	CheckedAt  string `json:"checked_at,omitempty"`
	ValidUntil string `json:"valid_until,omitempty"`
	Family     string `json:"address_family,omitempty"`
	Attempts   int    `json:"attempts"`
}

var identityActions = map[string]string{
	"MEASUREMENT_DISABLED":       "匿名性测量已关闭；不宣称匿名性已验证。管理员可启用 HTTPS echo 测量；已确认泄露仍永久删除。",
	"EGRESS_PROBE_FAILED":        "出口 trace 尚未成功，echo 未执行；先修复节点连接并运行出口探测。",
	"POLICY_UNMANAGED":           "启用节点隐私规则、配置自有资产发现和 HTTPS echo 后运行出口探测。",
	"ENDPOINT_UNCONFIGURED":      "部署独立自有 HTTPS /check，配置可信证书和 echo_url 后运行出口探测。",
	"EGRESS_UNMEASURED":          "修复节点连接并运行出口探测，再完成 echo 测量。",
	"NOT_ATTEMPTED":              "运行出口探测以执行 echo；启用端点后扫描器也会补测。",
	"EGRESS_CHANGED":             "当前出口已变化，重新运行出口探测与 echo。",
	"ENDPOINT_CHANGED":           "echo 配置已变化，重新运行出口探测与 echo。",
	"EXPIRED":                    "测量超过 24 小时，重新运行出口探测与 echo。",
	"ENDPOINT_INVALID":           "检查 echo_url 的 HTTPS 域名、端口和 /check 路径后重测。",
	"OUTBOUND_NOT_READY":         "修复节点配置或缺失的协议引擎，出站就绪后重测。",
	"MARKER_GENERATION_FAILED":   "修复主机随机数服务后重测。",
	"TLS_FAILED":                 "修复端点证书链、域名匹配或主机时间后重测；不关闭验证。",
	"TIMEOUT":                    "检查节点到 echo 的可达性和端点负载，修复后重测。",
	"HTTP_FAILED":                "检查 echo /check 的状态码、访问限制和重定向，修复后重测。",
	"NETWORK_FAILED":             "检查节点连接、DNS 与 echo 可达性，修复后重测。",
	"INVALID_RESPONSE":           "升级或修复自有 echo 响应协议后重测。",
	"CHALLENGE_MISMATCH":         "检查端点实现和缓存，确保返回本次随机 marker 后重测。",
	"HEADER_OBSERVATION_MISSING": "升级自有 echo，使其返回全部约定身份头观察后重测。",
	"MEASUREMENT_FAILED":         "运行出口探测取得带具体失败原因的新 echo 结果。",
}

func (e *NodeEntry) RecordIdentityPrerequisiteFailure() {
	if e.privacySource == nil {
		return
	}
	p := e.privacySource.Load()
	if p == nil || !p.echoEnabled || p.echoURL == "" || e.IdentityStatus() == "measured" {
		return
	}
	e.RecordIdentityMeasurement(e.GetEgressIP(), "measured_failed", p.echoURL, "EGRESS_PROBE_FAILED")
}

// CompleteIdentityMeasurement cannot overwrite a result completed after this request began,
// or an observation of another exit/endpoint. CAS also covers concurrent echoes.
func (e *NodeEntry) CompleteIdentityMeasurement(ip netip.Addr, status, url, cause string, started time.Time) bool {
	for {
		if ip != e.GetEgressIP() {
			return false
		}
		if e.privacySource != nil {
			if p := e.privacySource.Load(); p != nil && p.echoURL != url {
				return false
			}
		}
		old := e.identityCheck.Load()
		if old != nil && old.At.After(started) {
			return false
		}
		attempts := 1
		if old != nil && old.IP == ip && old.EchoURL == url && old.Status != "measured" {
			attempts = min(5, old.Attempts+1)
		}
		if e.identityCheck.CompareAndSwap(old, &IdentityMeasurement{IP: ip, Status: status, EchoURL: url, Cause: cause, At: time.Now(), Attempts: attempts}) {
			return true
		}
	}
}

func (e *NodeEntry) explainPrivacy(d *PrivacyDecision) {
	d.IdentityEvidence = e.IdentityEvidence()
	d.ConfigCheck = quality.Known("static_config")
	if len(e.privacyReasons) > 0 {
		d.ConfigCheck.State = "rejected"
	}
	d.Findings = map[string]quality.Fact{}
	for _, reason := range append(append([]string{}, d.Reasons...), d.Warnings...) {
		class, action := "static_config", "修复上游节点配置；已确认泄露的旧节点哈希不可恢复。"
		switch reason {
		case PrivacyIdentityUnknown:
			d.Findings[reason] = d.IdentityEvidence.Fact
			continue
		case PrivacyUnknown:
			class = "active_probe"
			action = identityActions["EGRESS_UNMEASURED"]
		case PrivacyDiscovery:
			class = "active_probe"
			action = "修复本机公网发现目标、DNS 和路由，补齐自有 VPS/NAT 资产清单后按部署流程重启。"
		case PrivacyOwnAsset:
			class = "active_probe"
			action = "该出口命中自有资产，按隐私规则删除；确认资产清单准确。"
		case PrivacyManual:
			action = "核实人工暂停原因后由管理员从暂停名单移除。"
		case "TLS_VERIFICATION_DISABLED":
			action = "修复上游证书并启用节点 TLS 证书验证后重新导入修正配置。"
		}
		d.Findings[reason] = quality.Missing(class, reason, action)
	}
}

func (e *NodeEntry) IdentityEvidence() IdentityEvidence {
	out := IdentityEvidence{Fact: quality.Known("active_probe")}
	m := e.identityCheck.Load()
	if m != nil {
		out.CheckedAt = m.At.UTC().Format(time.RFC3339Nano)
		out.Attempts = m.Attempts
		if m.IP.Is4() {
			out.Family = "ipv4"
		} else if m.IP.Is6() {
			out.Family = "ipv6"
		}
	}
	var policy *PrivacyPolicy
	if e.privacySource != nil {
		policy = e.privacySource.Load()
	}
	cause := ""
	switch {
	case policy == nil:
		cause = "POLICY_UNMANAGED"
	case !policy.echoEnabled && e.IdentityStatus() != "measured":
		cause = "MEASUREMENT_DISABLED"
	case policy.echoURL == "":
		cause = "ENDPOINT_UNCONFIGURED"
	case !e.GetEgressIP().IsValid():
		cause = "EGRESS_UNMEASURED"
	case m == nil:
		cause = "NOT_ATTEMPTED"
	case m.EchoURL != policy.echoURL:
		cause = "ENDPOINT_CHANGED"
	case m.IP != e.GetEgressIP():
		cause = "EGRESS_CHANGED"
	case time.Since(m.At) >= 24*time.Hour:
		cause = "EXPIRED"
	case m.Status != "measured":
		cause = m.Cause
		if cause == "" {
			cause = "MEASUREMENT_FAILED"
		}
	default:
		out.ValidUntil = m.At.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	if cause != "" {
		action := identityActions[cause]
		if action == "" {
			action = identityActions["MEASUREMENT_FAILED"]
		}
		out.Fact = quality.Missing("active_probe", cause, action)
	}
	return out
}

// IdentityRetryDue expedites at most five attempts per exit/endpoint between ordinary health
// probes. A failed echo never disables health recovery. No endpoint means no
// accelerated network loop. Restart/config/exit changes require fresh evidence.
func (e *NodeEntry) IdentityRetryDue(now time.Time) bool {
	if e.privacySource == nil {
		return false
	}
	p := e.privacySource.Load()
	if p == nil || !p.echoEnabled || p.echoURL == "" {
		return false
	}
	m := e.identityCheck.Load()
	if m == nil || m.IP != e.GetEgressIP() || m.EchoURL != p.echoURL {
		return true
	}
	if m.Status == "measured" {
		return now.Sub(m.At) >= 24*time.Hour
	}
	if m.Attempts >= 5 {
		return false
	}
	return now.Sub(m.At) >= time.Duration(30*(1<<max(0, m.Attempts-1)))*time.Second
}
