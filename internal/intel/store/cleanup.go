package store

import (
	"context"
	"database/sql"
	"time"
)

// CleanupPolicy bounds every retention rule of §6. A zero duration disables the
// corresponding rule so callers can run partial maintenance passes.
type CleanupPolicy struct {
	JobItemsAfter            time.Duration // 7d
	JobsAfter                time.Duration // 30d
	ProviderQueueDoneAfter   time.Duration // 1d
	ProviderQueueFailedAfter time.Duration // 7d
	OrphanAfter              time.Duration // 30d
	EgressHistoryPerNode     int           // 50
	ProviderNodeStateAfter   time.Duration // 7d
}

// DefaultCleanupPolicy returns the retention rules of WP08 §6.
func DefaultCleanupPolicy() CleanupPolicy {
	return CleanupPolicy{
		JobItemsAfter:            7 * 24 * time.Hour,
		JobsAfter:                30 * 24 * time.Hour,
		ProviderQueueDoneAfter:   24 * time.Hour,
		ProviderQueueFailedAfter: 7 * 24 * time.Hour,
		OrphanAfter:              30 * 24 * time.Hour,
		EgressHistoryPerNode:     50,
		ProviderNodeStateAfter:   7 * 24 * time.Hour,
	}
}

// CleanupResult reports how many rows each rule removed.
type CleanupResult struct {
	JobItems      int64 `json:"job_items"`
	Jobs          int64 `json:"jobs"`
	ProviderQueue int64 `json:"provider_queue"`
	EgressHistory int64 `json:"egress_history"`
	Evidence      int64 `json:"evidence"`
	Checks        int64 `json:"checks"`
	Assessments   int64 `json:"assessments"`
	// ProviderNodeState counts the per-node via-node budget rows of past days.
	ProviderNodeState int64 `json:"provider_node_state"`
	Vacuumed          bool  `json:"vacuumed"`
	Optimized         bool  `json:"optimized"`
}

// Cleanup applies the retention rules of §6. Orphan deletion uses the node
// hashes and IPs still referenced by node_egress, so intel.db never keeps data
// for IPs that no node uses any more (R4: every table stays bounded).
func (s *Store) Cleanup(ctx context.Context, now time.Time, policy CleanupPolicy) (CleanupResult, error) {
	db, err := s.conn()
	if err != nil {
		return CleanupResult{}, err
	}
	nowNs := now.UTC().UnixNano()
	var result CleanupResult

	if policy.JobItemsAfter > 0 {
		n, err := s.DeleteJobItemsFinishedBefore(ctx, nowNs-int64(policy.JobItemsAfter))
		if err != nil {
			return result, err
		}
		result.JobItems = n
	}
	if policy.JobsAfter > 0 {
		n, err := s.DeleteJobsFinishedBefore(ctx, nowNs-int64(policy.JobsAfter))
		if err != nil {
			return result, err
		}
		result.Jobs = n
	}
	if policy.ProviderQueueDoneAfter > 0 || policy.ProviderQueueFailedAfter > 0 {
		doneBefore := int64(0)
		if policy.ProviderQueueDoneAfter > 0 {
			doneBefore = nowNs - int64(policy.ProviderQueueDoneAfter)
		}
		failedBefore := int64(0)
		if policy.ProviderQueueFailedAfter > 0 {
			failedBefore = nowNs - int64(policy.ProviderQueueFailedAfter)
		}
		n, err := s.CleanupProviderQueue(ctx, doneBefore, failedBefore)
		if err != nil {
			return result, err
		}
		result.ProviderQueue = n
	}
	if policy.EgressHistoryPerNode > 0 {
		n, err := s.PruneEgressHistory(ctx, policy.EgressHistoryPerNode)
		if err != nil {
			return result, err
		}
		result.EgressHistory = n
	}
	if policy.OrphanAfter > 0 {
		cutoff := nowNs - int64(policy.OrphanAfter)
		orphans, err := s.countOrphansTx(ctx, db, cutoff)
		if err != nil {
			return result, err
		}
		result.Evidence = orphans.Evidence
		result.Checks = orphans.Checks
		result.Assessments = orphans.Assessments
	}

	if policy.ProviderNodeStateAfter > 0 {
		dayCutoff := now.UTC().Add(-policy.ProviderNodeStateAfter).Format("2006-01-02")
		n, err := s.DeleteProviderNodeStateBefore(ctx, dayCutoff)
		if err != nil {
			return result, err
		}
		result.ProviderNodeState = n
	}

	if err := s.Optimize(); err != nil {
		return result, err
	}
	result.Optimized = true

	vacuumed, err := s.VacuumIfFragmented(0.2)
	if err != nil {
		return result, err
	}
	result.Vacuumed = vacuumed

	return result, nil
}

type orphanCounts struct {
	Evidence    int64
	Checks      int64
	Assessments int64
}

// countOrphansTx deletes evidence/checks/assessments that expired before cutoff
// and are not referenced by any node_egress row.
func (s *Store) countOrphansTx(ctx context.Context, db *sql.DB, cutoff int64) (orphanCounts, error) {
	var out orphanCounts

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	// Evidence of an IP that no live node egresses from.
	res, err := tx.ExecContext(ctx, `
		DELETE FROM evidence
		WHERE valid_until_ns <= ?
		  AND ip NOT IN (SELECT ipv4 FROM node_egress WHERE ipv4 <> '')
		  AND ip NOT IN (SELECT ipv6 FROM node_egress WHERE ipv6 <> '')`, cutoff)
	if err != nil {
		return out, err
	}
	out.Evidence, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx, `
		DELETE FROM node_checks
		WHERE valid_until_ns <= ?
		  AND node_hash NOT IN (SELECT node_hash FROM node_egress)`, cutoff)
	if err != nil {
		return out, err
	}
	out.Checks, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx, `
		DELETE FROM ip_assessment
		WHERE valid_until_ns <= ?
		  AND ip NOT IN (SELECT ipv4 FROM node_egress WHERE ipv4 <> '')
		  AND ip NOT IN (SELECT ipv6 FROM node_egress WHERE ipv6 <> '')`, cutoff)
	if err != nil {
		return out, err
	}
	out.Assessments, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

// DeleteProviderNodeStateBefore deletes the per-node via-node budget rows whose
// day rolled over before the cutoff. A row only ever carries a same-day
// counter, so a stale one is dead weight (R4: every table stays bounded).
func (s *Store) DeleteProviderNodeStateBefore(ctx context.Context, dayCutoff string) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM provider_node_state WHERE day < ?`, dayCutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
