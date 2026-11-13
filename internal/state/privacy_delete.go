package state

import (
	"encoding/json"
	"fmt"
	"prism/internal/node"
	"prism/internal/subscription"
	"time"
)

type PrivacyDeletion struct {
	NodeHash       string `json:"node_hash"`
	Reason         string `json:"reason"`
	DeletedAtNs    int64  `json:"deleted_at_ns"`
	SubscriptionID string `json:"subscription_id"`
}

func (r *StateRepo) PrivacyDeletions() ([]PrivacyDeletion, error) {
	rows, err := r.db.Query(`SELECT node_hash,reason,deleted_at_ns,subscription_id FROM privacy_deletions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PrivacyDeletion{}
	for rows.Next() {
		var e PrivacyDeletion
		if err := rows.Scan(&e.NodeHash, &e.Reason, &e.DeletedAtNs, &e.SubscriptionID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnablePrivacyDeletion is called before runtime assembly and event replay.
func (r *StateRepo) EnablePrivacyDeletion() { r.privacyEnabled = true }

// Caller holds r.mu, the same lock as subscription writes and privacy purges.
func (r *StateRepo) scrubSubscription(id, content string) (string, error) {
	events, err := r.PrivacyDeletions()
	if err != nil {
		return "", err
	}
	if !r.privacyEnabled && len(events) == 0 {
		return content, nil
	}
	denied := map[string]string{}
	for _, e := range events {
		denied[e.NodeHash] = e.Reason
	}
	rejected := map[string]string{}
	clean, err := subscription.ScrubContent(content, func(h node.Hash, raw json.RawMessage) bool {
		reason := denied[h.Hex()]
		if reason == "" && r.privacyEnabled {
			reason = node.PrivacyDeletionReason(raw)
		}
		if reason != "" {
			rejected[h.Hex()] = reason
		}
		return reason != ""
	})
	if err != nil {
		return "", err
	}
	for h, reason := range rejected {
		if _, err := r.db.Exec(`INSERT OR IGNORE INTO privacy_deletions VALUES(?,?,?,?)`, h, reason, time.Now().UnixNano(), id); err != nil {
			return "", err
		}
	}
	return clean, nil
}

// RecordPrivacyDeletion journals first, then scrubs every subscription, including sources not
// currently enabled or referenced by a live entry. Repeating this is safe.
func (r *StateRepo) RecordPrivacyDeletion(hash, reason string, sources []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(sources) == 0 {
		sources = []string{""}
	}
	for _, id := range sources {
		if _, err := r.db.Exec(`INSERT OR IGNORE INTO privacy_deletions VALUES(?,?,?,?)`, hash, reason, time.Now().UnixNano(), id); err != nil {
			return err
		}
	}
	subs, err := r.ListSubscriptions()
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Content == "" {
			continue
		}
		clean, err := r.scrubSubscription(s.ID, s.Content)
		if err != nil {
			return fmt.Errorf("scrub subscription %s: %w", s.ID, err)
		}
		if clean != s.Content {
			if _, err := r.db.Exec(`UPDATE subscriptions SET content=?,last_parse_report_json='{}' WHERE id=?`, clean, s.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// PurgePrivacyNode serializes with an already-read flush snapshot and fences all
// later writes in SQLite itself. No node document is retained in the fence.
func (e *StateEngine) PurgePrivacyNode(hash string) error {
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	tx, err := e.CacheRepo.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO privacy_deleted_hashes VALUES(?)`, hash); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM nodes_static WHERE hash=?`, `DELETE FROM nodes_dynamic WHERE hash=?`,
		`DELETE FROM node_latency WHERE node_hash=?`, `DELETE FROM subscription_nodes WHERE node_hash=?`,
		`DELETE FROM leases WHERE node_hash=?`,
	} {
		if _, err = tx.Exec(q, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}
