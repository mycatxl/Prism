package intel

import (
	"context"
	"prism/internal/intel/jobs"
)

// EnsureInitialMeasurements is used by production startup/import. The existing
// intel_auto_checks switch still controls recurring checks; each enabled node
// receives one full initial attempt, including nodes missed by older versions.
func (s *Service) EnsureInitialMeasurements(ctx context.Context, hashes []string) (int, error) {
	if !s.manager.Enabled() {
		return 0, nil
	}
	s.initialMu.Lock()
	defer s.initialMu.Unlock()
	missing, err := s.store.MissingFullMeasurements(ctx, hashes)
	if err != nil {
		return 0, err
	}
	count := 0
	for start := 0; start < len(missing); start += 1000 {
		batch := missing[start:min(start+1000, len(missing))]
		if _, err := s.manager.Create(ctx, jobs.Request{Kind: jobs.KindFull, Scope: jobs.Scope{NodeHashes: batch}}, "initial_measurement", jobs.PrioritySubscription); err != nil {
			return count, err
		}
		count += len(batch)
	}
	return count, nil
}
