package store

import (
	"context"
	"testing"
)

// TestAudit_CancelJobSettlesRunningItemsAndCounters pins the counters the API
// reports after a cancel: JobProgressView.Pending is queued+running, so a
// canceled job that still holds a running item shows a nonzero Pending forever.
func TestAudit_CancelJobSettlesRunningItemsAndCounters(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := int64(1_000)
	if err := st.CreateJob(ctx, Job{ID: "j", Kind: "intel", Status: JobRunning, CreatedAtNs: now},
		[]JobItem{
			{JobID: "j", NodeHash: "queued-1", Status: ItemQueued, NextRunAtNs: now, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "running-1", Status: ItemRunning, LeaseOwner: "w", LeaseUntilNs: now + 1000, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "done-1", Status: ItemDone, UpdatedAtNs: now},
			{JobID: "j", NodeHash: "failed-1", Status: ItemFailed, UpdatedAtNs: now},
		}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.CancelJob(ctx, "j", now+1); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	view, err := st.JobProgressView(ctx, "j")
	if err != nil {
		t.Fatalf("JobProgressView: %v", err)
	}
	t.Logf("status=%s total=%d done=%d failed=%d canceled=%d pending=%d",
		view.Status, view.Total, view.Done, view.Failed, view.Canceled, view.Pending)
	if view.Status != JobCanceled {
		t.Errorf("status = %s, want %s", view.Status, JobCanceled)
	}
	if view.Pending != 0 {
		t.Errorf("pending = %d, want 0: a canceled job must not hold an item no worker can claim", view.Pending)
	}
	if view.Canceled != 2 {
		t.Errorf("canceled = %d, want 2 (the queued and the running item)", view.Canceled)
	}
	// The settled history stays: canceling must not rewrite done/failed items.
	if view.Done != 1 || view.Failed != 1 {
		t.Errorf("done = %d failed = %d, want 1 and 1: canceling must not touch settled items", view.Done, view.Failed)
	}
	// The lease is released so a stale sweep cannot pick the row up again.
	if row, _, err := st.GetJobItem(ctx, "j", "running-1"); err != nil {
		t.Fatalf("GetJobItem: %v", err)
	} else if row.LeaseOwner != "" || row.LeaseUntilNs != 0 {
		t.Errorf("running item lease = %q/%d, want cleared", row.LeaseOwner, row.LeaseUntilNs)
	}
}
