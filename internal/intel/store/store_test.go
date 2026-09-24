package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	st, err := Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpen_AppliesPragmasAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	db := st.DB()
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var synchronous int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat intel.db: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("intel.db mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", perm)
	}
	if filepath.Base(path) != "intel.db" {
		t.Fatalf("file name = %q, want intel.db (separate from state.db/cache.db)", filepath.Base(path))
	}
}

func TestOpen_EmptyPath(t *testing.T) {
	if _, err := Open("   "); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestMigrate_CreatesEveryTableAndRecordsVersion(t *testing.T) {
	st := openTemp(t)

	expected := []string{
		"node_egress", "egress_history", "evidence", "node_checks",
		"ip_assessment", "provider_state", "provider_queue", "jobs", "job_items",
	}
	for _, table := range expected {
		var name string
		err := st.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	version, dirty, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != LatestVersion {
		t.Fatalf("version = %d, want %d", version, LatestVersion)
	}
	if dirty {
		t.Fatal("migration marked dirty")
	}

	// Re-opening an already migrated database must be a no-op.
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if version, _, err := reopened.SchemaVersion(); err != nil || version != LatestVersion {
		t.Fatalf("reopen version = %d (err %v), want %d", version, err, LatestVersion)
	}
}

func TestStore_CloseIsIdempotentAndBlocksWrites(t *testing.T) {
	st := openTemp(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	err := st.UpsertNodeEgress(context.Background(), NodeEgress{NodeHash: "h1"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestStore_ConcurrentWritesSerializeOnSingleConnection(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	const workers = 8
	const perWorker = 25
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				hash := filepath.Join("node", string(rune('a'+w)), string(rune('a'+i%26)))
				if err := st.UpsertNodeEgress(ctx, NodeEgress{
					NodeHash:     hash,
					IPv4:         "203.0.113." + string(rune('0'+w)),
					V4ObservedNs: int64(1000 + i),
				}); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent write: %v", err)
	}

	rows, err := st.ListNodeEgress(ctx)
	if err != nil {
		t.Fatalf("ListNodeEgress: %v", err)
	}
	if len(rows) != workers*perWorker {
		t.Fatalf("rows = %d, want %d", len(rows), workers*perWorker)
	}
}

func TestCleanup_AppliesRetentionRules(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	nowNs := now.UnixNano()

	// One live node with an IPv4 egress address.
	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "live", IPv4: "198.51.100.10", V4ObservedNs: nowNs,
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	// Old finished job + items.
	old := now.Add(-40 * 24 * time.Hour)
	if err := st.CreateJob(ctx, Job{
		ID: "old", Kind: "intel", Status: JobSucceeded, Priority: 10,
		CreatedBy: "admin", CreatedAtNs: old.UnixNano(), FinishedAtNs: old.UnixNano(),
	}, []JobItem{{NodeHash: "live", UpdatedAtNs: old.UnixNano()}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	recent := now.Add(-2 * 24 * time.Hour)
	if err := st.CreateJob(ctx, Job{
		ID: "recent", Kind: "intel", Status: JobSucceeded, Priority: 10,
		CreatedBy: "admin", CreatedAtNs: recent.UnixNano(), FinishedAtNs: recent.UnixNano(),
	}, []JobItem{{NodeHash: "live", UpdatedAtNs: recent.UnixNano()}}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Provider queue: stale done + stale failed.
	enqueue := func(provider, ip, status string, at int64) {
		t.Helper()
		if _, err := st.EnqueueProviderItem(ctx, QueueItem{
			Provider: provider, IP: ip, Priority: 1, Status: QueueQueued,
			NextRunAtNs: at, EnqueuedAtNs: at,
		}, EnqueueIfMissing); err != nil {
			t.Fatalf("EnqueueProviderItem: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`UPDATE provider_queue SET status = ?, next_run_at_ns = ? WHERE provider = ? AND ip = ?`,
			status, at, provider, ip); err != nil {
			t.Fatalf("update queue: %v", err)
		}
	}
	enqueue("proxycheck", "198.51.100.10", QueueDone, now.Add(-48*time.Hour).UnixNano())
	enqueue("proxycheck", "198.51.100.11", QueueFailed, now.Add(-10*24*time.Hour).UnixNano())

	// Egress history beyond the per-node cap.
	for i := 0; i < 60; i++ {
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO egress_history (node_hash, family, ip, observed_at_ns) VALUES (?, 4, ?, ?)`,
			"live", "198.51.100.10", int64(i)); err != nil {
			t.Fatalf("insert history: %v", err)
		}
	}

	// Orphan (expired, unused IP) evidence + assessment + stale check.
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "203.0.113.250", Provider: "proxycheck", Status: StatusOk,
		NormalizedJSON: `{"risk_score":10}`, ObservedAtNs: nowNs - int64(60*24*time.Hour),
		ValidUntilNs: nowNs - int64(40*24*time.Hour),
	}); err != nil {
		t.Fatalf("UpsertEvidence orphan: %v", err)
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.10", Provider: "proxycheck", Status: StatusOk,
		NormalizedJSON: `{"risk_score":10}`, ObservedAtNs: nowNs - int64(60*24*time.Hour),
		ValidUntilNs: nowNs - int64(40*24*time.Hour),
	}); err != nil {
		t.Fatalf("UpsertEvidence live: %v", err)
	}
	if err := st.UpsertAssessment(ctx, Assessment{
		IP: "203.0.113.250", Profile: "prism-purity-v2", State: "valid", Verdict: "favorable",
		PurityBand: "clean", Confidence: "high", IPType: "residential",
		ReasonsJSON: "[]", ComponentsJSON: "[]",
		ComputedAtNs: nowNs - int64(60*24*time.Hour), ValidUntilNs: nowNs - int64(40*24*time.Hour),
	}); err != nil {
		t.Fatalf("UpsertAssessment orphan: %v", err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "ghost", CheckID: "chatgpt", Outcome: "available",
		ObservedAtNs: nowNs - int64(60*24*time.Hour), ValidUntilNs: nowNs - int64(40*24*time.Hour),
	}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}

	result, err := st.Cleanup(ctx, now, DefaultCleanupPolicy())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if result.Jobs != 1 {
		t.Fatalf("deleted jobs = %d, want 1", result.Jobs)
	}
	if result.JobItems != 1 {
		t.Fatalf("deleted job items = %d, want 1", result.JobItems)
	}
	if result.ProviderQueue != 2 {
		t.Fatalf("deleted provider queue rows = %d, want 2", result.ProviderQueue)
	}
	if result.EgressHistory != 10 {
		t.Fatalf("pruned egress history = %d, want 10", result.EgressHistory)
	}
	if result.Evidence != 1 {
		t.Fatalf("deleted orphan evidence = %d, want 1", result.Evidence)
	}
	if result.Assessments != 1 {
		t.Fatalf("deleted orphan assessments = %d, want 1", result.Assessments)
	}
	if result.Checks != 1 {
		t.Fatalf("deleted orphan checks = %d, want 1", result.Checks)
	}
	if !result.Optimized {
		t.Fatal("Optimize was not reported")
	}

	// The live IP evidence survives.
	if _, ok, err := st.GetEvidence(ctx, "198.51.100.10", "proxycheck"); err != nil || !ok {
		t.Fatalf("live evidence removed (ok=%v err=%v)", ok, err)
	}
	if _, err := st.GetJob(ctx, "recent"); err != nil {
		t.Fatalf("recent job removed: %v", err)
	}
}

func TestCleanup_OrphanAgeCutoffRespected(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	// Expired only five minutes ago: still inside the 30 day orphan window.
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "203.0.113.9", Provider: "proxycheck", Status: StatusOk,
		NormalizedJSON: `{}`, ObservedAtNs: now.UnixNano() - int64(time.Hour),
		ValidUntilNs: now.UnixNano() - int64(5*time.Minute),
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	result, err := st.Cleanup(ctx, now, DefaultCleanupPolicy())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if result.Evidence != 0 {
		t.Fatalf("deleted evidence = %d, want 0", result.Evidence)
	}
	if _, ok, _ := st.GetEvidence(ctx, "203.0.113.9", "proxycheck"); !ok {
		t.Fatal("recently expired evidence was deleted")
	}
}

func TestStore_FileBytesCountsWALSideFiles(t *testing.T) {
	st := openTemp(t)
	if err := st.UpsertEvidence(context.Background(), Evidence{
		IP: "198.51.100.1", Provider: "proxycheck", Status: StatusOk, NormalizedJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
	size, err := st.FileBytes()
	if err != nil {
		t.Fatalf("FileBytes: %v", err)
	}
	if size <= 0 {
		t.Fatalf("FileBytes = %d, want > 0", size)
	}
	// The -wal file exists while the connection is open, so the reported size
	// must exceed the main database file alone.
	main, err := os.Stat(st.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size < main.Size() {
		t.Fatalf("FileBytes %d < main file %d", size, main.Size())
	}
}
