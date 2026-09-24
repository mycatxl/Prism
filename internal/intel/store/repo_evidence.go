package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UpsertEvidence inserts or replaces the evidence of one (ip, provider) pair.
// The normalised blob is capped at 8 KiB and the raw response at 32 KiB (§2).
func (s *Store) UpsertEvidence(ctx context.Context, e Evidence) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(e.IP) == "" || strings.TrimSpace(e.Provider) == "" {
		return fmt.Errorf("upsert evidence: ip and provider are required")
	}
	raw := e.RawJSON
	if raw.Valid {
		raw = truncateRaw(raw.String, MaxRawJSONBytes)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO evidence (ip, provider, profile, via_node_hash, status, observed_at_ns,
			valid_until_ns, normalized_json, raw_json, error_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip, provider) DO UPDATE SET
			profile         = excluded.profile,
			via_node_hash   = excluded.via_node_hash,
			status          = excluded.status,
			observed_at_ns  = excluded.observed_at_ns,
			valid_until_ns  = excluded.valid_until_ns,
			normalized_json = excluded.normalized_json,
			raw_json        = excluded.raw_json,
			error_code      = excluded.error_code`,
		e.IP, e.Provider, e.Profile, e.ViaNodeHash, e.Status, e.ObservedAtNs,
		e.ValidUntilNs, truncateJSON(e.NormalizedJSON, MaxNormalizedJSONBytes), raw, e.ErrorCode)
	return err
}

// GetEvidence returns one (ip, provider) row.
func (s *Store) GetEvidence(ctx context.Context, ip, provider string) (Evidence, bool, error) {
	db, err := s.conn()
	if err != nil {
		return Evidence{}, false, err
	}
	row, err := scanEvidence(db.QueryRowContext(ctx, evidenceSelect+` WHERE ip = ? AND provider = ?`, ip, provider))
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, false, nil
	}
	if err != nil {
		return Evidence{}, false, err
	}
	return row, true, nil
}

// ListEvidenceByIP returns every provider row for one IP.
func (s *Store) ListEvidenceByIP(ctx context.Context, ip string) ([]Evidence, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, evidenceSelect+` WHERE ip = ? ORDER BY provider`, ip)
	if err != nil {
		return nil, err
	}
	return collectEvidence(rows)
}

// ListEvidenceByProvider returns every row of one provider, newest first.
func (s *Store) ListEvidenceByProvider(ctx context.Context, provider string, limit int) ([]Evidence, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx,
		evidenceSelect+` WHERE provider = ? ORDER BY observed_at_ns DESC LIMIT ?`, provider, limit)
	if err != nil {
		return nil, err
	}
	return collectEvidence(rows)
}

// ListAllEvidence returns every evidence row (snapshot and assessment rebuild).
func (s *Store) ListAllEvidence(ctx context.Context) ([]Evidence, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, evidenceSelect+` ORDER BY ip, provider`)
	if err != nil {
		return nil, err
	}
	return collectEvidence(rows)
}

// HasValidEvidence reports whether a usable evidence row already exists, which
// step 3 of the node pipeline uses to decide whether to enqueue a lookup (§3.2).
func (s *Store) HasValidEvidence(ctx context.Context, ip, provider string, nowNs int64) (bool, error) {
	db, err := s.conn()
	if err != nil {
		return false, err
	}
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM evidence WHERE ip = ? AND provider = ? AND status = ? AND valid_until_ns > ?`,
		ip, provider, StatusOk, nowNs).Scan(&count)
	return count > 0, err
}

// ListExpiredEvidence returns evidence rows that expired before expiredBeforeNs.
func (s *Store) ListExpiredEvidence(ctx context.Context, expiredBeforeNs int64, limit int) ([]Evidence, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx,
		evidenceSelect+` WHERE valid_until_ns <= ? ORDER BY valid_until_ns LIMIT ?`, expiredBeforeNs, limit)
	if err != nil {
		return nil, err
	}
	return collectEvidence(rows)
}

// DeleteEvidence removes one (ip, provider) row.
func (s *Store) DeleteEvidence(ctx context.Context, ip, provider string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM evidence WHERE ip = ? AND provider = ?`, ip, provider)
	return err
}

const evidenceSelect = `SELECT ip, provider, profile, via_node_hash, status, observed_at_ns,
	valid_until_ns, normalized_json, raw_json, error_code FROM evidence`

func collectEvidence(rows *sql.Rows) ([]Evidence, error) {
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		row, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanEvidence(row rowScanner) (Evidence, error) {
	var e Evidence
	err := row.Scan(&e.IP, &e.Provider, &e.Profile, &e.ViaNodeHash, &e.Status,
		&e.ObservedAtNs, &e.ValidUntilNs, &e.NormalizedJSON, &e.RawJSON, &e.ErrorCode)
	return e, err
}
