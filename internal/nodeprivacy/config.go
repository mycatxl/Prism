// Package nodeprivacy defines node privacy settings and controlled echo measurements.
package nodeprivacy

import (
	"fmt"
	"net/netip"
	"net/url"
)

// Config is persisted with runtime settings. Discovered addresses
// are additive across restarts: a changed NAT must not silently release an old
// operator asset. Only the administrator can remove a mistaken historical IP.
type Config struct {
	OwnCIDRs         []string `json:"own_cidrs"`
	DetectedIPs      []string `json:"detected_ips"`
	ActiveIPs        []string `json:"active_ips"`
	ManualQuarantine []string `json:"manual_quarantine"`
	SelfProbeURL     string   `json:"self_probe_url"`
	EchoURL          string   `json:"echo_url"`
	EchoEnabled      bool     `json:"echo_enabled"`
	ProbeStatus      string   `json:"probe_status"`
	ProbedAt         string   `json:"probed_at"`
}

const DefaultSelfProbeURL = "https://api64.ipify.org"

// ValidateSelfProbeURL permits HTTPS or loopback HTTP for offline fixtures.
func ValidateSelfProbeURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("node_privacy.self_probe_url: invalid URL")
	}
	ip, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || !ip.IsLoopback()) {
		return fmt.Errorf("node_privacy.self_probe_url: requires HTTPS or loopback HTTP")
	}
	return nil
}

func (c Config) Clone() Config {
	c.OwnCIDRs = append([]string{}, c.OwnCIDRs...)
	c.DetectedIPs = append([]string{}, c.DetectedIPs...)
	c.ActiveIPs = append([]string{}, c.ActiveIPs...)
	c.ManualQuarantine = append([]string{}, c.ManualQuarantine...)
	return c
}
