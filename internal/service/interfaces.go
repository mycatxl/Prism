// Package service defines service-layer types used by API handlers.
package service

import (
	"context"
	"net/netip"
	"time"

	"prism/internal/node"
	"prism/internal/probe"
	"prism/internal/quality"
)

// SystemInfo contains version and runtime information.
type SystemInfo struct {
	Version   string    `json:"version"`
	GitCommit string    `json:"git_commit"`
	BuildTime string    `json:"build_time"`
	StartedAt time.Time `json:"started_at"`
}

// ProbeManager interface for probe operations.
type ProbeManager interface {
	ProbeEgressSync(hash node.Hash) (*probe.EgressProbeResult, error)
	ProbeLatencySync(hash node.Hash) (*probe.LatencyProbeResult, error)
}

// InspectionManager interface for quality inspection operations.
type InspectionManager interface {
	Snapshot(ip netip.Addr) quality.Summary
	List(query string) []quality.Summary
	Request(ctx context.Context, ip netip.Addr, force bool) (RequestResult, error)
	Status() Status
}

// RequestResult holds the result of an inspection request.
type RequestResult struct {
	Quality  quality.Summary `json:"quality"`
	Action   string          `json:"action"`
	Queued   bool            `json:"queued"`
	Warnings []string        `json:"warnings,omitempty"`
}

// Status holds inspection manager status.
type Status struct {
	Enabled             bool          `json:"enabled"`
	QueueCapacity       int           `json:"queue_capacity"`
	DroppedObservations uint64        `json:"dropped_observations"`
	StorageError        error         `json:"storage_error,omitempty"`
	KnownIPs            int           `json:"known_ips"`
	CheckedIPs          int           `json:"checked_ips"`
	StaleIPs            int           `json:"stale_ips"`
	LowRiskIPs          int           `json:"low_risk_ips"`
	HighRiskIPs         int           `json:"high_risk_ips"`
	Sources             []SourceStatus `json:"sources"`
}

// SourceStatus holds status for an inspection source.
type SourceStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Website    string `json:"website"`
	Configured bool   `json:"configured"`
	Queued     int    `json:"queued"`
	InFlight   int    `json:"in_flight"`
}
