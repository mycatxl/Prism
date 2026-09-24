package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provider budget/queue sentinel errors. Callers translate them into a
// next_run_at for the queued item instead of blocking a worker (§3.3, R4).
var (
	ErrProviderPaused   = errors.New("provider is paused")
	ErrProviderBlocked  = errors.New("provider is blocked")
	ErrBudgetExhausted  = errors.New("provider daily budget exhausted")
	ErrProviderNotReady = errors.New("provider rate limit not reached")
)

// BudgetRequest describes one budget consumption attempt.
type BudgetRequest struct {
	Provider     string
	Day          string  // UTC YYYY-MM-DD
	DailyLimit   int     // 0 = unlimited
	QPS          float64 // 0 = unlimited
	NowNs        int64
	CredentialID string // changing it clears a pause (§3.3, WP09 §1)
}

// DayString renders a UTC day key (YYYY-MM-DD) for provider_state.day.
func DayString(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// GetProviderState returns the persisted state of one provider.
func (s *Store) GetProviderState(ctx context.Context, provider string) (ProviderState, bool, error) {
	db, err := s.conn()
	if err != nil {
		return ProviderState{}, false, err
	}
	row, err := scanProviderState(db.QueryRowContext(ctx, providerStateSelect+` WHERE provider = ?`, provider))
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderState{Provider: provider}, false, nil
	}
	if err != nil {
		return ProviderState{}, false, err
	}
	return row, true, nil
}

// ListProviderStates returns every persisted provider state.
func (s *Store) ListProviderStates(ctx context.Context) ([]ProviderState, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, providerStateSelect+` ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderState
	for rows.Next() {
		row, err := scanProviderState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpsertProviderState writes a full provider_state row.
func (s *Store) UpsertProviderState(ctx context.Context, st ProviderState) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(st.Provider) == "" {
		return fmt.Errorf("upsert provider state: provider is required")
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO provider_state (provider, day, used, next_request_at_ns, blocked_until_ns,
			paused, error_code, credential_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			day                = excluded.day,
			used               = excluded.used,
			next_request_at_ns = excluded.next_request_at_ns,
			blocked_until_ns   = excluded.blocked_until_ns,
			paused             = excluded.paused,
			error_code         = excluded.error_code,
			credential_id      = excluded.credential_id`,
		st.Provider, st.Day, st.Used, st.NextRequestAtNs, st.BlockedUntilNs,
		boolToInt(st.Paused), st.ErrorCode, st.CredentialID)
	return err
}

// ConsumeProviderBudget atomically rolls the day counter, verifies the pause,
// block and budget state, then reserves one request. provider_state.used and
// provider_state.next_request_at_ns are updated in the same transaction (§3.3).
//
// The returned ProviderState is always populated when the call fails with one of
// the sentinel errors, so the caller can schedule the next attempt.
func (s *Store) ConsumeProviderBudget(ctx context.Context, req BudgetRequest) (ProviderState, error) {
	db, err := s.conn()
	if err != nil {
		return ProviderState{}, err
	}
	if strings.TrimSpace(req.Provider) == "" {
		return ProviderState{}, fmt.Errorf("consume provider budget: provider is required")
	}
	if req.NowNs <= 0 {
		return ProviderState{}, fmt.Errorf("consume provider budget: now_ns is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderState{}, err
	}
	defer func() { _ = tx.Rollback() }()

	st, err := scanProviderState(tx.QueryRowContext(ctx, providerStateSelect+` WHERE provider = ?`, req.Provider))
	if errors.Is(err, sql.ErrNoRows) {
		st = ProviderState{Provider: req.Provider, Day: req.Day, CredentialID: req.CredentialID}
	} else if err != nil {
		return ProviderState{}, err
	}

	// A rotated credential automatically lifts a pause: the previous 401/403
	// was caused by the old key.
	if req.CredentialID != "" && req.CredentialID != st.CredentialID {
		st.Paused = false
		st.ErrorCode = ""
		st.CredentialID = req.CredentialID
	}
	if req.Day != "" && req.Day != st.Day {
		st.Day = req.Day
		st.Used = 0
		st.NextRequestAtNs = 0
	}

	switch {
	case st.Paused:
		if err := upsertProviderStateTx(ctx, tx, st); err != nil {
			return ProviderState{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProviderState{}, err
		}
		return st, ErrProviderPaused
	case st.BlockedUntilNs > req.NowNs:
		if err := upsertProviderStateTx(ctx, tx, st); err != nil {
			return ProviderState{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProviderState{}, err
		}
		return st, ErrProviderBlocked
	case req.DailyLimit > 0 && st.Used >= req.DailyLimit:
		if err := upsertProviderStateTx(ctx, tx, st); err != nil {
			return ProviderState{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProviderState{}, err
		}
		return st, ErrBudgetExhausted
	case st.NextRequestAtNs > req.NowNs:
		if err := upsertProviderStateTx(ctx, tx, st); err != nil {
			return ProviderState{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProviderState{}, err
		}
		return st, ErrProviderNotReady
	}

	st.Used++
	if req.QPS > 0 {
		st.NextRequestAtNs = req.NowNs + int64(float64(time.Second)/req.QPS)
	}
	if err := upsertProviderStateTx(ctx, tx, st); err != nil {
		return ProviderState{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProviderState{}, err
	}
	return st, nil
}

// MarkProviderBlocked records a 429 cooldown.
func (s *Store) MarkProviderBlocked(ctx context.Context, provider string, untilNs int64, errorCode string) error {
	return s.patchProviderState(ctx, provider, func(st *ProviderState) {
		st.BlockedUntilNs = untilNs
		st.ErrorCode = errorCode
	})
}

// MarkProviderPaused records a 401/403 state. credentialID lets a later key
// rotation clear the pause automatically.
func (s *Store) MarkProviderPaused(ctx context.Context, provider, errorCode, credentialID string) error {
	return s.patchProviderState(ctx, provider, func(st *ProviderState) {
		st.Paused = true
		st.ErrorCode = errorCode
		if credentialID != "" {
			st.CredentialID = credentialID
		}
	})
}

// ResumeProvider clears the paused flag (POST /providers/{id}/actions/resume).
func (s *Store) ResumeProvider(ctx context.Context, provider string) error {
	return s.patchProviderState(ctx, provider, func(st *ProviderState) {
		st.Paused = false
		st.BlockedUntilNs = 0
		st.ErrorCode = ""
	})
}

func (s *Store) patchProviderState(ctx context.Context, provider string, apply func(*ProviderState)) error {
	if strings.TrimSpace(provider) == "" {
		return fmt.Errorf("provider state: provider is required")
	}
	db, err := s.conn()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	st, err := scanProviderState(tx.QueryRowContext(ctx, providerStateSelect+` WHERE provider = ?`, provider))
	if errors.Is(err, sql.ErrNoRows) {
		st = ProviderState{Provider: provider}
	} else if err != nil {
		return err
	}
	apply(&st)
	if err := upsertProviderStateTx(ctx, tx, st); err != nil {
		return err
	}
	return tx.Commit()
}

// EnqueueMode controls how EnqueueProviderItem merges with an existing row.
type EnqueueMode int

const (
	// EnqueueIfMissing inserts a new lookup and otherwise only raises priority.
	EnqueueIfMissing EnqueueMode = iota
	// EnqueueRefresh also resets done/failed rows back to queued.
	EnqueueRefresh
	// EnqueueForce resets any row back to queued.
	EnqueueForce
)

// EnqueueProviderItem de-duplicates online lookups by (provider, ip) and returns
// whether a new row was inserted (§3.2 step 3).
func (s *Store) EnqueueProviderItem(ctx context.Context, item QueueItem, mode EnqueueMode) (bool, error) {
	db, err := s.conn()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(item.Provider) == "" || strings.TrimSpace(item.IP) == "" {
		return false, fmt.Errorf("enqueue provider item: provider and ip are required")
	}
	if item.NextRunAtNs == 0 {
		item.NextRunAtNs = item.EnqueuedAtNs
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = QueueQueued
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanQueueItem(tx.QueryRowContext(ctx,
		queueItemSelect+` WHERE provider = ? AND ip = ?`, item.Provider, item.IP))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO provider_queue (provider, ip, priority, job_id, status, attempts,
				next_run_at_ns, lease_owner, lease_until_ns, error_code, enqueued_at_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?)`,
			item.Provider, item.IP, item.Priority, item.JobID, item.Status, item.Attempts,
			item.NextRunAtNs, item.ErrorCode, item.EnqueuedAtNs); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	case err != nil:
		return false, err
	}

	merged := existing
	if item.Priority > merged.Priority {
		merged.Priority = item.Priority
	}
	if item.JobID != "" {
		merged.JobID = item.JobID
	}
	reset := mode == EnqueueForce ||
		(mode == EnqueueRefresh && (existing.Status == QueueDone || existing.Status == QueueFailed))
	if reset {
		merged.Status = QueueQueued
		merged.Attempts = 0
		merged.ErrorCode = ""
		merged.LeaseOwner = ""
		merged.LeaseUntilNs = 0
		merged.NextRunAtNs = item.NextRunAtNs
	} else if existing.Status == QueueQueued && item.NextRunAtNs < merged.NextRunAtNs {
		merged.NextRunAtNs = item.NextRunAtNs
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE provider_queue SET priority = ?, job_id = ?, status = ?, attempts = ?,
			next_run_at_ns = ?, lease_owner = ?, lease_until_ns = ?, error_code = ?
		WHERE provider = ? AND ip = ?`,
		merged.Priority, merged.JobID, merged.Status, merged.Attempts, merged.NextRunAtNs,
		merged.LeaseOwner, merged.LeaseUntilNs, merged.ErrorCode, merged.Provider, merged.IP); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

// ClaimProviderItems leases up to limit due items of one provider. Items whose
// lease expired are reclaimable, so a crashed worker cannot strand work (§3.5).
func (s *Store) ClaimProviderItems(ctx context.Context, provider string, nowNs, leaseUntilNs int64, limit int, owner string) ([]QueueItem, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, queueItemSelect+`
		WHERE provider = ?
		  AND ((status = 'queued' AND next_run_at_ns <= ?) OR (status = 'running' AND lease_until_ns <= ?))
		ORDER BY priority DESC, enqueued_at_ns ASC
		LIMIT ?`, provider, nowNs, nowNs, limit)
	if err != nil {
		return nil, err
	}
	var claimed []QueueItem
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range claimed {
		claimed[i].Status = QueueRunning
		claimed[i].LeaseOwner = owner
		claimed[i].LeaseUntilNs = leaseUntilNs
		if _, err := tx.ExecContext(ctx, `
			UPDATE provider_queue SET status = ?, lease_owner = ?, lease_until_ns = ?, error_code = ''
			WHERE provider = ? AND ip = ?`,
			QueueRunning, owner, leaseUntilNs, claimed[i].Provider, claimed[i].IP); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// ResolveProviderItem marks one queued lookup done and clears its lease.
func (s *Store) ResolveProviderItem(ctx context.Context, provider, ip string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE provider_queue SET status = ?, lease_owner = '', lease_until_ns = 0, error_code = ''
		WHERE provider = ? AND ip = ?`, QueueDone, provider, ip)
	return err
}

// FailProviderItem records a failed attempt. terminal marks the row failed,
// otherwise it is retried at nextRunAtNs with the exponential backoff the worker
// computed (§3.3).
func (s *Store) FailProviderItem(ctx context.Context, provider, ip string, attempts int, nextRunAtNs int64, errorCode string, terminal bool) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	status := QueueQueued
	if terminal {
		status = QueueFailed
	}
	_, err = db.ExecContext(ctx, `
		UPDATE provider_queue SET status = ?, attempts = ?, next_run_at_ns = ?,
			lease_owner = '', lease_until_ns = 0, error_code = ?
		WHERE provider = ? AND ip = ?`,
		status, attempts, nextRunAtNs, errorCode, provider, ip)
	return err
}

// RequeueProviderItem returns a leased item to the queue without counting an
// attempt. It is used when the budget or QPS gate is closed (§3.2 step 4).
func (s *Store) RequeueProviderItem(ctx context.Context, provider, ip string, nextRunAtNs int64, errorCode string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE provider_queue SET status = ?, next_run_at_ns = ?, lease_owner = '', lease_until_ns = 0,
			error_code = ?
		WHERE provider = ? AND ip = ?`,
		QueueQueued, nextRunAtNs, errorCode, provider, ip)
	return err
}

// ProviderQueueCounts is the per-provider queue summary used by
// GET /api/v1/intel/providers and GET /api/v1/intel/status.
type ProviderQueueCounts struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// ProviderQueueCounts returns the queue length by status for one provider.
func (s *Store) ProviderQueueCounts(ctx context.Context, provider string) (ProviderQueueCounts, error) {
	db, err := s.conn()
	if err != nil {
		return ProviderQueueCounts{}, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM provider_queue WHERE provider = ? GROUP BY status`, provider)
	if err != nil {
		return ProviderQueueCounts{}, err
	}
	defer rows.Close()

	var out ProviderQueueCounts
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return ProviderQueueCounts{}, err
		}
		switch status {
		case QueueQueued:
			out.Queued = count
		case QueueRunning:
			out.Running = count
		case QueueDone:
			out.Done = count
		case QueueFailed:
			out.Failed = count
		}
	}
	return out, rows.Err()
}

// CountPendingOnlineLookups counts queue rows still owned by one job (§3.5).
func (s *Store) CountPendingOnlineLookups(ctx context.Context, jobID string) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var count int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_queue WHERE job_id = ? AND status IN ('queued','running')`, jobID).Scan(&count)
	return count, err
}

// ResetStaleProviderItems clears leases left behind by a crash and returns the
// number of rows that went back to queued (§3.5).
func (s *Store) ResetStaleProviderItems(ctx context.Context) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE provider_queue SET status = 'queued', lease_owner = '', lease_until_ns = 0
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupProviderQueue deletes done rows older than doneBeforeNs and failed rows
// older than failedBeforeNs (§6).
func (s *Store) CleanupProviderQueue(ctx context.Context, doneBeforeNs, failedBeforeNs int64) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, stmt := range []struct {
		sql string
		arg int64
	}{
		{`DELETE FROM provider_queue WHERE status = 'done' AND next_run_at_ns <= ?`, doneBeforeNs},
		{`DELETE FROM provider_queue WHERE status = 'failed' AND next_run_at_ns <= ?`, failedBeforeNs},
	} {
		res, err := db.ExecContext(ctx, stmt.sql, stmt.arg)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// DeleteJobQueueItems removes the queue rows owned by one job (job deletion).
func (s *Store) DeleteJobQueueItems(ctx context.Context, jobID string) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM provider_queue WHERE job_id = ?`, jobID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const providerStateSelect = `SELECT provider, day, used, next_request_at_ns, blocked_until_ns,
	paused, error_code, credential_id FROM provider_state`

const queueItemSelect = `SELECT provider, ip, priority, job_id, status, attempts,
	next_run_at_ns, lease_owner, lease_until_ns, error_code, enqueued_at_ns FROM provider_queue`

func scanProviderState(row rowScanner) (ProviderState, error) {
	var st ProviderState
	var paused int
	if err := row.Scan(&st.Provider, &st.Day, &st.Used, &st.NextRequestAtNs, &st.BlockedUntilNs,
		&paused, &st.ErrorCode, &st.CredentialID); err != nil {
		return ProviderState{}, err
	}
	st.Paused = paused != 0
	return st, nil
}

func scanQueueItem(row rowScanner) (QueueItem, error) {
	var item QueueItem
	err := row.Scan(&item.Provider, &item.IP, &item.Priority, &item.JobID, &item.Status,
		&item.Attempts, &item.NextRunAtNs, &item.LeaseOwner, &item.LeaseUntilNs,
		&item.ErrorCode, &item.EnqueuedAtNs)
	return item, err
}

func upsertProviderStateTx(ctx context.Context, tx *sql.Tx, st ProviderState) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO provider_state (provider, day, used, next_request_at_ns, blocked_until_ns,
			paused, error_code, credential_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			day                = excluded.day,
			used               = excluded.used,
			next_request_at_ns = excluded.next_request_at_ns,
			blocked_until_ns   = excluded.blocked_until_ns,
			paused             = excluded.paused,
			error_code         = excluded.error_code,
			credential_id      = excluded.credential_id`,
		st.Provider, st.Day, st.Used, st.NextRequestAtNs, st.BlockedUntilNs,
		boolToInt(st.Paused), st.ErrorCode, st.CredentialID)
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
