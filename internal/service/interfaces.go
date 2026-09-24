// Package service defines service-layer types used by API handlers.
package service

import (
	"context"
	"net/netip"
	"time"

	"prism/internal/inspection"
	"prism/internal/node"
	"prism/internal/probe"
	"prism/internal/quality"
)

// SystemInfo contains version and runtime information.
type SystemInfo struct {
	Version   string    `json:"version"`
	GitCommit string    `json:"git_commit"`
	BuildTime string    `json:"build_time"`
	BuildTags []string  `json:"build_tags"`
	StartedAt time.Time `json:"started_at"`
}

// ProbeManager interface for probe operations.
type ProbeManager interface {
	ProbeEgressSync(hash node.Hash) (*probe.EgressProbeResult, error)
	ProbeLatencySync(hash node.Hash) (*probe.LatencyProbeResult, error)
}

// InspectionManager interface for quality inspection operations.
//
// *inspection.Manager satisfies it directly: the lossy adapter that used to
// project inspection.Status onto a partial service.Status is gone, so the
// /api/v1/quality/status wire shape stays identical to the WebUI contract.
type InspectionManager interface {
	Snapshot(ip netip.Addr) quality.Summary
	List(query string) []quality.Summary
	Request(ctx context.Context, ip netip.Addr, force bool) (inspection.RequestResult, error)
	Status() inspection.Status
}

// WP08 wires the intel-backed inspection manager; the concrete manager must
// satisfy this interface without a lossy projection adapter.
var _ InspectionManager = (*inspection.Manager)(nil)

// RequestResult holds the result of an inspection request.
type RequestResult struct {
	Quality  quality.Summary `json:"quality"`
	Action   string          `json:"action"`
	Queued   bool            `json:"queued"`
	Warnings []string        `json:"warnings,omitempty"`
}
