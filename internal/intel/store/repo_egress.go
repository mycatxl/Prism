package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// EgressObservation is one egress probe result for one node.
//
// IPv4 == invalid means "no new IPv4 information" and leaves the stored value
// untouched. V6Checked == true records an IPv6 attempt: an invalid IPv6 then
// clears the stored IPv6 address (a node without IPv6 is a normal outcome).
type EgressObservation struct {
	NodeHash  string
	IPv4      netip.Addr
	IPv6      netip.Addr
	V6Checked bool
	Colo      string
	Loc       string
	NowNs     int64
}

// RecordEgress persists one egress observation and appends egress_history rows
// for every address that changed. It returns the stored row and whether an
// address changed, so callers can react to IP churn (§4).
func (s *Store) RecordEgress(ctx context.Context, obs EgressObservation) (NodeEgress, bool, error) {
	var row NodeEgress
	db, err := s.conn()
	if err != nil {
		return row, false, err
	}
	if strings.TrimSpace(obs.NodeHash) == "" {
		return row, false, fmt.Errorf("record egress: node_hash is required")
	}
	if obs.NowNs <= 0 {
		return row, false, fmt.Errorf("record egress: now_ns is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return row, false, err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := getNodeEgressTx(ctx, tx, obs.NodeHash)
	if err != nil {
		return row, false, err
	}
	row = current
	row.NodeHash = obs.NodeHash

	changed := false
	if obs.IPv4.IsValid() {
		ip := obs.IPv4.Unmap().String()
		if row.IPv4 != ip {
			changed = true
			row.IPv4 = ip
			if err := appendEgressHistoryTx(ctx, tx, obs.NodeHash, FamilyV4, ip, obs.NowNs); err != nil {
				return row, false, err
			}
		}
		row.V4ObservedNs = obs.NowNs
	}
	if obs.V6Checked {
		row.V6CheckedNs = obs.NowNs
		if obs.IPv6.IsValid() {
			ip := obs.IPv6.Unmap().String()
			if row.IPv6 != ip {
				changed = true
				row.IPv6 = ip
				if err := appendEgressHistoryTx(ctx, tx, obs.NodeHash, FamilyV6, ip, obs.NowNs); err != nil {
					return row, false, err
				}
			}
			row.V6ObservedNs = obs.NowNs
		} else {
			if row.IPv6 != "" {
				changed = true
			}
			row.IPv6 = ""
		}
	}
	if strings.TrimSpace(obs.Colo) != "" {
		row.Colo = obs.Colo
	}
	if strings.TrimSpace(obs.Loc) != "" {
		row.Loc = obs.Loc
	}

	if err := upsertNodeEgressTx(ctx, tx, row); err != nil {
		return row, false, err
	}
	if err := tx.Commit(); err != nil {
		return row, false, err
	}
	return row, changed, nil
}

// UpsertNodeEgress writes a full row without touching egress_history. It is the
// low-level write used by tests and by maintenance code.
func (s *Store) UpsertNodeEgress(ctx context.Context, row NodeEgress) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(row.NodeHash) == "" {
		return fmt.Errorf("upsert node egress: node_hash is required")
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO node_egress (node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_hash) DO UPDATE SET
			ipv4           = excluded.ipv4,
			ipv6           = excluded.ipv6,
			colo           = excluded.colo,
			loc            = excluded.loc,
			v4_observed_ns = excluded.v4_observed_ns,
			v6_observed_ns = excluded.v6_observed_ns,
			v6_checked_ns  = excluded.v6_checked_ns`,
		row.NodeHash, row.IPv4, row.IPv6, row.Colo, row.Loc,
		row.V4ObservedNs, row.V6ObservedNs, row.V6CheckedNs,
	)
	return err
}

// GetNodeEgress returns one node's egress row. The bool is false when the node
// has never been probed.
func (s *Store) GetNodeEgress(ctx context.Context, nodeHash string) (NodeEgress, bool, error) {
	db, err := s.conn()
	if err != nil {
		return NodeEgress{}, false, err
	}
	row, err := scanNodeEgress(db.QueryRowContext(ctx,
		`SELECT node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns
		 FROM node_egress WHERE node_hash = ?`, nodeHash))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeEgress{}, false, nil
	}
	if err != nil {
		return NodeEgress{}, false, err
	}
	return row, true, nil
}

// ListNodeEgress returns every egress row ordered by node hash.
func (s *Store) ListNodeEgress(ctx context.Context) ([]NodeEgress, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns
		 FROM node_egress ORDER BY node_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NodeEgress
	for rows.Next() {
		row, err := scanNodeEgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MaxEgressLookupHashes bounds one batch egress lookup. The node list looks up
// the hashes of one page, so the cap only bites on an unusually large page: the
// hashes past it are skipped and their egress facts stay empty rather than
// failing the request.
const MaxEgressLookupHashes = 4096

// egressLookupChunk is how many hashes go into one IN (...) clause, keeping the
// statement well below SQLite's variable limit.
const egressLookupChunk = 400

// NodeEgressByHashes returns the stored egress rows of the given node hashes.
// It reads them in chunks of egressLookupChunk so a caller can render a whole
// page of nodes without one query per node (WP10 §4). At most
// MaxEgressLookupHashes distinct hashes are resolved; empty and duplicate
// hashes are ignored and hashes without a stored row are simply absent from the
// result.
func (s *Store) NodeEgressByHashes(ctx context.Context, hashes []string) (map[string]NodeEgress, error) {
	if len(hashes) == 0 {
		return map[string]NodeEgress{}, nil
	}
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	unique := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
		if len(unique) >= MaxEgressLookupHashes {
			break
		}
	}

	out := make(map[string]NodeEgress, len(unique))
	for start := 0; start < len(unique); start += egressLookupChunk {
		end := start + egressLookupChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		args := make([]any, 0, len(chunk))
		for _, hash := range chunk {
			args = append(args, hash)
		}
		rows, err := db.QueryContext(ctx,
			`SELECT node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns
			 FROM node_egress WHERE node_hash IN (`+sqlPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			row, err := scanNodeEgress(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[row.NodeHash] = row
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sqlPlaceholders renders "?,?,..." for one IN clause.
func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ListNodeHashes returns every node hash known to intel.db.
func (s *Store) ListNodeHashes(ctx context.Context) ([]string, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT node_hash FROM node_egress ORDER BY node_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	return out, rows.Err()
}

// ListNodesByIP returns the node hashes whose IPv4 or IPv6 equals ip, capped by
// limit (WP08 §7: GET /api/v1/intel/ip/{ip} lists at most 100 nodes).
func (s *Store) ListNodesByIP(ctx context.Context, ip string, limit int) ([]string, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx,
		`SELECT node_hash FROM node_egress WHERE ipv4 = ? OR ipv6 = ? ORDER BY node_hash LIMIT ?`,
		ip, ip, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	return out, rows.Err()
}

// ListEgressHistory returns the newest history rows of one node.
func (s *Store) ListEgressHistory(ctx context.Context, nodeHash string, limit int) ([]EgressHistory, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, node_hash, family, ip, observed_at_ns
		FROM egress_history WHERE node_hash = ?
		ORDER BY observed_at_ns DESC, id DESC LIMIT ?`, nodeHash, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EgressHistory
	for rows.Next() {
		var h EgressHistory
		if err := rows.Scan(&h.ID, &h.NodeHash, &h.Family, &h.IP, &h.ObservedAtNs); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PruneEgressHistory keeps only the newest keepPerNode rows per node (§6).
func (s *Store) PruneEgressHistory(ctx context.Context, keepPerNode int) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	if keepPerNode <= 0 {
		keepPerNode = 50
	}
	res, err := db.ExecContext(ctx, `
		DELETE FROM egress_history WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY node_hash ORDER BY observed_at_ns DESC, id DESC) AS rn
				FROM egress_history
			) WHERE rn > ?
		)`, keepPerNode)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteNodeData removes every intel.db row belonging to the given node hashes
// (egress, history, checks and via-node evidence). It is used by the cleaner for
// nodes that no longer exist in cache.db and keeps the tables bounded (R4).
func (s *Store) DeleteNodeData(ctx context.Context, nodeHashes []string) (int64, error) {
	if len(nodeHashes) == 0 {
		return 0, nil
	}
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	for _, hash := range nodeHashes {
		for _, stmt := range []string{
			`DELETE FROM node_egress WHERE node_hash = ?`,
			`DELETE FROM egress_history WHERE node_hash = ?`,
			`DELETE FROM node_checks WHERE node_hash = ?`,
			`DELETE FROM evidence WHERE via_node_hash = ?`,
		} {
			res, err := tx.ExecContext(ctx, stmt, hash)
			if err != nil {
				return 0, err
			}
			n, _ := res.RowsAffected()
			total += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

func getNodeEgressTx(ctx context.Context, tx *sql.Tx, nodeHash string) (NodeEgress, error) {
	row, err := scanNodeEgress(tx.QueryRowContext(ctx,
		`SELECT node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns
		 FROM node_egress WHERE node_hash = ?`, nodeHash))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeEgress{NodeHash: nodeHash}, nil
	}
	return row, err
}

func upsertNodeEgressTx(ctx context.Context, tx *sql.Tx, row NodeEgress) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO node_egress (node_hash, ipv4, ipv6, colo, loc, v4_observed_ns, v6_observed_ns, v6_checked_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_hash) DO UPDATE SET
			ipv4           = excluded.ipv4,
			ipv6           = excluded.ipv6,
			colo           = excluded.colo,
			loc            = excluded.loc,
			v4_observed_ns = excluded.v4_observed_ns,
			v6_observed_ns = excluded.v6_observed_ns,
			v6_checked_ns  = excluded.v6_checked_ns`,
		row.NodeHash, row.IPv4, row.IPv6, row.Colo, row.Loc,
		row.V4ObservedNs, row.V6ObservedNs, row.V6CheckedNs)
	return err
}

func appendEgressHistoryTx(ctx context.Context, tx *sql.Tx, nodeHash string, family int, ip string, atNs int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO egress_history (node_hash, family, ip, observed_at_ns) VALUES (?, ?, ?, ?)`,
		nodeHash, family, ip, atNs)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNodeEgress(row rowScanner) (NodeEgress, error) {
	var out NodeEgress
	err := row.Scan(&out.NodeHash, &out.IPv4, &out.IPv6, &out.Colo, &out.Loc,
		&out.V4ObservedNs, &out.V6ObservedNs, &out.V6CheckedNs)
	return out, err
}
