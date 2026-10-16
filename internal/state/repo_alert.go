package state

import (
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"prism/internal/model"
)

// StateRepo methods for the alerts table created by 000019_alerts.
//
// Conventions match repo_invite.go: writes take r.mu, a missing row comes back
// as ErrNotFound and a duplicate id as ErrConflict.
//
// Acknowledging is the interesting half. It is one conditional UPDATE that
// matches only while the row is still open, so two operators acknowledging the
// same alert cannot both record a "read at": the second UPDATE matches no row
// and is refused.

const alertColumns = `id, kind, user_id, plan_id, subscription_id, window_minutes,
	threshold_ratio, used_bytes, quota_bytes, detected_at_ns, acknowledged_at_ns, note`

// InsertAlert persists a new alert and returns it.
//
// The returned row is the argument itself: every column of model.Alert is
// written, so reading it back would only re-read what was just stored. A
// duplicate id returns ErrConflict.
func (r *StateRepo) InsertAlert(a model.Alert) (model.Alert, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO alerts (id, kind, user_id, plan_id, subscription_id, window_minutes,
		                    threshold_ratio, used_bytes, quota_bytes, detected_at_ns,
		                    acknowledged_at_ns, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, a.Kind, a.UserID, a.PlanID, a.SubscriptionID, a.WindowMinutes,
		a.ThresholdRatio, a.UsedBytes, a.QuotaBytes, a.DetectedAtNs,
		a.AcknowledgedAtNs, a.Note)
	// A clashing id trips SQLite's primary-key constraint rather than its
	// unique-column one, so both codes mean the same thing here.
	if isSQLiteUniqueConstraint(err) || isSQLitePrimaryKeyConstraint(err) {
		return model.Alert{}, fmt.Errorf("%w: alert id already exists", ErrConflict)
	}
	if err != nil {
		return model.Alert{}, fmt.Errorf("insert alert: %w", err)
	}
	return a, nil
}

// ListAlerts returns the alert log, newest first.
//
// sinceNs of 0 means "no lower bound", which is what a caller with no window
// sends; unacknowledged keeps only the rows still waiting for an operator. A
// limit of 0 or less means "every matching row": the scan that suppresses a
// repeat alert has to see all the open ones, while the console passes its own
// page size.
func (r *StateRepo) ListAlerts(limit int, sinceNs int64, unacknowledged bool) ([]model.Alert, error) {
	query := "SELECT " + alertColumns + " FROM alerts WHERE detected_at_ns >= ?"
	if unacknowledged {
		query += " AND acknowledged_at_ns = 0"
	}
	query += " ORDER BY detected_at_ns DESC, id"
	args := []any{sinceNs}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	result := []model.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// AcknowledgeAlert marks one alert as read and returns the row afterwards.
//
// The claim is a single conditional UPDATE on acknowledged_at_ns = 0, so the
// first acknowledgement wins and a second one is refused with ErrNotFound rather
// than overwriting the timestamp of whoever read it first.
func (r *StateRepo) AcknowledgeAlert(id string, nowNs int64) (model.Alert, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(
		"UPDATE alerts SET acknowledged_at_ns = ? WHERE id = ? AND acknowledged_at_ns = 0",
		nowNs, id,
	)
	if err != nil {
		return model.Alert{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return model.Alert{}, ErrNotFound
	}

	row := r.db.QueryRow("SELECT "+alertColumns+" FROM alerts WHERE id = ?", id)
	a, err := scanAlert(row)
	if err == sql.ErrNoRows {
		return model.Alert{}, ErrNotFound
	}
	if err != nil {
		return model.Alert{}, fmt.Errorf("read acknowledged alert: %w", err)
	}
	return a, nil
}

// DeleteAlertsBefore removes every alert detected before the cutoff and reports
// how many rows went away.
//
// Retention is age-based on the detection time, not the acknowledgement time: an
// alert nobody ever read still ages out, which is what keeps the table bounded
// in a deployment where the console is rarely opened.
//
// The delete runs in batches of stateDeleteBatch rows, like the usage tables:
// the alerts table is on the same single write connection as logins and the
// flush worker, and one statement over an unbounded tail would hold it for the
// length of that tail.
func (r *StateRepo) DeleteAlertsBefore(cutoffNs int64) (int, error) {
	removed, err := r.deleteBeforeBatched("alerts", "detected_at_ns", cutoffNs)
	if err != nil {
		return int(removed), fmt.Errorf("delete alerts: %w", err)
	}
	return int(removed), nil
}

func scanAlert(row interface{ Scan(...any) error }) (model.Alert, error) {
	var a model.Alert
	err := row.Scan(&a.ID, &a.Kind, &a.UserID, &a.PlanID, &a.SubscriptionID,
		&a.WindowMinutes, &a.ThresholdRatio, &a.UsedBytes, &a.QuotaBytes,
		&a.DetectedAtNs, &a.AcknowledgedAtNs, &a.Note)
	return a, err
}

// isSQLitePrimaryKeyConstraint reports whether the error is a duplicate primary
// key. SQLite answers a clashing id with SQLITE_CONSTRAINT_PRIMARYKEY, a code
// isSQLiteUniqueConstraint does not cover, because a primary key is not always
// an index.
func isSQLitePrimaryKeyConstraint(err error) bool {
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	return sqlErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
