package store

import "context"

type MeasurementStatus struct {
	State    string
	Error    string
	Attempts int
}

// LatestMeasurementStates describes actual work, not the absence of check rows.
// A terminal egress failure and a node never scheduled are distinct outcomes.
func (s *Store) LatestMeasurementStates(ctx context.Context, hashes []string) (map[string]MeasurementStatus, error) {
	out := map[string]MeasurementStatus{}
	for start := 0; start < len(hashes); start += 400 {
		end := min(start+400, len(hashes))
		args := []any{}
		for _, h := range hashes[start:end] {
			args = append(args, h)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT node_hash,status,error_code,attempts FROM node_measurements WHERE node_hash IN (`+sqlPlaceholders(len(args))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var m MeasurementStatus
			if err := rows.Scan(&h, &m.State, &m.Error, &m.Attempts); err != nil {
				rows.Close()
				return nil, err
			}
			switch m.State {
			case ItemFailed:
				m.State = "measured_failed"
			case ItemDone:
				m.State = "complete"
			}
			out[h] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MissingFullMeasurements includes never-attempted nodes only. Terminal failures
// are real outcomes and must not silently receive another five attempts forever.
func (s *Store) MissingFullMeasurements(ctx context.Context, hashes []string) ([]string, error) {
	out := []string{}
	for _, h := range hashes {
		var n int
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_measurements WHERE node_hash=? AND full_attempted=1) OR EXISTS(SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=?)`, h, h).Scan(&n)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			out = append(out, h)
		}
	}
	return out, nil
}
