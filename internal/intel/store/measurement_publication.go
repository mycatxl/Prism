package store

import (
	"context"
	"time"
)

// SetMeasurementChanged publishes committed job state to the routing cache.
// Callbacks run after transactions release the single database connection.
func (s *Store) SetMeasurementChanged(hook func([]string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.measurementChanged = hook
}

func (s *Store) notifyMeasurementNodes(hashes []string) {
	s.mu.RLock()
	hook := s.measurementChanged
	s.mu.RUnlock()
	if hook != nil && len(hashes) > 0 {
		hook(hashes)
	}
}

// Bulk lifecycle operations publish a job's states once. Per-item operations
// use notifyMeasurementNodes, avoiding a whole-job scan for every step.
func (s *Store) notifyMeasurementJob(jobID string) {
	s.mu.RLock()
	hook := s.measurementChanged
	s.mu.RUnlock()
	if hook == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := `SELECT node_hash FROM node_measurements`
	var args []any
	if jobID != "" {
		query += ` WHERE job_id=?`
		args = append(args, jobID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return
	}
	var hashes []string
	for rows.Next() {
		var h string
		if rows.Scan(&h) != nil {
			break
		}
		hashes = append(hashes, h)
	}
	_ = rows.Close()
	s.notifyMeasurementNodes(hashes)
}

func (s *Store) ListAllMeasurementStates(ctx context.Context) (map[string]MeasurementStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_hash,status,error_code,attempts FROM node_measurements`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MeasurementStatus{}
	for rows.Next() {
		var h string
		var m MeasurementStatus
		if err := rows.Scan(&h, &m.State, &m.Error, &m.Attempts); err != nil {
			return nil, err
		}
		out[h] = m
	}
	return out, rows.Err()
}
