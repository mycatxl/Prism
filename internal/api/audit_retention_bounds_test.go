package api

import (
	"testing"
	"time"

	"prism/internal/config"
)

// TestPruneAuditLogsWithRetentionNeverPutsTheCutoffInTheFuture pins the guard on
// the audit window.
//
// The window becomes a duration and is subtracted from now. 106752 days
// overflows int64, the duration turns negative, and the subtraction moves the
// cutoff forward instead of back - so PruneAudit's "at_ns < ?" would match every
// row in the table. A window that cannot be acted on must keep the shipped one.
func TestPruneAuditLogsWithRetentionNeverPutsTheCutoffInTheFuture(t *testing.T) {
	for _, days := range []int{106752, 200000, 1000000000, config.MaxRetentionDays + 1} {
		t.Run("", func(t *testing.T) {
			store := &fakeAuditStore{}
			if _, err := PruneAuditLogsWithRetention(store, days); err != nil {
				t.Fatalf("PruneAuditLogsWithRetention(%d): %v", days, err)
			}
			now := time.Now().UnixNano()
			if store.pruneCall.olderThanNs >= now {
				t.Fatalf("retention %d days put the cutoff at %d, which is not before now (%d)",
					days, store.pruneCall.olderThanNs, now)
			}
			// The ceiling is 100 years; anything within a day of it is the
			// clamped value and not the requested one.
			age := time.Duration(now - store.pruneCall.olderThanNs)
			if age < time.Duration(config.MaxRetentionDays-1)*24*time.Hour {
				t.Fatalf("retention %d days clamped to %v, want about %d days",
					days, age, config.MaxRetentionDays)
			}
		})
	}
}

// TestPruneAuditLogsWithRetentionKeepsAnOrdinaryWindow checks the guard did not
// change the window it is given, or the meaning of zero.
func TestPruneAuditLogsWithRetentionKeepsAnOrdinaryWindow(t *testing.T) {
	cases := []struct{ requested, wantDays int }{
		{0, 90},
		{-1, 90},
		{7, 7},
		{400, 400},
	}
	for _, c := range cases {
		store := &fakeAuditStore{}
		if _, err := PruneAuditLogsWithRetention(store, c.requested); err != nil {
			t.Fatalf("PruneAuditLogsWithRetention(%d): %v", c.requested, err)
		}
		gotDays := int(time.Since(time.Unix(0, store.pruneCall.olderThanNs)).Hours() / 24)
		if gotDays < c.wantDays-1 || gotDays > c.wantDays+1 {
			t.Fatalf("retention %d: cutoff is %d days old, want about %d", c.requested, gotDays, c.wantDays)
		}
	}
}
