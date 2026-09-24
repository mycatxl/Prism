package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UpsertNodeCheck inserts or replaces the result of one check for one node.
func (s *Store) UpsertNodeCheck(ctx context.Context, c NodeCheck) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.NodeHash) == "" || strings.TrimSpace(c.CheckID) == "" {
		return fmt.Errorf("upsert node check: node_hash and check_id are required")
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO node_checks (node_hash, check_id, check_version, egress_ip, outcome, region,
			detail_json, latency_ms, observed_at_ns, valid_until_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_hash, check_id) DO UPDATE SET
			check_version  = excluded.check_version,
			egress_ip      = excluded.egress_ip,
			outcome        = excluded.outcome,
			region         = excluded.region,
			detail_json    = excluded.detail_json,
			latency_ms     = excluded.latency_ms,
			observed_at_ns = excluded.observed_at_ns,
			valid_until_ns = excluded.valid_until_ns`,
		c.NodeHash, c.CheckID, c.CheckVersion, c.EgressIP, c.Outcome, c.Region,
		truncateJSON(c.DetailJSON, MaxDetailJSONBytes), c.LatencyMs, c.ObservedAtNs, c.ValidUntilNs)
	return err
}

// ListNodeChecks returns every check row of one node.
func (s *Store) ListNodeChecks(ctx context.Context, nodeHash string) ([]NodeCheck, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, nodeCheckSelect+` WHERE node_hash = ? ORDER BY check_id`, nodeHash)
	if err != nil {
		return nil, err
	}
	return collectNodeChecks(rows)
}

// ListAllNodeChecks returns every check row (snapshot rebuild).
func (s *Store) ListAllNodeChecks(ctx context.Context) ([]NodeCheck, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, nodeCheckSelect+` ORDER BY node_hash, check_id`)
	if err != nil {
		return nil, err
	}
	return collectNodeChecks(rows)
}

// GetNodeCheck returns a single check row.
func (s *Store) GetNodeCheck(ctx context.Context, nodeHash, checkID string) (NodeCheck, bool, error) {
	db, err := s.conn()
	if err != nil {
		return NodeCheck{}, false, err
	}
	row, err := scanNodeCheck(db.QueryRowContext(ctx, nodeCheckSelect+` WHERE node_hash = ? AND check_id = ?`, nodeHash, checkID))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeCheck{}, false, nil
	}
	if err != nil {
		return NodeCheck{}, false, err
	}
	return row, true, nil
}

// ListExpiredChecks returns checks that expired before expiredBeforeNs, which the
// scheduled refresh job (§3.6) uses to compute its scope.
func (s *Store) ListExpiredChecks(ctx context.Context, expiredBeforeNs int64, limit int) ([]NodeCheck, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx,
		nodeCheckSelect+` WHERE valid_until_ns <= ? ORDER BY valid_until_ns LIMIT ?`, expiredBeforeNs, limit)
	if err != nil {
		return nil, err
	}
	return collectNodeChecks(rows)
}

// DeleteNodeCheck removes one check row.
func (s *Store) DeleteNodeCheck(ctx context.Context, nodeHash, checkID string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM node_checks WHERE node_hash = ? AND check_id = ?`, nodeHash, checkID)
	return err
}

const nodeCheckSelect = `SELECT node_hash, check_id, check_version, egress_ip, outcome, region,
	detail_json, latency_ms, observed_at_ns, valid_until_ns FROM node_checks`

func collectNodeChecks(rows *sql.Rows) ([]NodeCheck, error) {
	defer rows.Close()
	var out []NodeCheck
	for rows.Next() {
		row, err := scanNodeCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanNodeCheck(row rowScanner) (NodeCheck, error) {
	var c NodeCheck
	err := row.Scan(&c.NodeHash, &c.CheckID, &c.CheckVersion, &c.EgressIP, &c.Outcome,
		&c.Region, &c.DetailJSON, &c.LatencyMs, &c.ObservedAtNs, &c.ValidUntilNs)
	return c, err
}
