package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrJobNotFound is returned when a job id does not exist.
var ErrJobNotFound = errors.New("job not found")

// CreateJob inserts a job and its node items in one transaction.
func (s *Store) CreateJob(ctx context.Context, job Job, items []JobItem) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("create job: id is required")
	}
	if job.Status == "" {
		job.Status = JobQueued
	}
	if job.RequestJSON == "" {
		job.RequestJSON = "{}"
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, status, priority, request_json, total, done, failed, skipped,
			created_by, created_at_ns, started_at_ns, finished_at_ns, error)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?, ?)`,
		job.ID, job.Kind, job.Status, job.Priority, job.RequestJSON, len(items),
		job.CreatedBy, job.CreatedAtNs, job.StartedAtNs, job.FinishedAtNs, job.Error); err != nil {
		return err
	}

	if len(items) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO job_items (job_id, node_hash, status, step_index, attempts, next_run_at_ns,
				lease_owner, lease_until_ns, result_json, error_code, updated_at_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, item := range items {
			status := item.Status
			if status == "" {
				status = ItemQueued
			}
			if _, err := stmt.ExecContext(ctx, job.ID, item.NodeHash, status, item.StepIndex,
				item.Attempts, item.NextRunAtNs, item.LeaseOwner, item.LeaseUntilNs,
				truncateJSON(item.ResultJSON, MaxResultJSONBytes), item.ErrorCode, item.UpdatedAtNs); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// GetJob returns one job row.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	db, err := s.conn()
	if err != nil {
		return Job{}, err
	}
	job, err := scanJob(db.QueryRowContext(ctx, jobSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	return job, err
}

// ListJobs returns jobs filtered by status (empty = all) with pagination.
func (s *Store) ListJobs(ctx context.Context, status string, limit, offset int) ([]Job, error) {
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
	query := jobSelect
	args := []any{}
	if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at_ns DESC, id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectJobs(rows)
}

// CountJobs counts jobs, optionally filtered by status.
func (s *Store) CountJobs(ctx context.Context, status string) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var count int
	if strings.TrimSpace(status) == "" {
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&count)
	} else {
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status = ?`, status).Scan(&count)
	}
	return count, err
}

// ListActiveJobs returns queued and running jobs ordered by priority, which the
// job manager uses to respect intel_max_running_jobs (§3.4).
func (s *Store) ListActiveJobs(ctx context.Context, limit int) ([]Job, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx,
		jobSelect+` WHERE status IN ('queued','running') ORDER BY priority DESC, created_at_ns ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectJobs(rows)
}

// CountActiveJobs counts queued and running jobs.
func (s *Store) CountActiveJobs(ctx context.Context) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&count)
	return count, err
}

// MarkJobRunning promotes a queued job to running and stamps started_at_ns.
func (s *Store) MarkJobRunning(ctx context.Context, id string, nowNs int64) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, started_at_ns = CASE WHEN started_at_ns = 0 THEN ? ELSE started_at_ns END
		WHERE id = ? AND status IN ('queued','running')`, JobRunning, nowNs, id)
	return err
}

// CancelJob marks a job canceled; the workers stop after the current step (§3.5).
func (s *Store) CancelJob(ctx context.Context, id string) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ?`, JobCanceled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrJobNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE job_items SET status = ? WHERE job_id = ? AND status = ?`,
		ItemCanceled, id, ItemQueued); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryFailedJobItems requeues failed items and reopens the job (§3.5).
func (s *Store) RetryFailedJobItems(ctx context.Context, id string, nowNs int64) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE job_items SET status = ?, attempts = 0, next_run_at_ns = ?, lease_owner = '',
			lease_until_ns = 0, error_code = '', updated_at_ns = ?
		WHERE job_id = ? AND status = ?`, ItemQueued, nowNs, nowNs, id, ItemFailed)
	if err != nil {
		return 0, err
	}
	retried, _ := res.RowsAffected()

	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = ?, finished_at_ns = 0 WHERE id = ? AND status IN ('partial','failed','canceled')`,
		JobQueued, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return retried, nil
}

// JobItemFilter selects job items for listing.
type JobItemFilter struct {
	JobID  string
	Status string
	Limit  int
	Offset int
}

// ListJobItems returns the items of one job.
func (s *Store) ListJobItems(ctx context.Context, filter JobItemFilter) ([]JobItem, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := jobItemSelect + ` WHERE job_id = ?`
	args := []any{filter.JobID}
	if strings.TrimSpace(filter.Status) != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY node_hash LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectJobItems(rows)
}

// CountJobItems counts the items of one job, optionally filtered by status.
func (s *Store) CountJobItems(ctx context.Context, jobID, status string) (int, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var count int
	if strings.TrimSpace(status) == "" {
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_items WHERE job_id = ?`, jobID).Scan(&count)
	} else {
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_items WHERE job_id = ? AND status = ?`, jobID, status).Scan(&count)
	}
	return count, err
}

// GetJobItem returns one item.
func (s *Store) GetJobItem(ctx context.Context, jobID, nodeHash string) (JobItem, bool, error) {
	db, err := s.conn()
	if err != nil {
		return JobItem{}, false, err
	}
	item, err := scanJobItem(db.QueryRowContext(ctx, jobItemSelect+` WHERE job_id = ? AND node_hash = ?`, jobID, nodeHash))
	if errors.Is(err, sql.ErrNoRows) {
		return JobItem{}, false, nil
	}
	if err != nil {
		return JobItem{}, false, err
	}
	return item, true, nil
}

// ClaimOptions bounds one claim round of the node worker pool.
type ClaimOptions struct {
	NowNs        int64
	LeaseUntilNs int64
	Limit        int
	Owner        string
	// JobIDs restricts the claim to specific jobs (job-scoped workers). An empty
	// slice means "every queued or running job".
	JobIDs []string
}

// ClaimJobItems leases due items and stamps the owning jobs as running.
func (s *Store) ClaimJobItems(ctx context.Context, opts ClaimOptions) ([]JobItem, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	if opts.Limit <= 0 {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Items whose lease expired are reclaimable: an item lease is three minutes
	// while a single step may take at most 90 seconds (§3.4), so an expired lease
	// always means the owning worker died.
	args := []any{opts.NowNs, opts.NowNs}
	query := jobItemSelect + "\n\t\tWHERE ((status = 'queued' AND next_run_at_ns <= ?)" +
		"\n\t\t   OR (status = 'running' AND lease_until_ns <= ?))\n\t\t  AND job_id IN ("
	if len(opts.JobIDs) > 0 {
		query += placeholders(len(opts.JobIDs))
		for _, id := range opts.JobIDs {
			args = append(args, id)
		}
	} else {
		query += "SELECT id FROM jobs WHERE status IN ('queued','running')"
	}
	query += `)
		ORDER BY (SELECT priority FROM jobs WHERE jobs.id = job_items.job_id) DESC,
		         next_run_at_ns ASC, job_id, node_hash
		LIMIT ?`
	args = append(args, opts.Limit)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var claimed []JobItem
	for rows.Next() {
		item, err := scanJobItem(rows)
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

	jobs := make(map[string]struct{})
	for i := range claimed {
		claimed[i].Status = ItemRunning
		claimed[i].LeaseOwner = opts.Owner
		claimed[i].LeaseUntilNs = opts.LeaseUntilNs
		claimed[i].Attempts++
		if _, err := tx.ExecContext(ctx, `
			UPDATE job_items SET status = ?, lease_owner = ?, lease_until_ns = ?, attempts = ?
			WHERE job_id = ? AND node_hash = ?`,
			ItemRunning, opts.Owner, opts.LeaseUntilNs, claimed[i].Attempts,
			claimed[i].JobID, claimed[i].NodeHash); err != nil {
			return nil, err
		}
		jobs[claimed[i].JobID] = struct{}{}
	}
	for id := range jobs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = ?, started_at_ns = CASE WHEN started_at_ns = 0 THEN ? ELSE started_at_ns END
			WHERE id = ? AND status = 'queued'`, JobRunning, opts.NowNs, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// SaveJobItemStep stores the pipeline breakpoint after a successful step so a
// restarted process continues at the next step (§3.2, §3.5).
func (s *Store) SaveJobItemStep(ctx context.Context, jobID, nodeHash string, stepIndex int, resultJSON string, leaseUntilNs, updatedAtNs int64) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE job_items SET status = ?, step_index = ?, result_json = ?, error_code = '',
			lease_until_ns = ?, updated_at_ns = ?
		WHERE job_id = ? AND node_hash = ?`,
		ItemRunning, stepIndex, truncateJSON(resultJSON, MaxResultJSONBytes), leaseUntilNs, updatedAtNs,
		jobID, nodeHash)
	return err
}

// DeferJobItem returns an item to the queue with a later next_run_at without
// recording a failure (budget or QPS gate closed, §3.2 step 4).
func (s *Store) DeferJobItem(ctx context.Context, jobID, nodeHash string, stepIndex int, nextRunAtNs int64, resultJSON string, updatedAtNs int64) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE job_items SET status = ?, step_index = ?, next_run_at_ns = ?, result_json = ?,
			lease_owner = '', lease_until_ns = 0, updated_at_ns = ?
		WHERE job_id = ? AND node_hash = ?`,
		ItemQueued, stepIndex, nextRunAtNs, truncateJSON(resultJSON, MaxResultJSONBytes), updatedAtNs,
		jobID, nodeHash)
	return err
}

// FinishJobItem moves an item to a terminal status and clears its lease.
func (s *Store) FinishJobItem(ctx context.Context, jobID, nodeHash, status, errorCode, resultJSON string, updatedAtNs int64) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE job_items SET status = ?, error_code = ?, result_json = ?, lease_owner = '',
			lease_until_ns = 0, updated_at_ns = ?
		WHERE job_id = ? AND node_hash = ?`,
		status, errorCode, truncateJSON(resultJSON, MaxResultJSONBytes), updatedAtNs, jobID, nodeHash)
	return err
}

// ResetRunningJobItems clears leases left behind by a crash and returns the
// number of items that went back to queued. Running jobs stay running so the
// worker pool continues them after a restart (§3.5).
func (s *Store) ResetRunningJobItems(ctx context.Context) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE job_items SET status = 'queued', lease_owner = '', lease_until_ns = 0
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// JobCounters is the raw per-status item tally of one job.
type JobCounters struct {
	Total    int
	Queued   int
	Running  int
	Done     int
	Failed   int
	Skipped  int
	Canceled int
}

// JobItemCounters tallies the item statuses of one job.
func (s *Store) JobItemCounters(ctx context.Context, jobID string) (JobCounters, error) {
	db, err := s.conn()
	if err != nil {
		return JobCounters{}, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM job_items WHERE job_id = ? GROUP BY status`, jobID)
	if err != nil {
		return JobCounters{}, err
	}
	defer rows.Close()

	var counters JobCounters
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return JobCounters{}, err
		}
		counters.Total += count
		switch status {
		case ItemQueued:
			counters.Queued = count
		case ItemRunning:
			counters.Running = count
		case ItemDone:
			counters.Done = count
		case ItemFailed:
			counters.Failed = count
		case ItemSkipped:
			counters.Skipped = count
		case ItemCanceled:
			counters.Canceled = count
		}
	}
	return counters, rows.Err()
}

// RefreshJob recomputes the job counters from its items and settles the job
// status once every item is terminal (§3.5). It returns the updated job.
func (s *Store) RefreshJob(ctx context.Context, jobID string, nowNs int64) (Job, error) {
	db, err := s.conn()
	if err != nil {
		return Job{}, err
	}
	counters, err := s.JobItemCounters(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return Job{}, err
	}

	pending := counters.Queued + counters.Running
	status := job.Status
	finishedAt := job.FinishedAtNs
	switch {
	case status == JobCanceled:
		if finishedAt == 0 {
			finishedAt = nowNs
		}
	case pending > 0:
		if status == JobQueued && counters.Running > 0 {
			status = JobRunning
		}
	case counters.Failed > 0:
		status = JobPartial
		finishedAt = nowNs
	default:
		status = JobSucceeded
		finishedAt = nowNs
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, total = ?, done = ?, failed = ?, skipped = ?,
			finished_at_ns = ? WHERE id = ?`,
		status, counters.Total, counters.Done, counters.Failed, counters.Skipped, finishedAt, jobID); err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, jobID)
}

// JobProgressView aggregates the counters plus the pending online lookups of a
// job for GET /api/v1/intel/jobs/{id} and the SSE stream.
func (s *Store) JobProgressView(ctx context.Context, jobID string) (JobProgress, error) {
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return JobProgress{}, err
	}
	counters, err := s.JobItemCounters(ctx, jobID)
	if err != nil {
		return JobProgress{}, err
	}
	pendingLookups, err := s.CountPendingOnlineLookups(ctx, jobID)
	if err != nil {
		return JobProgress{}, err
	}
	return JobProgress{
		JobID:              job.ID,
		Status:             job.Status,
		Total:              counters.Total,
		Done:               counters.Done,
		Failed:             counters.Failed,
		Skipped:            counters.Skipped,
		Canceled:           counters.Canceled,
		Pending:            counters.Queued + counters.Running,
		PendingOnlineItems: pendingLookups,
	}, nil
}

// DeleteJobItemsFinishedBefore deletes the items of jobs that finished before
// beforeNs (§6: keep seven days).
func (s *Store) DeleteJobItemsFinishedBefore(ctx context.Context, beforeNs int64) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		DELETE FROM job_items WHERE job_id IN (
			SELECT id FROM jobs WHERE finished_at_ns > 0 AND finished_at_ns <= ?
		)`, beforeNs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteJobsFinishedBefore deletes finished jobs older than beforeNs along with
// their items and queue rows (§6: jobs are kept 30 days).
func (s *Store) DeleteJobsFinishedBefore(ctx context.Context, beforeNs int64) (int64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM job_items WHERE job_id IN (
			SELECT id FROM jobs WHERE finished_at_ns > 0 AND finished_at_ns <= ?
		)`, beforeNs); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM provider_queue WHERE job_id IN (
			SELECT id FROM jobs WHERE finished_at_ns > 0 AND finished_at_ns <= ?
		)`, beforeNs); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		DELETE FROM jobs WHERE finished_at_ns > 0 AND finished_at_ns <= ?`, beforeNs)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

const jobSelect = `SELECT id, kind, status, priority, request_json, total, done, failed, skipped,
	created_by, created_at_ns, started_at_ns, finished_at_ns, error FROM jobs`

const jobItemSelect = `SELECT job_id, node_hash, status, step_index, attempts, next_run_at_ns,
	lease_owner, lease_until_ns, result_json, error_code, updated_at_ns FROM job_items`

func collectJobs(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func collectJobItems(rows *sql.Rows) ([]JobItem, error) {
	defer rows.Close()
	var out []JobItem
	for rows.Next() {
		item, err := scanJobItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanJob(row rowScanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.Kind, &job.Status, &job.Priority, &job.RequestJSON,
		&job.Total, &job.Done, &job.Failed, &job.Skipped, &job.CreatedBy,
		&job.CreatedAtNs, &job.StartedAtNs, &job.FinishedAtNs, &job.Error)
	return job, err
}

func scanJobItem(row rowScanner) (JobItem, error) {
	var item JobItem
	err := row.Scan(&item.JobID, &item.NodeHash, &item.Status, &item.StepIndex, &item.Attempts,
		&item.NextRunAtNs, &item.LeaseOwner, &item.LeaseUntilNs, &item.ResultJSON,
		&item.ErrorCode, &item.UpdatedAtNs)
	return item, err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}
