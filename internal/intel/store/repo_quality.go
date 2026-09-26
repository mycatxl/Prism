package store

import (
	"context"
	"strings"
)

// QualityCounts is the aggregate the legacy /api/v1/quality/status surface
// reports (WP08 §9). Every field is a count of IPs, not of evidence rows: the
// WebUI renders them as "known IPs" and "checked IPs".
type QualityCounts struct {
	KnownIPs    int
	CheckedIPs  int
	LowRiskIPs  int
	HighRiskIPs int
	StaleIPs    int
}

// assessmentStateValid is the ip_assessment.state value of a scored, current
// assessment. It mirrors the projection's intel.StateValid; the store keeps the
// states as plain strings because the schema stores them that way.
const assessmentStateValid = "valid"

// assessmentVerdictFavorable and assessmentVerdictHighRisk are the two verdicts
// the legacy low_risk_ips / high_risk_ips counters split on.
const (
	assessmentVerdictFavorable = "favorable"
	assessmentVerdictHighRisk  = "high_risk"
)

// CountQualitySummary counts the assessed IPs by state and verdict in one pass.
//
// The mapping onto the legacy counters is:
//
//	known_ips     — every IP with an assessment row
//	checked_ips   — assessments in the valid state
//	low_risk_ips  — favorable verdicts
//	high_risk_ips — high_risk verdicts
//	stale_ips     — assessments whose validity already elapsed
//
// nowNs is compared against valid_until_ns, so an assessment that expires during
// the request is counted as stale from the next call on.
func (s *Store) CountQualitySummary(ctx context.Context, nowNs int64) (QualityCounts, error) {
	db, err := s.conn()
	if err != nil {
		return QualityCounts{}, err
	}
	var counts QualityCounts
	err = db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN verdict = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN verdict = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN valid_until_ns <= ? THEN 1 ELSE 0 END), 0)
		FROM ip_assessment`,
		assessmentStateValid, assessmentVerdictFavorable, assessmentVerdictHighRisk, nowNs,
	).Scan(&counts.KnownIPs, &counts.CheckedIPs, &counts.LowRiskIPs, &counts.HighRiskIPs, &counts.StaleIPs)
	if err != nil {
		return QualityCounts{}, err
	}
	return counts, nil
}

// CountEvidenceByProvider counts the usable evidence rows of one provider. It is
// the "已复核" counter of the manual source cards: only status=ok rows count, so a
// provider that is failing does not look productive.
func (s *Store) CountEvidenceByProvider(ctx context.Context, provider string) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return 0, nil
	}
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM evidence WHERE provider = ? AND status = ?`,
		provider, StatusOk).Scan(&count)
	return count, err
}

// maxEvidenceLookupIPs bounds one ListEvidenceByIPs call. It mirrors
// MaxEgressLookupHashes: a caller that passes more is truncated rather than
// turning into an unbounded query.
const maxEvidenceLookupIPs = 4096

// evidenceLookupChunk bounds the parameters of one IN clause. SQLite allows far
// more, but a small chunk keeps the statement cache effective.
const evidenceLookupChunk = 400

// ListEvidenceByIPs returns every evidence row of the given IPs, ordered by IP
// and provider.
//
// It is the batched form of ListEvidenceByIP: the quality list and the node list
// both project a whole page with one query instead of one query per IP.
func (s *Store) ListEvidenceByIPs(ctx context.Context, ips []string) ([]Evidence, error) {
	if len(ips) == 0 {
		return nil, nil
	}
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	unique := make([]string, 0, len(ips))
	seen := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		trimmed := strings.TrimSpace(ip)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
		if len(unique) >= maxEvidenceLookupIPs {
			break
		}
	}
	if len(unique) == 0 {
		return nil, nil
	}

	out := make([]Evidence, 0, len(unique))
	for start := 0; start < len(unique); start += evidenceLookupChunk {
		end := start + evidenceLookupChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		args := make([]any, 0, len(chunk))
		for _, ip := range chunk {
			args = append(args, ip)
		}
		rows, err := db.QueryContext(ctx,
			evidenceSelect+` WHERE ip IN (`+sqlPlaceholders(len(chunk))+`) ORDER BY ip, provider`, args...)
		if err != nil {
			return nil, err
		}
		collected, err := collectEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, collected...)
	}
	return out, nil
}
