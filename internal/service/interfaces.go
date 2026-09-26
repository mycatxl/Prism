// Package service defines service-layer types used by API handlers.
package service

import (
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
	BuildTags []string  `json:"build_tags"`
	StartedAt time.Time `json:"started_at"`
}

// ProbeManager interface for probe operations.
type ProbeManager interface {
	ProbeEgressSync(hash node.Hash) (*probe.EgressProbeResult, error)
	ProbeLatencySync(hash node.Hash) (*probe.LatencyProbeResult, error)
}

// RequestResult holds the result of a legacy quality request
// (POST /api/v1/quality/ip/{ip}/actions/probe and the two node actions).
//
// The legacy InspectionManager interface and its projection adapter are gone:
// WP08 §8 replaced the in-memory inspection manager with intel.db, and
// internal/service/control_plane_quality.go projects the intel rows directly.
type RequestResult struct {
	Quality  quality.Summary `json:"quality"`
	Action   string          `json:"action"`
	Queued   bool            `json:"queued"`
	Warnings []string        `json:"warnings,omitempty"`
	// JobID is the intel job created by the node actions (WP08 §9). It is empty
	// for the per-IP probe, which enqueues provider lookups instead of a job.
	JobID string `json:"job_id,omitempty"`
}
