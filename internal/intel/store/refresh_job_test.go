package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The tests in this file pin the RefreshJob contract: it recomputed a job from
// two independent reads (JobItemCounters, then GetJob) and wrote the result back
// with an unconditional UPDATE. Two publishers of the same job - two workers
// that finished their own item, or a worker and the timeout sweep - could
// interleave so the slower one overwrote the newer settlement with the counters
// it had read before the other item finished: total/done/failed/skipped rolled
// back and status could fall from partial to running, which makes ListActiveJobs
// report the job again and takes one of the max_running_jobs slots (manager.go).
//
// The fix is an optimistic guarded write:
// the UPDATE carries the counters of the job row it read, so a concurrent writer
// makes it match no row, and refreshJob re-reads and recomputes, bounded by
// refreshJobPublishAttempts.

// TestRefreshJob_ReadsTheRowBeforeTheCounters pins the read order that makes the
// guarded UPDATE sound.
//
// The guard compares the row it writes against the row refreshJob read, so that
// snapshot must never be newer than the counters it publishes. With the reverse
// order - counters first, row second - a publisher can take its counters before
// another publisher settled the job and its row after: it then holds a fresh row
// and stale counters at once, the guard matches the row against itself, and the
// settlement is overwritten by the counters from before it. That is the CI
// failure this test pins: a succeeded job with done=2 written back as done=1,
// because the stale counters still showed a pending item.
//
// The seam commits the other publisher's settlement in exactly that window, so
// the assertions below fail if the two reads are ever swapped back.
func TestRefreshJob_ReadsTheRowBeforeTheCounters(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := int64(1_000)
	if err := st.CreateJob(ctx,
		Job{ID: "j", Kind: "intel", Status: JobRunning, CreatedAtNs: now},
		[]JobItem{
			{JobID: "j", NodeHash: "n1", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "n2", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// The publisher under test reads the row, and the other publisher then
	// finishes its item and settles the job before the counters are read.
	var fired atomic.Bool
	st.jobReadHook = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		if err := st.FinishJobItem(ctx, "j", "n1", ItemDone, "", "{}", now); err != nil {
			t.Errorf("FinishJobItem inside the window: %v", err)
			return
		}
		if err := st.FinishJobItem(ctx, "j", "n2", ItemDone, "", "{}", now); err != nil {
			t.Errorf("FinishJobItem inside the window: %v", err)
			return
		}
		// The other publisher settles the job row itself: both items are
		// terminal, so its publish writes succeeded/done=2.
		if _, err := st.refreshJob(ctx, "j", now, 0); err != nil {
			t.Errorf("competing publish inside the window: %v", err)
		}
	}

	job, err := st.RefreshJob(ctx, "j", now)
	st.jobReadHook = nil
	if err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}

	counters, err := st.JobItemCounters(ctx, "j")
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("published: status=%s total=%d done=%d (items total=%d done=%d)",
		job.Status, job.Total, job.Done, counters.Total, counters.Done)

	if job.Done != 2 || job.Total != 2 {
		t.Errorf("jobs row total=%d done=%d, want 2/2: the counters were read before the "+
			"competing settlement and published over it", job.Total, job.Done)
	}
	if job.Status != JobSucceeded {
		t.Errorf("job = %s, want %s", job.Status, JobSucceeded)
	}

	// The stored row must agree with its items, which is the invariant the API
	// and the scheduler read.
	stored, err := st.GetJob(ctx, "j")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if stored.Total != counters.Total || stored.Done != counters.Done ||
		stored.Failed != counters.Failed || stored.Skipped != counters.Skipped {
		t.Errorf("stored jobs row total=%d done=%d failed=%d skipped=%d contradicts the items "+
			"total=%d done=%d failed=%d skipped=%d",
			stored.Total, stored.Done, stored.Failed, stored.Skipped,
			counters.Total, counters.Done, counters.Failed, counters.Skipped)
	}
}

// TestRefreshJob_ConcurrentPublishKeepsCountersInSync is the stochastic form of
// the check: rounds of two workers that each finish one item of the same job
// and then publish it, asserting the invariant the API and the scheduler rely on
// - the counters of the jobs row are exactly the item tally, and a job whose
// items are all terminal is terminal itself.
//
// The window is a few instructions wide (measured at roughly 1/300 rounds), so a
// green run here is not proof on its own:
// TestRefreshJob_PublishInterleavedWithFinishDoesNotLoseCounters holds one
// publisher inside the window with the refreshHook seam and is the deterministic
// half of the pair.
func TestRefreshJob_ConcurrentPublishKeepsCountersInSync(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	const rounds = 300
	regressions := 0
	var lastSeen Job
	for round := 0; round < rounds; round++ {
		jobID := fmt.Sprintf("job-%d", round)
		now := int64(1000 + round)
		if err := st.CreateJob(ctx,
			Job{ID: jobID, Kind: "intel", Status: JobRunning, CreatedAtNs: now},
			[]JobItem{
				{JobID: jobID, NodeHash: "n1", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
				{JobID: jobID, NodeHash: "n2", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
			}); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, hash := range []string{"n1", "n2"} {
			wg.Add(1)
			go func(hash string) {
				defer wg.Done()
				<-start
				if err := st.FinishJobItem(ctx, jobID, hash, ItemDone, "", "{}", now); err != nil {
					errs <- fmt.Errorf("FinishJobItem(%s): %w", hash, err)
					return
				}
				if _, err := st.RefreshJob(ctx, jobID, now); err != nil {
					errs <- fmt.Errorf("RefreshJob(%s): %w", hash, err)
				}
			}(hash)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}

		job, err := st.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("round %d: GetJob: %v", round, err)
		}
		counters, err := st.JobItemCounters(ctx, jobID)
		if err != nil {
			t.Fatalf("round %d: JobItemCounters: %v", round, err)
		}
		lastSeen = job
		if job.Total != counters.Total || job.Done != counters.Done ||
			job.Failed != counters.Failed || job.Skipped != counters.Skipped {
			regressions++
			if regressions <= 3 {
				t.Logf("round %d: jobs row total=%d done=%d failed=%d skipped=%d, items total=%d done=%d failed=%d skipped=%d (status=%s)",
					round, job.Total, job.Done, job.Failed, job.Skipped,
					counters.Total, counters.Done, counters.Failed, counters.Skipped, job.Status)
			}
			continue
		}
		if job.Status != JobSucceeded || job.FinishedAtNs == 0 {
			regressions++
			if regressions <= 3 {
				t.Logf("round %d: every item is terminal but the job is status=%s finished_at=%d",
					round, job.Status, job.FinishedAtNs)
			}
		}
	}

	t.Logf("rounds=%d regressions=%d (last: status=%s total=%d done=%d)",
		rounds, regressions, lastSeen.Status, lastSeen.Total, lastSeen.Done)
	if regressions > 0 {
		t.Errorf("%d/%d rounds left the jobs row contradicting its items: a concurrent publish overwrote the newer counters",
			regressions, rounds)
	}
}

// TestRefreshJob_PublishInterleavedWithFinishDoesNotLoseCounters is the
// deterministic form of the same race: worker A finishes n1 and enters the window
// between RefreshJob's reads and its write (the refreshHook seam), worker B
// finishes n2 and publishes to completion while A is held there, and only then
// does A resume.
//
// With the guarded write A's UPDATE matches no row (the row it read carried
// done=1, the committed row carries done=2), so A recomputes from the item table
// and the job settles as succeeded with done=2. Without the guard A writes the
// counters it read before B committed: status falls back to running, done rolls
// back to 1, and a job whose items are all done keeps holding a
// max_running_jobs slot until something publishes again.
func TestRefreshJob_PublishInterleavedWithFinishDoesNotLoseCounters(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := int64(1_000)
	if err := st.CreateJob(ctx,
		Job{ID: "j", Kind: "intel", Status: JobRunning, CreatedAtNs: now},
		[]JobItem{
			{JobID: "j", NodeHash: "n1", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "n2", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Only the first publisher is held: B's own publish has to run to completion
	// inside the window A is sitting in.
	entered := make(chan struct{})
	release := make(chan struct{})
	var held atomic.Bool
	st.refreshHook = func() {
		if held.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}

	doneA := make(chan error, 1)
	go func() {
		if err := st.FinishJobItem(ctx, "j", "n1", ItemDone, "", "{}", now); err != nil {
			doneA <- fmt.Errorf("worker A FinishJobItem: %w", err)
			return
		}
		_, err := st.RefreshJob(ctx, "j", now)
		doneA <- err
	}()

	select {
	case <-entered:
	case err := <-doneA:
		t.Fatalf("worker A never reached the window (err %v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker A never reached the window: the refreshHook seam did not fire")
	}

	// Worker B: the second item finishes and publishes while A is held.
	if err := st.FinishJobItem(ctx, "j", "n2", ItemDone, "", "{}", now); err != nil {
		t.Fatalf("worker B FinishJobItem: %v", err)
	}
	if _, err := st.RefreshJob(ctx, "j", now); err != nil {
		t.Fatalf("worker B RefreshJob: %v", err)
	}
	settledByB, err := st.GetJob(ctx, "j")
	if err != nil {
		t.Fatalf("GetJob after B: %v", err)
	}
	if settledByB.Status != JobSucceeded || settledByB.Done != 2 {
		t.Fatalf("worker B left status=%s done=%d, want succeeded/2: the interleaving is wrong",
			settledByB.Status, settledByB.Done)
	}

	close(release)
	select {
	case err := <-doneA:
		if err != nil {
			t.Fatalf("worker A RefreshJob: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker A never returned after the window was released")
	}
	st.refreshHook = nil

	job, err := st.GetJob(ctx, "j")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	counters, err := st.JobItemCounters(ctx, "j")
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("after both publishes: status=%s total=%d done=%d failed=%d skipped=%d finished_at=%d (items total=%d done=%d)",
		job.Status, job.Total, job.Done, job.Failed, job.Skipped, job.FinishedAtNs,
		counters.Total, counters.Done)

	if job.Total != counters.Total || job.Done != counters.Done ||
		job.Failed != counters.Failed || job.Skipped != counters.Skipped {
		t.Errorf("jobs row total=%d done=%d failed=%d skipped=%d contradicts the items total=%d done=%d failed=%d skipped=%d: the held publisher wrote the counters it read before the other one committed",
			job.Total, job.Done, job.Failed, job.Skipped,
			counters.Total, counters.Done, counters.Failed, counters.Skipped)
	}
	if job.Status != JobSucceeded || job.Done != 2 || job.FinishedAtNs == 0 {
		t.Errorf("job = status=%s done=%d finished_at=%d, want succeeded/2 with a finish time: the stale publisher rolled the settled job back to an active status",
			job.Status, job.Done, job.FinishedAtNs)
	}
	if job.Active() {
		t.Errorf("job is %s again, so ListActiveJobs reports it and it holds a max_running_jobs slot", job.Status)
	}
}

// TestRefreshJob_StopsRetryingWhenPublishersKeepColliding pins the retry bound:
// the guarded write cannot win while another writer keeps changing a guarded
// counter, and RefreshJob must give up instead of recursing forever. It returns
// the job it last read and writes nothing - the collision is not its to resolve,
// and the writer that owns the newer counters publishes on its own turn.
func TestRefreshJob_StopsRetryingWhenPublishersKeepColliding(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := int64(1_000)
	if err := st.CreateJob(ctx,
		Job{ID: "j", Kind: "intel", Status: JobRunning, CreatedAtNs: now},
		[]JobItem{
			{JobID: "j", NodeHash: "n1", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Every attempt collides: a competing publisher bumps `done` inside the
	// window, which is exactly what makes the guarded UPDATE match no row.
	var attempts atomic.Int32
	st.refreshHook = func() {
		n := attempts.Add(1)
		if _, err := st.DB().ExecContext(ctx, `UPDATE jobs SET done = ? WHERE id = ?`, n, "j"); err != nil {
			t.Errorf("colliding write: %v", err)
		}
	}

	job, err := st.RefreshJob(ctx, "j", now)
	st.refreshHook = nil
	if err != nil {
		t.Fatalf("RefreshJob: %v", err)
	}

	if got := attempts.Load(); got != refreshJobPublishAttempts {
		t.Errorf("publish attempts = %d, want %d: the retry loop is not bounded by refreshJobPublishAttempts",
			got, refreshJobPublishAttempts)
	}
	row, err := st.GetJob(ctx, "j")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if row.Done != int(attempts.Load()) {
		t.Errorf("jobs row done = %d, want %d (the colliding writer's value): RefreshJob wrote after giving up",
			row.Done, attempts.Load())
	}
	if job.Done != int(attempts.Load())-1 {
		t.Errorf("returned job done = %d, want %d: a bounded give-up must return the job it last read",
			job.Done, attempts.Load()-1)
	}
}

// TestRefreshJob_PublishInterleavedWithCancelKeepsTheJobCanceled pins the other
// half of the guard: the WHERE clause has to carry every column the UPDATE
// writes, not only the counters. CancelJob rewrites `status` (and settles the
// items) without touching total/done/failed/skipped, so a guard made of the
// counters alone still matches after a cancel: a publisher that read the job
// before the operator canceled it writes status=running back and the canceled
// job becomes active again - it reappears in ListActiveJobs and holds a
// max_running_jobs slot, while its items are all canceled and nothing will ever
// finish it.
func TestRefreshJob_PublishInterleavedWithCancelKeepsTheJobCanceled(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := int64(1_000)
	if err := st.CreateJob(ctx,
		Job{ID: "j", Kind: "intel", Status: JobRunning, CreatedAtNs: now},
		[]JobItem{
			{JobID: "j", NodeHash: "n1", Status: ItemRunning, NextRunAtNs: now, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "n2", Status: ItemQueued, NextRunAtNs: now, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var held atomic.Bool
	st.refreshHook = func() {
		if held.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := st.RefreshJob(ctx, "j", now)
		done <- err
	}()

	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the publisher never reached the window (err %v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the publisher never reached the window: the refreshHook seam did not fire")
	}

	// The operator cancels the job while the publisher sits in the window. The
	// cancel changes no counter, so only a guard that covers `status` notices.
	if err := st.CancelJob(ctx, "j", now); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RefreshJob: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the publisher never returned after the window was released")
	}
	st.refreshHook = nil

	job, err := st.GetJob(ctx, "j")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	counters, err := st.JobItemCounters(ctx, "j")
	if err != nil {
		t.Fatalf("JobItemCounters: %v", err)
	}
	t.Logf("after cancel and the held publish: status=%s finished_at=%d (items canceled=%d pending=%d)",
		job.Status, job.FinishedAtNs, counters.Canceled, counters.Queued+counters.Running)

	if job.Status != JobCanceled {
		t.Errorf("job = %s, want %s: the stale publisher overwrote the operator's cancel",
			job.Status, JobCanceled)
	}
	if job.Active() {
		t.Errorf("job is %s, so ListActiveJobs reports it again and it holds a max_running_jobs slot", job.Status)
	}
	if counters.Canceled != 2 || counters.Queued+counters.Running != 0 {
		t.Errorf("counters = %+v, want both items canceled", counters)
	}
}
