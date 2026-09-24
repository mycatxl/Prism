package metrics

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Metrics retention (G-08).
//
// PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS,
// PRISM_METRIC_CONNECTIONS_RETENTION_SECONDS and
// PRISM_METRIC_LEASES_RETENTION_SECONDS are published by
// GET /api/v1/system/config/env and rendered in the WebUI. Until now they only
// sized the in-memory realtime rings (cmd/prism/main.go,
// deriveMetricsManagerSettings) while the persisted bucket tables of metrics.db
// grew without any bound. RetentionPolicy applies the same windows to the
// persisted history:
//
//   - metric_traffic_bucket, metric_request_bucket, metric_access_latency_bucket,
//     metric_probe_bucket and metric_node_pool_bucket carry the per-bucket
//     samples of the throughput/observability stream, so they are bounded by the
//     throughput window;
//   - metric_lease_lifetime_bucket is bounded by the leases window;
//   - the connections stream has no persisted table — the realtime ring is its
//     only store and its capacity is already derived from the connections
//     window — so nothing else is pruned for it.
//
// An operator who wants a longer dashboard history raises the matching window;
// the values the WebUI shows are the values that are honoured.
type RetentionPolicy struct {
	ThroughputSeconds  int
	ConnectionsSeconds int
	LeasesSeconds      int
}

// Defaults mirror internal/config/env.go; they are used when a window is not
// configured.
const (
	defaultThroughputRetentionSeconds  = 3600
	defaultConnectionsRetentionSeconds = 18000
	defaultLeasesRetentionSeconds      = 18000
)

// RetentionPruneBatch bounds how many rows one pruning statement deletes per
// table, so a pass never holds the SQLite write lock for long on a large table.
// Rows that do not fit in one pass are removed by the following pass.
const RetentionPruneBatch = 2000

// RetentionPruneInterval is the schedule of the background pruning pass. It is
// well below the smallest default retention window, so the tables stay inside
// their window even when the values are small.
const RetentionPruneInterval = 5 * time.Minute

// ThroughputWindow is how long the throughput/observability samples are kept.
func (p RetentionPolicy) ThroughputWindow() time.Duration {
	return secondsToDuration(p.ThroughputSeconds, defaultThroughputRetentionSeconds)
}

// ConnectionsWindow is how long the realtime connections samples are kept.
func (p RetentionPolicy) ConnectionsWindow() time.Duration {
	return secondsToDuration(p.ConnectionsSeconds, defaultConnectionsRetentionSeconds)
}

// LeasesWindow is how long the lease lifetime samples are kept.
func (p RetentionPolicy) LeasesWindow() time.Duration {
	return secondsToDuration(p.LeasesSeconds, defaultLeasesRetentionSeconds)
}

func secondsToDuration(seconds, fallback int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	return time.Duration(seconds) * time.Second
}

// retentionRule pairs a persisted bucket table with the window that bounds it.
type retentionRule struct {
	table   string
	seconds int
}

// rules lists every persisted metric table, so a table can never be forgotten
// when a new one is added here.
func (p RetentionPolicy) rules() []retentionRule {
	throughput := int(p.ThroughputWindow() / time.Second)
	leases := int(p.LeasesWindow() / time.Second)
	return []retentionRule{
		{"metric_traffic_bucket", throughput},
		{"metric_request_bucket", throughput},
		{"metric_access_latency_bucket", throughput},
		{"metric_probe_bucket", throughput},
		{"metric_node_pool_bucket", throughput},
		{"metric_lease_lifetime_bucket", leases},
	}
}

// PruneExpired deletes the bucket rows that are older than the retention window
// of their table and returns how many rows were removed. The work per pass is
// bounded by maxRowsPerTable (RetentionPruneBatch when <= 0): every statement
// deletes at most that many rows, so a pass cannot block the metric writers for
// long. No VACUUM is issued for the same reason.
func (r *MetricsRepo) PruneExpired(policy RetentionPolicy, nowUnix int64, maxRowsPerTable int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	if maxRowsPerTable <= 0 {
		maxRowsPerTable = RetentionPruneBatch
	}
	var removed int64
	for _, rule := range policy.rules() {
		cutoff := nowUnix - int64(rule.seconds)
		deleted, err := r.pruneTable(rule.table, cutoff, maxRowsPerTable)
		if err != nil {
			return removed, err
		}
		removed += deleted
	}
	return removed, nil
}

// pruneTable removes at most limit rows of table older than cutoffUnix. The
// table name comes from retentionRule, never from user input.
func (r *MetricsRepo) pruneTable(table string, cutoffUnix int64, limit int) (int64, error) {
	stmt := fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (
		SELECT rowid FROM %s WHERE bucket_start_unix < ? ORDER BY bucket_start_unix LIMIT ?)`,
		table, table)
	res, err := r.db.Exec(stmt, cutoffUnix, limit)
	if err != nil {
		return 0, fmt.Errorf("prune %s: %w", table, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune %s rows: %w", table, err)
	}
	return affected, nil
}

// StartRetentionPruner runs one pruning pass immediately and then every interval
// until the returned stop function is called. A nil repo makes it a no-op.
func StartRetentionPruner(repo *MetricsRepo, policy RetentionPolicy, interval time.Duration) func() {
	if repo == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = RetentionPruneInterval
	}
	done := make(chan struct{})
	var once sync.Once

	prune := func() {
		removed, err := repo.PruneExpired(policy, time.Now().Unix(), RetentionPruneBatch)
		if err != nil {
			log.Printf("[metrics] retention prune failed: %v", err)
			return
		}
		if removed > 0 {
			log.Printf("[metrics] retention prune removed %d bucket rows", removed)
		}
	}

	go func() {
		prune()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				prune()
			case <-done:
				return
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}
