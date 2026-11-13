package intel

import (
	"context"
	"net/netip"
	"time"

	"prism/internal/intel/store"
	"prism/internal/quality"
)

func (s *Service) syncMeasurements(hashes []string) {
	s.measurementMu.Lock()
	defer s.measurementMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	states, err := s.store.LatestMeasurementStates(ctx, hashes)
	s.snapshot.mu.Lock()
	for _, h := range hashes {
		if s.snapshot.deletedNodes[h] {
			continue
		}
		if err != nil {
			s.snapshot.measurements[h] = store.MeasurementStatus{State: "read_failed"}
			continue
		}
		if m, ok := states[h]; ok {
			s.snapshot.measurements[h] = m
		}
	}
	s.snapshot.mu.Unlock()
	if s.assessor.onChange != nil {
		egress, err := s.store.NodeEgressByHashes(ctx, hashes)
		if err != nil {
			return
		}
		var ips []netip.Addr
		for _, row := range egress {
			ips = append(ips, egressAddresses(row)...)
		}
		if len(ips) > 0 {
			s.assessor.onChange(ips)
		}
	}
}

func (s *Snapshot) Measurement(hash string) store.MeasurementStatus {
	if s == nil {
		return store.MeasurementStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.deletedNodes[hash] {
		return store.MeasurementStatus{}
	}
	m := s.measurements[hash]
	if m.State == store.ItemFailed {
		m.State = "measured_failed"
	}
	if m.State == store.ItemDone {
		m.State = "complete"
	}
	return m
}

// NodeAssessment applies the durable job state without a database read on the
// routing path. IP evidence remains reusable only after this node's work ends.
func (s *Snapshot) NodeAssessment(hash string, ip netip.Addr) (AssessmentLite, bool) {
	lite, ok := s.Assessment(ip)
	now := time.Now().UTC()
	lite = lite.Current(now)
	if s == nil {
		return lite, ok
	}
	s.mu.RLock()
	m := s.measurements[hash]
	deleted := s.deletedNodes[hash]
	s.mu.RUnlock()
	if deleted {
		return AssessmentLite{}, false
	}
	code := ""
	switch m.State {
	case "queued":
		code = "MEASUREMENT_QUEUED"
	case "running":
		code = "MEASUREMENT_RUNNING"
	case "failed", "measured_failed", "canceled":
		code = "MEASUREMENT_FAILED"
	case "read_failed":
		code = "MEASUREMENT_STATE_READ_FAILED"
	}
	if code == "" {
		return lite, ok
	}
	facts := lite.FactValues()
	// A specific primary failure remains useful after the job fails.
	if code == "MEASUREMENT_FAILED" && facts["purity"].ErrorCode != "" && lite.ScoreValue() == nil {
		return lite, ok
	}
	fact := quality.PurityDiagnostic(code)
	fact.ErrorCode = quality.SafeCode(m.Error)
	facts["purity"] = fact
	lite.Score, lite.Band, lite.State, lite.Verdict = -1, BandUnknown, StatePending, VerdictPending
	// This is current durable work state, not expired provider evidence. The
	// source facts keep their own TTLs after Current above clears stale values.
	lite.ValidUntil = now.Add(time.Hour).UnixNano()
	lite.FactsJSON = marshalJSON(facts)
	lite.ReasonsJSON = marshalJSON(append(lite.ReasonValues(), code))
	return lite, true
}
