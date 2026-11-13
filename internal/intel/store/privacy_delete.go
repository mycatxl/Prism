package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PurgePrivacyNode is transactional. The permanent hash fence also rejects
// observations, job expansion and provider/check writes that were already in flight.
func (s *Store) PurgePrivacyNode(ctx context.Context, hash string) ([]string, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO privacy_deleted_hashes VALUES(?)`, hash); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ipv4 FROM node_egress WHERE node_hash=? UNION SELECT ipv6 FROM node_egress WHERE node_hash=? UNION SELECT ip FROM egress_history WHERE node_hash=? UNION SELECT ip FROM evidence WHERE via_node_hash=? UNION SELECT egress_ip FROM node_checks WHERE node_hash=?`, hash, hash, hash, hash, hash)
	if err != nil {
		return nil, err
	}
	ips := []string{}
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			rows.Close()
			return nil, err
		}
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, q := range []string{`DELETE FROM node_egress WHERE node_hash=?`, `DELETE FROM egress_history WHERE node_hash=?`, `DELETE FROM node_checks WHERE node_hash=?`, `DELETE FROM evidence WHERE via_node_hash=?`, `DELETE FROM job_items WHERE node_hash=?`, `DELETE FROM provider_node_state WHERE node_hash=?`, `DELETE FROM node_measurements WHERE node_hash=?`} {
		if _, err = tx.ExecContext(ctx, q, hash); err != nil {
			return nil, err
		}
	}
	// Remove explicit hash references from persisted requests without changing
	// an explicit selection into an all-nodes selection.
	jobs, err := tx.QueryContext(ctx, `SELECT id,request_json FROM jobs WHERE instr(request_json,?)>0`, hash)
	if err != nil {
		return nil, err
	}
	requests := map[string]string{}
	for jobs.Next() {
		var id, raw string
		if err = jobs.Scan(&id, &raw); err != nil {
			jobs.Close()
			return nil, err
		}
		requests[id] = raw
	}
	err = jobs.Err()
	jobs.Close()
	if err != nil {
		return nil, err
	}
	for id, raw := range requests {
		var v any
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		v = scrubHash(v, hash)
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET request_json=? WHERE id=?`, string(b), id); err != nil {
			return nil, err
		}
	}
	orphan := []string{}
	for _, ip := range ips {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM node_egress WHERE ipv4=? OR ipv6=?`, ip, ip).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		orphan = append(orphan, ip)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO privacy_deleted_ips VALUES(?)`, ip); err != nil {
			return nil, err
		}
		for _, q := range []string{`DELETE FROM ip_assessment WHERE ip=?`, `DELETE FROM evidence WHERE ip=?`, `DELETE FROM provider_queue WHERE ip=?`} {
			if _, err = tx.ExecContext(ctx, q, ip); err != nil {
				return nil, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET total=(SELECT count(*) FROM job_items WHERE job_id=jobs.id),done=(SELECT count(*) FROM job_items WHERE job_id=jobs.id AND status='done'),failed=(SELECT count(*) FROM job_items WHERE job_id=jobs.id AND status='failed'),skipped=(SELECT count(*) FROM job_items WHERE job_id=jobs.id AND status IN ('skipped','canceled'))`); err != nil {
		return nil, err
	}
	return orphan, tx.Commit()
}
func scrubHash(v any, h string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			x[k] = scrubHash(y, h)
		}
		return x
	case []any:
		out := []any{}
		for _, y := range x {
			if z, ok := y.(string); ok && z == h {
				continue
			}
			out = append(out, scrubHash(y, h))
		}
		return out
	}
	return v
}
func (s *Store) PrivacyDeleted(ctx context.Context, hash string) bool {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT node_hash FROM privacy_deleted_hashes WHERE node_hash=?`, hash).Scan(&h)
	return !errors.Is(err, sql.ErrNoRows)
}
func (s *Store) PrivacyIPDeleted(ip string) bool {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM privacy_deleted_ips WHERE ip=? AND NOT EXISTS(SELECT 1 FROM node_egress WHERE ipv4=? OR ipv6=?)`, ip, ip, ip).Scan(&n)
	return err != nil || n > 0
}
