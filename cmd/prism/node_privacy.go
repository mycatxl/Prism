package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/state"
)

// discoverOwnEgress deliberately bypasses HTTP(S)_PROXY and Prism outbounds.
// A bounded response and a non-redirecting client keep discovery predictable.
func discoverOwnEgress(ctx context.Context, target, network string) (netip.Addr, error) {
	if target == "" {
		target = config.DefaultSelfProbeURL
	}
	if err := config.ValidateSelfProbeURL(target); err != nil {
		return netip.Addr{}, err
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("discovery HTTP status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 513))
	if err != nil || len(b) > 512 {
		return netip.Addr{}, fmt.Errorf("invalid discovery response")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("discovery did not return a public IP")
	}
	ip = ip.Unmap()
	if network == "tcp4" && !ip.Is4() || network == "tcp6" && !ip.Is6() {
		return netip.Addr{}, fmt.Errorf("discovery returned the wrong address family")
	}
	return ip.Unmap(), nil
}

// Called on every binary start, before any listener or background work starts.
// Discovery failure is visible as incomplete coverage; known asset denials stay
// active. A failed persistence write aborts rather than forgetting a found asset.
func initializeNodePrivacy(engine *state.StateEngine, env *config.EnvConfig) error {
	cfg := loadRuntimeConfig(engine, env)
	_, version, err := engine.GetSystemConfig()
	if err != nil {
		return err
	}
	p := cfg.NodePrivacy.Clone()
	if err := config.ValidateSelfProbeURL(p.SelfProbeURL); err != nil {
		return err
	}
	if p.SelfProbeURL == "" {
		p.SelfProbeURL = config.DefaultSelfProbeURL
	}
	p.ProbeStatus = "failed"
	p.ActiveIPs = []string{}
	add := func(ip netip.Addr) {
		if ip.IsValid() && !slices.Contains(p.ActiveIPs, ip.String()) {
			p.ActiveIPs = append(p.ActiveIPs, ip.String())
		}
		if ip.IsValid() && !slices.Contains(p.DetectedIPs, ip.String()) {
			p.DetectedIPs = append(p.DetectedIPs, ip.String())
		}
	}
	// Include all local public interface addresses, including IPv6; private
	// addresses behind NAT are not evidence of the public exit.
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		ip := prefix.Addr().Unmap()
		if ip.IsGlobalUnicast() && !ip.IsPrivate() {
			add(ip)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A connected UDP socket sends no packet but asks the kernel whether an
	// address family has a route. This also finds NAT66 behind a private ULA.
	// Every available family must pass discovery; IPv6-only hosts are valid.
	available, succeeded := 0, 0
	for _, family := range []struct{ network, target string }{{"udp4", "192.0.2.1:9"}, {"udp6", "[2001:db8::1]:9"}} {
		conn, err := net.Dial(family.network, family.target)
		if err != nil {
			continue
		}
		_ = conn.Close()
		available++
		ip, err := discoverOwnEgress(ctx, p.SelfProbeURL, strings.Replace(family.network, "udp", "tcp", 1))
		if err == nil {
			add(ip)
			succeeded++
		}
	}
	if available > 0 && available == succeeded {
		p.ProbeStatus = "ready"
	}
	p.ProbedAt = time.Now().UTC().Format(time.RFC3339Nano)
	cfg.NodePrivacy = p
	if _, err := node.NewPrivacyPolicy(p); err != nil {
		return err
	}
	if err := engine.SaveSystemConfig(cfg, version+1, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("persist node privacy discovery: %w", err)
	}
	log.Printf("Node privacy: own-asset discovery %s; %d persisted addresses", p.ProbeStatus, len(p.DetectedIPs))
	return nil
}
