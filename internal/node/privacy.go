package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"prism/internal/nodeprivacy"
	"prism/internal/quality"
)

const (
	PrivacyPlaintext       = "PLAINTEXT_TRANSPORT"
	PrivacyIdentity        = "IDENTITY_HEADER_CONFIG"
	PrivacyOwnAsset        = "OWN_ASSET_EGRESS"
	PrivacyUnknown         = "EGRESS_UNMEASURED"
	PrivacyDiscovery       = "OWN_ASSET_DISCOVERY_FAILED"
	PrivacyManual          = "ADMIN_QUARANTINE"
	PrivacyEchoLeak        = "IDENTITY_ECHO_LEAK"
	PrivacyIdentityUnknown = "IDENTITY_UNMEASURED"
)

type PrivacyDecision struct {
	IdentityEvidence IdentityEvidence        `json:"identity_evidence"`
	ConfigCheck      quality.Fact            `json:"config_check"`
	Findings         map[string]quality.Fact `json:"findings"`
	Status           string                  `json:"status"`
	Reasons          []string                `json:"reasons"`
	Warnings         []string                `json:"warnings"`
	IdentityCheck    string                  `json:"identity_check"`
}

// PrivacyDeletionReason includes only mechanically proven violations. Failed
// discovery, health, purity and missing measurements are never delete reasons.
func PrivacyDeletionReason(raw json.RawMessage) string {
	reasons, _ := ClassifyPrivacy(raw)
	for _, reason := range reasons {
		if reason == PrivacyPlaintext || reason == PrivacyIdentity || reason == "DIRECT_EGRESS" {
			return reason
		}
	}
	return ""
}

// PrivacyPolicy is immutable after publication; every node shares its pool's
// atomic pointer, so live leases see a policy change before view rebuilds.
type PrivacyPolicy struct {
	echoURL     string
	echoEnabled bool
	deny        []netip.Prefix
	manual      map[Hash]bool
	ready       bool
}

func NewPrivacyPolicy(c nodeprivacy.Config) (*PrivacyPolicy, error) {
	if err := nodeprivacy.ValidateEchoURL(c.EchoURL); err != nil {
		return nil, err
	}
	if len(c.OwnCIDRs)+len(c.DetectedIPs)+len(c.ActiveIPs) > 4096 || len(c.ManualQuarantine) > 10000 {
		return nil, fmt.Errorf("node_privacy: too many entries")
	}
	p := &PrivacyPolicy{ready: c.ProbeStatus == "ready", manual: make(map[Hash]bool), echoURL: c.EchoURL, echoEnabled: c.EchoEnabled}
	for _, value := range append(append(append([]string{}, c.OwnCIDRs...), c.DetectedIPs...), c.ActiveIPs...) {
		value = strings.TrimSpace(value)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			ip, ipErr := netip.ParseAddr(value)
			if ipErr != nil || ip.Zone() != "" {
				return nil, fmt.Errorf("node_privacy: invalid IP/CIDR")
			}
			ip = ip.Unmap()
			prefix = netip.PrefixFrom(ip, ip.BitLen())
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("node_privacy: mapped IPv4 prefix must be at least /96")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		p.deny = append(p.deny, prefix.Masked())
	}
	for _, value := range c.ManualQuarantine {
		h, err := ParseHex(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("node_privacy: invalid quarantine node hash")
		}
		p.manual[h] = true
	}
	return p, nil
}

func (p *PrivacyPolicy) Denies(ip netip.Addr) bool {
	if p == nil || !ip.IsValid() {
		return false
	}
	for _, prefix := range p.deny {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// SetPrivacyPolicySource is called only before publishing an entry in a pool.
func (e *NodeEntry) SetPrivacyPolicySource(source *atomic.Pointer[PrivacyPolicy]) {
	if e.privacyReasons == nil {
		e.privacyReasons, e.privacyWarnings = ClassifyPrivacy(e.RawOptions)
	}
	e.privacySource = source
}

func (e *NodeEntry) Privacy() (d PrivacyDecision) {
	defer func() { e.explainPrivacy(&d) }()
	d = PrivacyDecision{Status: "accepted", Reasons: []string{}, Warnings: append([]string{}, e.privacyWarnings...), IdentityCheck: "unmeasured"}
	d.IdentityCheck = e.IdentityStatus()
	if e.privacySource == nil {
		d.Status = "unmanaged"
		return d
	}
	p := e.privacySource.Load()
	if p == nil {
		d.Status = "unmanaged"
		return d
	}
	d.Reasons = append(d.Reasons, e.privacyReasons...)
	if !p.ready {
		d.Warnings = append(d.Warnings, PrivacyDiscovery)
	}
	if !e.GetEgressIP().IsValid() {
		d.Reasons = append(d.Reasons, PrivacyUnknown)
	} else if p.Denies(e.GetEgressIP()) {
		d.Reasons = append(d.Reasons, PrivacyOwnAsset)
	}
	if p.manual[e.Hash] {
		d.Reasons = append(d.Reasons, PrivacyManual)
	}
	if p.echoEnabled && d.IdentityCheck != "measured" {
		d.Reasons = append(d.Reasons, PrivacyIdentityUnknown)
	}
	if len(d.Reasons) > 0 {
		d.Status = "quarantined"
	}
	return d
}

// PrivacyAllowed is independent of connectivity health. It is checked again
// at the lease boundary, including sticky hits and same-IP replacement.
func (e *NodeEntry) PrivacyAllowed() bool {
	return e.privacyAllowed(true)
}

// AutomaticProbeAllowed uses the same quarantine gate as capacity and leases,
// except that an unknown egress must be measurable once. Known risk never
// becomes eligible merely because another timer or subscription refresh runs.
func (e *NodeEntry) AutomaticProbeAllowed() bool {
	return e.privacyAllowed(false)
}

// ConfigPrivacyAllowed is the offline boundary, including for manual probes.
// An administrator may remeasure an egress, not bypass a proven config risk.
func (e *NodeEntry) ConfigPrivacyAllowed() bool {
	return e != nil && (e.privacySource == nil || e.privacySource.Load() == nil || len(e.privacyReasons) == 0)
}

func (e *NodeEntry) PrivacyDeniesEgress(ip netip.Addr) bool {
	return e.privacySource != nil && e.privacySource.Load().Denies(ip)
}

// SetProbeEgressIP preserves an owned-egress finding against a stale response
// while removal completes. Deleted hashes cannot be retested or restored.
func (e *NodeEntry) SetProbeEgressIP(ip netip.Addr, adminRetest bool) (netip.Addr, bool) {
	for {
		previous := e.egressIP.Load()
		var old netip.Addr
		if previous != nil {
			old = *previous
		}
		if old == ip || (!adminRetest && e.PrivacyDeniesEgress(old)) {
			return old, false
		}
		if e.egressIP.CompareAndSwap(previous, &ip) {
			return old, true
		}
	}
}

func (e *NodeEntry) privacyAllowed(requireEgress bool) bool {
	if e == nil {
		return false
	}
	if e.privacyContext != nil && e.privacyContext.Err() != nil {
		return false
	}
	if e.privacySource == nil {
		return true
	}
	p := e.privacySource.Load()
	if p == nil {
		return true
	}
	ip := e.GetEgressIP()
	return len(e.privacyReasons) == 0 && (!requireEgress || (ip.IsValid() && (!p.echoEnabled || e.IdentityStatus() == "measured"))) && !p.Denies(ip) && !p.manual[e.Hash]
}

type IdentityMeasurement struct {
	Cause    string
	Attempts int
	EchoURL  string
	IP       netip.Addr
	Status   string
	At       time.Time
}

// BindPrivacyContext cancels network work already using this entry when a
// measured violation deletes it. Call the returned cleanup after the request.
func (e *NodeEntry) BindPrivacyContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if e.privacyContext == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(e.privacyContext, cancel)
	if e.privacyContext.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
func (e *NodeEntry) CancelPrivacyWork() {
	if e == nil {
		return
	}
	e.privacyConnMu.Lock()
	e.privacyClosed = true
	conns := make([]io.Closer, 0, len(e.privacyConns))
	for c := range e.privacyConns {
		conns = append(conns, c)
	}
	e.privacyConns = nil
	e.privacyConnMu.Unlock()
	if e.privacyCancel != nil {
		e.privacyCancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

func (e *NodeEntry) SetIdentityMeasurement(ip netip.Addr, status string) {
	url := ""
	if e.privacySource != nil {
		if p := e.privacySource.Load(); p != nil {
			url = p.echoURL
		}
	}
	e.SetIdentityMeasurementFromEcho(ip, status, url)
}

// SetIdentityMeasurementFromEcho stores the endpoint captured before the request.
func (e *NodeEntry) SetIdentityMeasurementFromEcho(ip netip.Addr, status, url string) {
	e.RecordIdentityMeasurement(ip, status, url, "")
}
func (e *NodeEntry) RecordIdentityMeasurement(ip netip.Addr, status, url, cause string) {
	attempts := 1
	if old := e.identityCheck.Load(); old != nil && old.IP == ip && old.EchoURL == url && old.Status != "measured" {
		attempts = min(5, old.Attempts+1)
	}
	e.identityCheck.Store(&IdentityMeasurement{IP: ip, Status: status, At: time.Now(), EchoURL: url, Cause: cause, Attempts: attempts})
}
func (e *NodeEntry) IdentityStatus() string {
	m := e.identityCheck.Load()
	if m == nil || m.IP != e.GetEgressIP() || time.Since(m.At) > 24*time.Hour {
		return "unmeasured"
	}
	if e.privacySource != nil {
		if p := e.privacySource.Load(); p != nil && p.echoURL != m.EchoURL {
			return "unmeasured"
		}
	}
	return m.Status
}

// ClassifyPrivacy inspects normalized documents, including every chain leg.
// TLS absence alone is not plaintext: SS/VMess and VPNs encrypt independently.
func ClassifyPrivacy(raw json.RawMessage) (reasons, warnings []string) {
	reasons = []string{}
	warnings = []string{}
	add := func(dst *[]string, s string) {
		for _, v := range *dst {
			if v == s {
				return
			}
		}
		*dst = append(*dst, s)
	}
	doc, err := ParseNodeDoc(raw)
	if err != nil {
		return []string{"INVALID_NODE_DOCUMENT"}, warnings
	}
	for _, leg := range append(append([]json.RawMessage{}, doc.Deps...), doc.Main) {
		var o map[string]any
		if json.Unmarshal(leg, &o) != nil {
			add(&reasons, "INVALID_NODE_DOCUMENT")
			continue
		}
		t, _ := o["type"].(string)
		t = strings.ToLower(t)
		// Direct never becomes an encrypted upstream through a stray TLS field.
		if t == "direct" {
			add(&reasons, "DIRECT_EGRESS")
		}
		tls, _ := o["tls"].(map[string]any)
		encrypted := tls["enabled"] == true || o["tls"] == true
		if doc.Engine == EngineMihomo && t == "trojan" {
			encrypted = true
		} // engine requires TLS
		if tls["insecure"] == true || o["skip-cert-verify"] == true {
			add(&warnings, "TLS_VERIFICATION_DISABLED")
		}
		if !encrypted {
			switch t {
			case "http", "socks", "socks5", "vless", "trojan":
				add(&reasons, PrivacyPlaintext)
			case "vmess":
				if o["security"] == "none" || o["security"] == "zero" || o["cipher"] == "none" {
					add(&reasons, PrivacyPlaintext)
				}
			case "shadowsocks", "ss":
				if o["method"] == "none" || o["method"] == "plain" || o["cipher"] == "none" {
					add(&reasons, PrivacyPlaintext)
				}
			}
		}
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for k, v := range x {
					lower := strings.ToLower(k)
					if lower == "forwarded" || lower == "via" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-real-ip" || lower == "client-ip" || lower == "x-client-ip" || lower == "true-client-ip" || lower == "cf-connecting-ip" || lower == "x-proxyuser-ip" {
						add(&reasons, PrivacyIdentity)
					}
					walk(v)
				}
			case []any:
				for _, v := range x {
					walk(v)
				}
			}
		}
		walk(o)
	}
	return reasons, warnings
}
