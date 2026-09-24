package metrics

import (
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// retentionTables are the persisted metric tables that a pruning pass must cover.
var retentionTables = []string{
	"metric_traffic_bucket",
	"metric_request_bucket",
	"metric_access_latency_bucket",
	"metric_probe_bucket",
	"metric_node_pool_bucket",
	"metric_lease_lifetime_bucket",
}

func newRetentionRepo(t *testing.T) *MetricsRepo {
	t.Helper()
	repo, err := NewMetricsRepo(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("NewMetricsRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// writeRetentionBucket writes one row into every persisted metric table.
func writeRetentionBucket(t *testing.T, repo *MetricsRepo, bucket int64) {
	t.Helper()
	if err := repo.WriteBucket(&BucketFlushData{
		BucketStartUnix: bucket,
		Traffic:         trafficAccum{IngressBytes: 1, EgressBytes: 2},
		Requests:        map[string]requestAccum{"": {Total: 1, Success: 1}},
		Probes:          probeAccum{Total: 1},
		LeaseLifetimes:  map[string]*leaseLifeAccum{"plat-1": {Samples: []int64{int64(time.Second)}}},
	}); err != nil {
		t.Fatalf("WriteBucket(%d): %v", bucket, err)
	}
	if err := repo.WriteNodePoolSnapshot(bucket, 1, 1, 1); err != nil {
		t.Fatalf("WriteNodePoolSnapshot(%d): %v", bucket, err)
	}
	if err := repo.WriteLatencyBucket(bucket, "", []int64{1}); err != nil {
		t.Fatalf("WriteLatencyBucket(%d): %v", bucket, err)
	}
}

func countTableRows(t *testing.T, repo *MetricsRepo, table string) int {
	t.Helper()
	var rows int
	if err := repo.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return rows
}

func newestBucket(t *testing.T, repo *MetricsRepo, table string) int64 {
	t.Helper()
	var bucket int64
	if err := repo.db.QueryRow("SELECT MAX(bucket_start_unix) FROM " + table).Scan(&bucket); err != nil {
		t.Fatalf("max bucket_start_unix of %s: %v", table, err)
	}
	return bucket
}

// TestRetentionRules_CoverEveryPersistedMetricTable fails when a metrics table is
// added to the schema without a retention rule, which is how the tables were
// left unpruned before G-08.
func TestRetentionRules_CoverEveryPersistedMetricTable(t *testing.T) {
	createTable := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS ([a-z_]+)`)
	matches := createTable.FindAllStringSubmatch(MetricsDBDDL, -1)
	if len(matches) == 0 {
		t.Fatal("MetricsDBDDL declares no table")
	}

	rules := RetentionPolicy{}.rules()
	covered := make(map[string]bool, len(rules))
	for _, rule := range rules {
		covered[rule.table] = true
	}
	for _, match := range matches {
		if !covered[match[1]] {
			t.Fatalf("table %s has no retention rule", match[1])
		}
	}
	if len(rules) != len(matches) {
		t.Fatalf("retention rules: got %d, want one per table (%d)", len(rules), len(matches))
	}
}

// TestPruneExpired_RemovesRowsOlderThanRetention covers G-08: rows past the
// retention window are gone from every metric table, rows inside it survive.
func TestPruneExpired_RemovesRowsOlderThanRetention(t *testing.T) {
	repo := newRetentionRepo(t)
	policy := RetentionPolicy{ThroughputSeconds: 3600, ConnectionsSeconds: 18000, LeasesSeconds: 3600}
	now := time.Now().Unix()
	old := now - 7200
	recent := now - 60

	writeRetentionBucket(t, repo, old)
	writeRetentionBucket(t, repo, recent)

	removed, err := repo.PruneExpired(policy, now, RetentionPruneBatch)
	if err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	if removed != int64(len(retentionTables)) {
		t.Fatalf("removed rows: got %d, want %d", removed, len(retentionTables))
	}
	for _, table := range retentionTables {
		if rows := countTableRows(t, repo, table); rows != 1 {
			t.Fatalf("%s rows after pruning: got %d, want 1 (the recent bucket)", table, rows)
		}
		if bucket := newestBucket(t, repo, table); bucket != recent {
			t.Fatalf("%s surviving bucket: got %d, want %d", table, bucket, recent)
		}
	}
}

// TestPruneExpired_HonoursPerTableWindows covers the mapping of the published
// windows onto the tables: a bucket outside the throughput window but inside the
// leases window disappears from the throughput samples and stays in the lease
// lifetime table.
func TestPruneExpired_HonoursPerTableWindows(t *testing.T) {
	repo := newRetentionRepo(t)
	policy := RetentionPolicy{ThroughputSeconds: 3600, ConnectionsSeconds: 18000, LeasesSeconds: 18000}
	now := time.Now().Unix()
	bucket := now - 3*3600

	writeRetentionBucket(t, repo, bucket)

	if _, err := repo.PruneExpired(policy, now, RetentionPruneBatch); err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	for _, table := range retentionTables {
		wantRows := 1
		if table != "metric_lease_lifetime_bucket" {
			wantRows = 0
		}
		if rows := countTableRows(t, repo, table); rows != wantRows {
			t.Fatalf("%s rows after pruning: got %d, want %d", table, rows, wantRows)
		}
	}
}

// TestPruneExpired_BoundsRowsPerPass proves the work per pass is bounded: one
// statement never deletes more than maxRowsPerTable rows, and the following pass
// continues where it stopped.
func TestPruneExpired_BoundsRowsPerPass(t *testing.T) {
	repo := newRetentionRepo(t)
	policy := RetentionPolicy{ThroughputSeconds: 3600, ConnectionsSeconds: 18000, LeasesSeconds: 3600}
	now := time.Now().Unix()
	for i := 0; i < 5; i++ {
		writeRetentionBucket(t, repo, now-7200+int64(i))
	}

	removed, err := repo.PruneExpired(policy, now, 2)
	if err != nil {
		t.Fatalf("first PruneExpired: %v", err)
	}
	// Two rows per table, six tables.
	if removed != 12 {
		t.Fatalf("first pass removed: got %d, want 12", removed)
	}
	if rows := countTableRows(t, repo, "metric_traffic_bucket"); rows != 3 {
		t.Fatalf("traffic rows after the first pass: got %d, want 3", rows)
	}

	// The next passes finish the job.
	for i := 0; i < 3; i++ {
		if _, err := repo.PruneExpired(policy, now, 2); err != nil {
			t.Fatalf("follow-up PruneExpired %d: %v", i, err)
		}
	}
	for _, table := range retentionTables {
		if rows := countTableRows(t, repo, table); rows != 0 {
			t.Fatalf("%s rows after the follow-up passes: got %d, want 0", table, rows)
		}
	}
}

// TestStartRetentionPruner_PrunesOnStartup covers the schedule: the first pass
// runs immediately instead of waiting for the first tick.
func TestStartRetentionPruner_PrunesOnStartup(t *testing.T) {
	repo := newRetentionRepo(t)
	policy := RetentionPolicy{ThroughputSeconds: 3600, ConnectionsSeconds: 18000, LeasesSeconds: 3600}
	now := time.Now().Unix()
	writeRetentionBucket(t, repo, now-7200)

	stop := StartRetentionPruner(repo, policy, time.Hour)
	defer stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := countTableRows(t, repo, "metric_traffic_bucket"); rows == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expired rows survived the startup pass: %d rows", countTableRows(t, repo, "metric_traffic_bucket"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
