package service

import (
	"context"
	"net/netip"

	"prism/internal/inspection"
	"prism/internal/quality"
)

// inspectionAdapter wraps inspection.Manager to adapt its RequestResult to service.RequestResult.
type inspectionAdapter struct {
	mgr *inspection.Manager
}

// NewInspectionAdapter creates a new adapter wrapping an inspection.Manager.
func NewInspectionAdapter(mgr *inspection.Manager) InspectionManager {
	return &inspectionAdapter{mgr: mgr}
}

func (a *inspectionAdapter) Snapshot(ip netip.Addr) quality.Summary {
	return a.mgr.Snapshot(ip)
}

func (a *inspectionAdapter) List(query string) []quality.Summary {
	return a.mgr.List(query)
}

func (a *inspectionAdapter) Request(ctx context.Context, ip netip.Addr, force bool) (RequestResult, error) {
	result, err := a.mgr.Request(ctx, ip, force)
	return RequestResult{
		Quality:  result.Quality,
		Action:   "",
		Queued:   result.Queued,
		Warnings: result.Warnings,
	}, err
}

func (a *inspectionAdapter) Status() Status {
	inspStatus := a.mgr.Status()
	sources := make([]SourceStatus, len(inspStatus.Sources))
	for i, s := range inspStatus.Sources {
		sources[i] = SourceStatus{
			ID:         s.ID,
			Name:       s.Name,
			Website:    s.Website,
			Configured: s.Configured,
			Queued:     s.Queued,
			InFlight:   s.Running,
		}
	}
	return Status{
		Enabled:             inspStatus.Enabled,
		QueueCapacity:       inspStatus.QueueCapacity,
		DroppedObservations: inspStatus.DroppedObservations,
		KnownIPs:            inspStatus.KnownIPs,
		CheckedIPs:          inspStatus.CheckedIPs,
		StaleIPs:            inspStatus.StaleIPs,
		LowRiskIPs:          inspStatus.LowRiskIPs,
		HighRiskIPs:         inspStatus.HighRiskIPs,
		Sources:             sources,
	}
}
