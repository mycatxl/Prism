package intel

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"time"

	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/quality"
)

// Assessor recomputes the purity assessment of an IP from the authoritative
// intel.db evidence and persists it (WP10 §1).
type Assessor struct {
	store    *store.Store
	snapshot *Snapshot
	enabled  func() map[string]bool
	onChange func(ips []netip.Addr)
	now      func() time.Time
	logf     func(format string, args ...any)
}

// AssessorOptions configures the assessor. Store is required.
type AssessorOptions struct {
	Store    *store.Store
	Snapshot *Snapshot
	// Enabled reports which data sources are currently enabled; it feeds the
	// §1.2 coverage denominator.
	Enabled func() map[string]bool
	// OnChange runs after an assessment was persisted. The egress-IP dirty
	// notification of §2 hangs off it.
	OnChange func(ips []netip.Addr)
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// NewAssessor builds an assessor. A nil store is a programming error (R5).
func NewAssessor(opts AssessorOptions) *Assessor {
	if opts.Store == nil {
		panic("intel: NewAssessor requires a store")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Assessor{
		store:    opts.Store,
		snapshot: opts.Snapshot,
		enabled:  opts.Enabled,
		onChange: opts.OnChange,
		now:      now,
		logf:     logf,
	}
}

// enabledSources resolves the enabled source map.
func (a *Assessor) enabledSources() map[string]bool {
	if a == nil || a.enabled == nil {
		return nil
	}
	return a.enabled()
}

// AssessIP recomputes and persists the assessment of one IP.
func (a *Assessor) AssessIP(ctx context.Context, ip netip.Addr) (store.Assessment, error) {
	if a == nil || a.store == nil || !ip.IsValid() {
		return store.Assessment{}, nil
	}
	now := a.now().UTC()
	evidence, failed, err := a.loadEvidence(ctx, ip)
	if err != nil {
		return store.Assessment{}, err
	}
	result := assess.AssessInput(assess.Input{
		IP:       ip,
		Evidence: evidence,
		Enabled:  a.enabledSources(),
		Failed:   failed,
		Now:      now,
	})
	row := AssessmentRow(result, now)
	if err := a.store.UpsertAssessment(ctx, row); err != nil {
		return store.Assessment{}, err
	}
	if a.snapshot != nil {
		a.snapshot.SetAssessmentFromRow(row)
	}
	return row, nil
}

// AssessMany recomputes the assessments of several IPs. Errors are logged and
// skipped so one bad IP never blocks the batch.
func (a *Assessor) AssessMany(ctx context.Context, ips []netip.Addr) int {
	if a == nil {
		return 0
	}
	done := 0
	for _, ip := range ips {
		if !ip.IsValid() {
			continue
		}
		if _, err := a.AssessIP(ctx, ip); err != nil {
			a.logf("[intel] assess %s: %v", ip, err)
			continue
		}
		done++
	}
	if done > 0 && a.onChange != nil {
		a.onChange(ips)
	}
	return done
}

// AssessNode recomputes the assessments of a node's observed egress addresses.
func (a *Assessor) AssessNode(ctx context.Context, nodeHash string) (int, error) {
	if a == nil || a.store == nil {
		return 0, nil
	}
	row, ok, err := a.store.GetNodeEgress(ctx, nodeHash)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	ips := make([]netip.Addr, 0, 2)
	for _, raw := range []string{row.IPv4, row.IPv6} {
		addr, err := netip.ParseAddr(raw)
		if err != nil || !addr.IsValid() {
			continue
		}
		ips = append(ips, addr.Unmap())
	}
	return a.AssessMany(ctx, ips), nil
}

// SweepExpired recalculates the assessments that expired in (expiredAfterNs,
// nowNs]. WP10 §1.8 runs it every five minutes.
func (a *Assessor) SweepExpired(ctx context.Context, expiredAfterNs int64) (int, error) {
	if a == nil || a.store == nil {
		return 0, nil
	}
	now := a.now().UTC()
	rows, err := a.store.ListExpiredAssessments(ctx, expiredAfterNs, now.UnixNano(), maxAssessmentSweep)
	if err != nil {
		return 0, err
	}
	changed := make([]netip.Addr, 0, len(rows))
	for _, row := range rows {
		ip, err := netip.ParseAddr(row.IP)
		if err != nil {
			continue
		}
		if _, err := a.AssessIP(ctx, ip.Unmap()); err != nil {
			a.logf("[intel] reassess %s: %v", row.IP, err)
			continue
		}
		changed = append(changed, ip.Unmap())
	}
	if len(changed) > 0 && a.onChange != nil {
		a.onChange(changed)
	}
	return len(changed), nil
}

// RecomputeForeignProfiles recalculates rows written by an older profile
// (§1.8 startup pass). limit <= 0 uses the default batch.
func (a *Assessor) RecomputeForeignProfiles(ctx context.Context, limit int) (int, error) {
	if a == nil || a.store == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = maxAssessmentSweep
	}
	rows, err := a.store.ListAssessmentsWithForeignProfile(ctx, assess.Profile, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, row := range rows {
		ip, err := netip.ParseAddr(row.IP)
		if err != nil {
			continue
		}
		if _, err := a.AssessIP(ctx, ip.Unmap()); err != nil {
			a.logf("[intel] recompute %s: %v", row.IP, err)
			continue
		}
		done++
	}
	return done, nil
}

// maxAssessmentSweep bounds one sweep batch (R4).
const maxAssessmentSweep = 5000

// loadEvidence reads the status=ok evidence rows of one IP plus the last error
// code of every enabled source that has no current evidence.
func (a *Assessor) loadEvidence(ctx context.Context, ip netip.Addr) ([]quality.Evidence, map[string]string, error) {
	rows, err := a.store.ListEvidenceByIP(ctx, ip.Unmap().String())
	if err != nil {
		return nil, nil, err
	}
	all := a.enabledSources()
	newest := make(map[string]store.Evidence, len(rows))
	for _, row := range rows {
		current, ok := newest[row.Provider]
		if ok && current.ObservedAtNs >= row.ObservedAtNs {
			continue
		}
		newest[row.Provider] = row
	}

	evidence := make([]quality.Evidence, 0, len(newest))
	failed := make(map[string]string)
	for provider, row := range newest {
		if row.Status != store.StatusOk {
			// "enabled" is only meaningful for sources the caller tracks.
			if all == nil || all[provider] {
				failed[provider] = row.ErrorCode
			}
			continue
		}
		ev, err := evidenceFromRow(row)
		if err != nil {
			a.logf("[intel] decode evidence %s/%s: %v", row.IP, provider, err)
			continue
		}
		evidence = append(evidence, ev)
	}
	return evidence, failed, nil
}

// evidenceFromRow decodes one stored row into the normalised evidence shape.
func evidenceFromRow(row store.Evidence) (quality.Evidence, error) {
	var ev quality.Evidence
	raw := row.NormalizedJSON
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return quality.Evidence{}, err
		}
	}
	if ev.IP == "" {
		ev.IP = row.IP
	}
	if ev.Provider == "" {
		ev.Provider = row.Provider
	}
	if ev.Profile == "" {
		ev.Profile = row.Profile
	}
	if ev.ObservedAt.IsZero() && row.ObservedAtNs > 0 {
		ev.ObservedAt = time.Unix(0, row.ObservedAtNs).UTC()
	}
	if ev.ValidUntil.IsZero() && row.ValidUntilNs > 0 {
		ev.ValidUntil = time.Unix(0, row.ValidUntilNs).UTC()
	}
	return ev, nil
}

// AssessmentRow converts a scoring result into its ip_assessment row.
func AssessmentRow(result assess.Assessment, computedAt time.Time) store.Assessment {
	row := store.Assessment{
		IP:             result.IPText,
		Profile:        assess.Profile,
		State:          result.State,
		Verdict:        result.Verdict,
		PurityBand:     result.PurityBand,
		Confidence:     result.Confidence,
		Coverage:       result.Coverage,
		IPType:         result.IPType,
		Flags:          int64(result.Flags),
		ASOrg:          result.ASOrg,
		Country:        result.Country,
		City:           result.City,
		ReasonsJSON:    marshalJSON(result.Reasons),
		ComponentsJSON: marshalComponents(result),
		ComputedAtNs:   computedAt.UTC().UnixNano(),
		ValidUntilNs:   result.ValidUntil.UTC().UnixNano(),
	}
	if result.PurityScore != nil {
		row.PurityScore = sql.NullInt64{Int64: int64(*result.PurityScore), Valid: true}
	}
	if result.Native != nil {
		value := int64(0)
		if *result.Native {
			value = 1
		}
		row.Native = sql.NullInt64{Int64: value, Valid: true}
	}
	if result.ASN > 0 {
		row.ASN = sql.NullInt64{Int64: int64(result.ASN), Valid: true}
	}
	if row.ValidUntilNs <= 0 {
		row.ValidUntilNs = computedAt.UTC().Add(time.Hour).UnixNano()
	}
	return row
}

// componentsBlob is the explainable detail persisted in components_json (§1.7).
type componentsBlob struct {
	Components    []assess.Component `json:"components"`
	Penalties     []assess.Penalty   `json:"penalties"`
	PenaltyPoints int                `json:"penalty_points"`
	Coverage      float64            `json:"coverage"`
	Agreement     float64            `json:"agreement"`
	SourceCount   int                `json:"source_count"`
	Explain       string             `json:"explain"`
}

func marshalComponents(result assess.Assessment) string {
	return marshalJSON(componentsBlob{
		Components:    result.Components,
		Penalties:     result.Penalties,
		PenaltyPoints: result.PenaltyPoints,
		Coverage:      round3(result.Coverage),
		Agreement:     round3(result.Agreement),
		SourceCount:   result.SourceCount,
		Explain:       result.Explain(),
	})
}

func marshalJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}
