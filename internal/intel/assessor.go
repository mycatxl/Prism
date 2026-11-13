package intel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/quality"
)

// Assessor recomputes the purity assessment of an IP from the authoritative
// intel.db evidence and persists it.
type Assessor struct {
	store    *store.Store
	snapshot *Snapshot
	enabled  func() map[string]bool
	onChange func(ips []netip.Addr)
	now      func() time.Time
	logf     func(format string, args ...any)
	// bands supplies the score thresholds; see AssessorOptions.Bands.
	bands func() quality.Bands
}

// AssessorOptions configures the assessor. Store is required.
type AssessorOptions struct {
	Store    *store.Store
	Snapshot *Snapshot
	// Enabled reports which data sources are currently enabled; it feeds the
	// coverage denominator.
	Enabled func() map[string]bool
	// OnChange runs after an assessment was persisted. The egress-IP dirty
	// notification of hangs off it.
	OnChange func(ips []netip.Addr)
	Now      func() time.Time
	Logf     func(format string, args ...any)
	// Bands reports the score thresholds to score against. Nil means the
	// shipped thresholds, which keeps a caller that has no runtime config to
	// read from behaving as it did before the thresholds were configurable.
	Bands func() quality.Bands
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
		bands:    opts.Bands,
	}
}

// enabledSources resolves the enabled source map.
func (a *Assessor) enabledSources() map[string]bool {
	if a == nil || a.enabled == nil {
		return nil
	}
	return a.enabled()
}

// bandSet resolves the thresholds to score against, falling back to the
// shipped ones when no settings surface was wired.
func (a *Assessor) bandSet() quality.Bands {
	if a == nil || a.bands == nil {
		return quality.DefaultBands()
	}
	return a.bands().Normalized()
}

// AssessIP recomputes and persists the assessment of one IP.
func (a *Assessor) AssessIP(ctx context.Context, ip netip.Addr) (store.Assessment, error) {
	if a == nil || a.store == nil || !ip.IsValid() {
		return store.Assessment{}, nil
	}
	ip = ip.Unmap()
	var row store.Assessment
	err := a.store.WithEvidenceLock(ip.String(), func() error {
		var err error
		row, err = a.assessIP(ctx, ip)
		return err
	})
	return row, err
}

// assessIP requires the store's evidence lock for this IP.
func (a *Assessor) assessIP(ctx context.Context, ip netip.Addr) (store.Assessment, error) {
	now := a.now().UTC()
	input, err := a.loadEvidence(ctx, ip, now)
	if err != nil {
		if ctx.Err() == nil {
			a.invalidate(ip, "EVIDENCE_READ_FAILED", now)
		}
		return store.Assessment{}, err
	}
	input.Enabled, input.Bands = a.enabledSources(), a.bandSet()
	result := assess.AssessInput(input)
	row := AssessmentRow(result, now)
	if err := a.store.UpsertAssessment(ctx, row); err != nil {
		if ctx.Err() == nil {
			a.invalidate(ip, "ASSESSMENT_WRITE_FAILED", now)
		}
		return store.Assessment{}, err
	}
	if a.snapshot != nil {
		a.snapshot.SetAssessmentFromRow(row)
	}
	// All callers, including the job's final single-IP step, must publish the
	// committed projection before views re-evaluate membership.
	if a.onChange != nil {
		a.onChange([]netip.Addr{ip.Unmap()})
	}
	return row, nil
}

func (a *Assessor) invalidate(ip netip.Addr, code string, now time.Time) {
	if a.snapshot != nil {
		facts := map[string]quality.Fact{"purity": quality.PurityDiagnostic(code)}
		a.snapshot.SetAssessment(ip, AssessmentLite{Score: -1, Native: -1, State: StatePending, ReasonsJSON: marshalJSON([]string{code}), FactsJSON: marshalJSON(facts), ComputedAt: now.UnixNano(), ValidUntil: now.Add(time.Hour).UnixNano()})
	}
	if a.onChange != nil {
		a.onChange([]netip.Addr{ip.Unmap()})
	}
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
// nowNs]. runs it every five minutes.
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
	return len(changed), nil
}

// RecomputeForeignProfiles recalculates rows written by an older profile
// (startup pass). limit <= 0 uses the default batch.
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
			return done, fmt.Errorf("assessment profile migration: invalid stored IP")
		}
		if _, err := a.AssessIP(ctx, ip.Unmap()); err != nil {
			return done, fmt.Errorf("assessment profile migration: %w", err)
		}
		done++
	}
	return done, nil
}

// maxAssessmentSweep bounds one sweep batch (R4).
const maxAssessmentSweep = 5000

// loadEvidence reads the status=ok evidence rows of one IP plus the last error
// code of every enabled source that has no current evidence.
func (a *Assessor) loadEvidence(ctx context.Context, ip netip.Addr, now time.Time) (assess.Input, error) {
	rows, err := a.store.ListEvidenceByIP(ctx, ip.Unmap().String())
	if err != nil {
		return assess.Input{}, err
	}
	return AssessmentInputFromRows(ip, rows, now), nil
}

// AssessmentInputFromRows is shared by persisted assessments and API diagnostics.
// Precedence: latest provider status, successful-row expiry, then payload
// validation. An expired payload is never decoded, so malformed JSON cannot
// replace EVIDENCE_EXPIRED. Explicit failures retain their atomic error code.
func AssessmentInputFromRows(ip netip.Addr, rows []store.Evidence, now time.Time) assess.Input {
	input := assess.Input{IP: ip, Now: now, Failed: map[string]string{}}
	newest := make(map[string]store.Evidence, len(rows))
	for _, row := range rows {
		current, ok := newest[row.Provider]
		if ok && current.ObservedAtNs >= row.ObservedAtNs {
			continue
		}
		newest[row.Provider] = row
	}

	for provider, row := range newest {
		if row.Status != store.StatusOk {
			input.Failed[provider] = quality.SafeCode(row.ErrorCode)
			continue
		}
		if row.ValidUntilNs > 0 && row.ValidUntilNs <= now.UnixNano() {
			input.Evidence = append(input.Evidence, quality.Evidence{
				IP: row.IP, Provider: provider, Profile: row.Profile,
				ObservedAt: time.Unix(0, row.ObservedAtNs).UTC(),
				ValidUntil: time.Unix(0, row.ValidUntilNs).UTC(),
			})
			continue
		}
		ev, err := EvidenceFromRow(row)
		if err != nil {
			input.Failed[provider] = "PROVIDER_RESPONSE"
			continue
		}
		input.Evidence = append(input.Evidence, ev)
	}
	return input
}

// EvidenceFromRow decodes one stored evidence row into the normalised
// quality.Evidence shape.
//
// It is exported because the legacy /api/v1/quality/* alias surface maps intel
// rows back onto quality.Summary, and re-deriving the same
// normalisation there would be a second implementation of this contract.
func EvidenceFromRow(row store.Evidence) (quality.Evidence, error) {
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
	storedIP, storedErr := netip.ParseAddr(row.IP)
	payloadIP, payloadErr := netip.ParseAddr(ev.IP)
	if storedErr != nil || payloadErr != nil || storedIP.Unmap() != payloadIP.Unmap() || ev.Provider != row.Provider {
		return quality.Evidence{}, fmt.Errorf("evidence identity mismatch")
	}
	ev.IP = storedIP.Unmap().String()
	// Row metadata is authoritative. Embedded timestamps cannot extend its TTL.
	if row.ObservedAtNs > 0 {
		ev.ObservedAt = time.Unix(0, row.ObservedAtNs).UTC()
	} else {
		ev.ObservedAt = time.Time{}
	}
	if row.ValidUntilNs > 0 {
		until := time.Unix(0, row.ValidUntilNs).UTC()
		if ev.ValidUntil.IsZero() || until.Before(ev.ValidUntil) {
			ev.ValidUntil = until
		}
	} else {
		ev.ValidUntil = time.Time{}
	}
	ev.NormalizeLegacySignals()
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

// componentsBlob is the explainable detail persisted in components_json.
type componentsBlob struct {
	Facts         map[string]quality.Fact `json:"facts"`
	Components    []assess.Component      `json:"components"`
	Penalties     []assess.Penalty        `json:"penalties"`
	PenaltyPoints int                     `json:"penalty_points"`
	Coverage      float64                 `json:"coverage"`
	Agreement     float64                 `json:"agreement"`
	SourceCount   int                     `json:"source_count"`
	Explain       string                  `json:"explain"`
}

func marshalComponents(result assess.Assessment) string {
	return marshalJSON(componentsBlob{
		Facts:         result.Facts,
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
