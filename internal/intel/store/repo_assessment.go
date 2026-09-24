package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UpsertAssessment inserts or replaces the assessment of one IP.
func (s *Store) UpsertAssessment(ctx context.Context, a Assessment) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.IP) == "" {
		return fmt.Errorf("upsert assessment: ip is required")
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO ip_assessment (ip, profile, state, verdict, purity_score, purity_band, confidence,
			coverage, ip_type, native, flags, asn, as_org, country, city, reasons_json, components_json,
			computed_at_ns, valid_until_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET
			profile         = excluded.profile,
			state           = excluded.state,
			verdict         = excluded.verdict,
			purity_score    = excluded.purity_score,
			purity_band     = excluded.purity_band,
			confidence      = excluded.confidence,
			coverage        = excluded.coverage,
			ip_type         = excluded.ip_type,
			native          = excluded.native,
			flags           = excluded.flags,
			asn             = excluded.asn,
			as_org          = excluded.as_org,
			country         = excluded.country,
			city            = excluded.city,
			reasons_json    = excluded.reasons_json,
			components_json = excluded.components_json,
			computed_at_ns  = excluded.computed_at_ns,
			valid_until_ns  = excluded.valid_until_ns`,
		a.IP, a.Profile, a.State, a.Verdict, a.PurityScore, a.PurityBand, a.Confidence,
		a.Coverage, a.IPType, a.Native, a.Flags, a.ASN, a.ASOrg, a.Country, a.City,
		truncateJSON(a.ReasonsJSON, MaxDetailJSONBytes), truncateJSON(a.ComponentsJSON, MaxNormalizedJSONBytes),
		a.ComputedAtNs, a.ValidUntilNs)
	return err
}

// GetAssessment returns one assessment row.
func (s *Store) GetAssessment(ctx context.Context, ip string) (Assessment, bool, error) {
	db, err := s.conn()
	if err != nil {
		return Assessment{}, false, err
	}
	row, err := scanAssessment(db.QueryRowContext(ctx, assessmentSelect+` WHERE ip = ?`, ip))
	if errors.Is(err, sql.ErrNoRows) {
		return Assessment{}, false, nil
	}
	if err != nil {
		return Assessment{}, false, err
	}
	return row, true, nil
}

// ListAssessments returns assessments ordered by IP with pagination.
func (s *Store) ListAssessments(ctx context.Context, limit, offset int) ([]Assessment, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx, assessmentSelect+` ORDER BY ip LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectAssessments(rows)
}

// ListAllAssessments returns every assessment (snapshot rebuild, §5).
func (s *Store) ListAllAssessments(ctx context.Context) ([]Assessment, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, assessmentSelect+` ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	return collectAssessments(rows)
}

// CountAssessments returns the number of stored assessments.
func (s *Store) CountAssessments(ctx context.Context) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ip_assessment`).Scan(&count)
	return count, err
}

// ListExpiredAssessments returns assessments that expired at or before
// expiredBeforeNs; WP10's five-minute sweep recalculates them.
func (s *Store) ListExpiredAssessments(ctx context.Context, expiredAfterNs, expiredBeforeNs int64, limit int) ([]Assessment, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx, assessmentSelect+
		` WHERE valid_until_ns > ? AND valid_until_ns <= ? ORDER BY valid_until_ns LIMIT ?`,
		expiredAfterNs, expiredBeforeNs, limit)
	if err != nil {
		return nil, err
	}
	return collectAssessments(rows)
}

// ListAssessmentsWithForeignProfile returns assessments written by an older
// purity profile so WP10 can recompute them at startup (§1.8).
func (s *Store) ListAssessmentsWithForeignProfile(ctx context.Context, profile string, limit int) ([]Assessment, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, assessmentSelect+` WHERE profile <> ? ORDER BY ip LIMIT ?`, profile, limit)
	if err != nil {
		return nil, err
	}
	return collectAssessments(rows)
}

// DeleteAssessment removes one assessment row.
func (s *Store) DeleteAssessment(ctx context.Context, ip string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM ip_assessment WHERE ip = ?`, ip)
	return err
}

const assessmentSelect = `SELECT ip, profile, state, verdict, purity_score, purity_band, confidence,
	coverage, ip_type, native, flags, asn, as_org, country, city, reasons_json, components_json,
	computed_at_ns, valid_until_ns FROM ip_assessment`

func collectAssessments(rows *sql.Rows) ([]Assessment, error) {
	defer rows.Close()
	var out []Assessment
	for rows.Next() {
		row, err := scanAssessment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanAssessment(row rowScanner) (Assessment, error) {
	var a Assessment
	err := row.Scan(&a.IP, &a.Profile, &a.State, &a.Verdict, &a.PurityScore, &a.PurityBand,
		&a.Confidence, &a.Coverage, &a.IPType, &a.Native, &a.Flags, &a.ASN, &a.ASOrg,
		&a.Country, &a.City, &a.ReasonsJSON, &a.ComponentsJSON, &a.ComputedAtNs, &a.ValidUntilNs)
	return a, err
}
