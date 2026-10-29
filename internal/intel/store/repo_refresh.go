package store

import (
	"context"
	"fmt"
	"strings"
)

// RefreshNodeHashPage is a bounded keyset page over the three scheduled
// refresh sources. Each source is limited before the union; the first limit
// rows of each source are sufficient to determine the first limit rows of the
// union, so the database work stays bounded as well as the returned slice.
type RefreshNodeHashPage struct {
	NodeHashes []string
	Cursor     string
	Scanned    int64
	Complete   bool
}

// PageRefreshNodeHashes returns stale node hashes after cursor in stable hash
// order. Expired evidence and stale egress are always included; expired checks
// are included only when includeExpiredChecks is true.
func (s *Store) PageRefreshNodeHashes(ctx context.Context, expiredBeforeNs, staleBeforeNs int64, includeExpiredChecks bool, cursor string, limit int) (RefreshNodeHashPage, error) {
	if limit <= 0 {
		return RefreshNodeHashPage{Complete: true}, nil
	}
	db, err := s.conn()
	if err != nil {
		return RefreshNodeHashPage{}, err
	}

	parts := make([]string, 0, 4)
	args := make([]any, 0, 16)
	appendPart := func(query string, values ...any) {
		parts = append(parts, query)
		args = append(args, values...)
	}
	appendPart(`SELECT node_hash FROM (
			SELECT node_hash FROM node_egress
			WHERE node_hash > ? AND ipv4 IN (SELECT ip FROM evidence WHERE valid_until_ns <= ?)
			ORDER BY node_hash LIMIT ?
		)`, cursor, expiredBeforeNs, limit)
	appendPart(`SELECT node_hash FROM (
			SELECT node_hash FROM node_egress
			WHERE node_hash > ? AND ipv6 IN (SELECT ip FROM evidence WHERE valid_until_ns <= ?)
			ORDER BY node_hash LIMIT ?
		)`, cursor, expiredBeforeNs, limit)
	if includeExpiredChecks {
		appendPart(`SELECT node_hash FROM (
				SELECT node_hash FROM node_checks
				WHERE node_hash > ? AND valid_until_ns <= ?
				ORDER BY node_hash LIMIT ?
			)`, cursor, expiredBeforeNs, limit)
	}
	appendPart(`SELECT node_hash FROM (
			SELECT node_hash FROM node_egress
			WHERE node_hash > ? AND v4_observed_ns > 0 AND v4_observed_ns < ?
			ORDER BY node_hash LIMIT ?
		)`, cursor, staleBeforeNs, limit)
	query := "SELECT node_hash FROM (" + strings.Join(parts, " UNION ") + ") ORDER BY node_hash LIMIT ?"
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return RefreshNodeHashPage{}, fmt.Errorf("page refresh node hashes: %w", err)
	}
	defer rows.Close()

	page := RefreshNodeHashPage{NodeHashes: make([]string, 0, limit), Cursor: cursor}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return RefreshNodeHashPage{}, err
		}
		page.NodeHashes = append(page.NodeHashes, hash)
		page.Cursor = hash
		page.Scanned++
	}
	if err := rows.Err(); err != nil {
		return RefreshNodeHashPage{}, err
	}
	page.Complete = len(page.NodeHashes) < limit
	return page, nil
}

// NodeNeedsRefresh rechecks one node against the scheduled refresh predicates.
// It is used immediately before execution so a node refreshed after scope
// expansion is skipped instead of spending a new refresh item.
func (s *Store) NodeNeedsRefresh(ctx context.Context, nodeHash string, expiredBeforeNs, staleBeforeNs int64, includeExpiredChecks bool) (bool, error) {
	db, err := s.conn()
	if err != nil {
		return false, err
	}
	var exists int
	err = db.QueryRowContext(ctx, `
		SELECT (
			EXISTS(
				SELECT 1 FROM node_egress AS e
				WHERE e.node_hash = ? AND (
					e.ipv4 IN (SELECT ip FROM evidence WHERE valid_until_ns <= ?)
					OR e.ipv6 IN (SELECT ip FROM evidence WHERE valid_until_ns <= ?)
					OR (e.v4_observed_ns > 0 AND e.v4_observed_ns < ?)
				)
			)
			OR (? = 1 AND EXISTS(
				SELECT 1 FROM node_checks AS c
				WHERE c.node_hash = ? AND c.valid_until_ns <= ?
			))
		)`, nodeHash, expiredBeforeNs, expiredBeforeNs, staleBeforeNs,
		boolInt(includeExpiredChecks), nodeHash, expiredBeforeNs).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("node refresh membership: %w", err)
	}
	return exists != 0, nil
}
