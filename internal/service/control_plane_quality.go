package service

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/inspection"
	"prism/internal/node"
	"prism/internal/quality"
)

func inspectionError(err error) error {
	switch {
	case errors.Is(err, quality.ErrUnsupportedIP):
		return invalidArg("quality inspection requires a public IP address")
	case errors.Is(err, quality.ErrDisabled):
		return conflict("quality inspection is disabled")
	case errors.Is(err, quality.ErrQueueFull):
		return conflict("quality inspection queue is full; retry later")
	case errors.Is(err, quality.ErrStorageFull):
		return conflict("quality evidence storage limit reached")
	default:
		return internal("quality inspection storage is unavailable", err)
	}
}

func (s *ControlPlaneService) RequestIPQuality(ctx context.Context, rawIP string) (RequestResult, error) {
	ip, err := netip.ParseAddr(rawIP)
	if err != nil {
		return RequestResult{}, invalidArg("ip: invalid address")
	}
	if s.Inspection == nil {
		return RequestResult{}, inspectionError(quality.ErrDisabled)
	}
	result, err := s.Inspection.Request(ctx, ip, true)
	if err != nil {
		return result, inspectionError(err)
	}
	result.Quality = s.projectQuality(result.Quality)
	return result, nil
}

func (s *ControlPlaneService) projectQuality(summary quality.Summary) quality.Summary {
	if s.TorRegistry != nil && summary.IP != "" {
		if ip, err := netip.ParseAddr(summary.IP); err == nil {
			source := s.TorRegistry.Source(ip)
			hasTor := summary.Evidence != nil && summary.Evidence.Signals.Tor != nil && *summary.Evidence.Signals.Tor
			if hasTor || (source.Evidence != nil && len(source.Evidence.TorRoles) > 0) {
				summary.Sources = append(summary.Sources, source)
			}
		}
	}
	if s.IPPure != nil && summary.IP != "" {
		if ip, err := netip.ParseAddr(summary.IP); err == nil {
			summary.Sources = append(summary.Sources, s.IPPure.Source(ip))
		}
	}
	assessment := quality.Assess(summary, time.Now())
	summary.Assessment = &assessment
	return summary
}

func (s *ControlPlaneService) nodeQuality(ip netip.Addr) quality.Summary {
	summary := quality.Summary{State: "unobserved", Sources: []quality.SourceSummary{}}
	if ip.IsValid() {
		summary.IP = ip.Unmap().String()
	}
	if s.Inspection != nil {
		summary = s.Inspection.Snapshot(ip)
	}
	return s.projectQuality(summary)
}

func (s *ControlPlaneService) ProbeQuality(ctx context.Context, hash string) (RequestResult, error) {
	if s.Inspection == nil || !s.Inspection.Status().Enabled {
		return RequestResult{}, inspectionError(quality.ErrDisabled)
	}
	h, err := node.ParseHex(hash)
	if err != nil {
		return RequestResult{}, invalidArg("node_hash: invalid format")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return RequestResult{}, notFound("node not found")
	}
	ip := entry.GetEgressIP()
	observedAt := entry.LastEgressUpdate.Load()
	if !ip.IsValid() || observedAt == 0 || time.Since(time.Unix(0, observedAt)) > 15*time.Minute {
		if s.ProbeMgr == nil {
			return RequestResult{}, conflict("probe the node egress IP before quality inspection")
		}
		result, err := s.ProbeMgr.ProbeEgressSync(h)
		if err != nil {
			return RequestResult{}, conflict("unable to confirm the node egress IP; check node connectivity")
		}
		ip, err = netip.ParseAddr(result.EgressIP)
		if err != nil {
			return RequestResult{}, conflict("egress probe did not return a valid IP")
		}
	}
	return s.RequestIPQuality(ctx, ip.Unmap().String())
}

func (s *ControlPlaneService) QualityStatus() Status {
	status := Status{Sources: []SourceStatus{}}
	if s.Inspection != nil {
		status = s.Inspection.Status()
	}
	if s.IPPure != nil {
		status.Sources = append(status.Sources, SourceStatus{
			Name:       "IPPure",
			Configured: true,
		})
	}
	if s.TorRegistry != nil {
		status.Sources = append(status.Sources, SourceStatus{
			Name:       "TorRegistry",
			Configured: true,
		})
	}
	return status
}

// ReviewIPPure performs a user-requested check using the selected node only.
// A per-target exit can differ from the cached egress; it is never published
// into the automatic quality store or substituted for the node's egress.
func (s *ControlPlaneService) ReviewIPPure(ctx context.Context, hash string) (*inspection.IPPureReview, error) {
	h, err := node.ParseHex(hash)
	if err != nil {
		return nil, invalidArg("node_hash: invalid format")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return nil, notFound("node not found")
	}
	if s.IPPure == nil {
		return nil, conflict("IPPure review is unavailable")
	}
	outbound := entry.Outbound.Load()
	if outbound == nil {
		return nil, conflict("IPPure review requires a ready node connection")
	}
	expected := entry.GetEgressIP().Unmap()
	result, err := s.IPPure.Check(ctx, *outbound)
	if err != nil {
		return nil, err
	}
	result.NodeHash = hash
	if expected.IsValid() {
		result.ExpectedIP = expected.String()
	}
	current, exists := s.Pool.GetEntry(h)
	result.MatchesNodeIP = exists && current == entry && expected.IsValid() &&
		current.GetEgressIP().Unmap() == expected && result.Evidence.IP == expected.String()
	if result.MatchesNodeIP {
		s.IPPure.Remember(result.Evidence)
	}
	return result, nil
}

func (s *ControlPlaneService) QualityIP(rawIP string) (quality.Summary, error) {
	ip, err := netip.ParseAddr(rawIP)
	if err != nil {
		return quality.Summary{}, invalidArg("ip: invalid address")
	}
	return s.nodeQuality(ip), nil
}

func (s *ControlPlaneService) ListQuality(query string) []quality.Summary {
	byIP := make(map[string]quality.Summary)
	if s.Inspection != nil {
		for _, summary := range s.Inspection.List("") {
			byIP[summary.IP] = summary
		}
	}
	if s.IPPure != nil {
		for _, ip := range s.IPPure.CachedIPs() {
			if _, found := byIP[ip.String()]; !found {
				byIP[ip.String()] = quality.Summary{IP: ip.String(), State: "unobserved", Sources: []quality.SourceSummary{}}
			}
		}
	}
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]quality.Summary, 0, len(byIP))
	for _, summary := range byIP {
		summary = s.projectQuality(summary)
		haystack := summary.IP
		for _, source := range summary.Sources {
			if source.Evidence != nil {
				haystack += " " + source.Evidence.ASN + " " + source.Evidence.Organization
			}
		}
		if query == "" || strings.Contains(strings.ToLower(haystack), query) {
			result = append(result, summary)
		}
	}
	latest := func(summary quality.Summary) time.Time {
		var observed time.Time
		for _, source := range summary.Sources {
			if source.Evidence != nil && source.Evidence.ObservedAt.After(observed) {
				observed = source.Evidence.ObservedAt
			}
		}
		return observed
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := latest(result[i]), latest(result[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return result[i].IP < result[j].IP
	})
	return result
}
