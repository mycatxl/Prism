package proxy

import (
	"time"

	"prism/internal/node"
	"prism/internal/routing"
	"prism/internal/safego"
)

// HealthRecorder abstracts passive health feedback reporting.
// topology.GlobalNodePool satisfies this interface.
type HealthRecorder interface {
	RecordResult(hash node.Hash, success bool)
	RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration)
}

type passiveHealthRecorder interface {
	RecordPassiveResult(platformID string, hash node.Hash, success bool)
}

func recordPassiveResultAsync(health HealthRecorder, route routing.RouteResult, success bool) {
	if health == nil {
		return
	}
	// Health recording is bookkeeping on the request path: a panic in it must
	// not take the process down after the response has already been served.
	if recorder, ok := health.(passiveHealthRecorder); ok {
		safego.Go("a passive health result", func() {
			recorder.RecordPassiveResult(route.PlatformID, route.NodeHash, success)
		})
		return
	}
	safego.Go("a health result", func() {
		health.RecordResult(route.NodeHash, success)
	})
}
