package store

import (
	"context"
	"testing"
)

// TestProviderQueue_ReleaseKeepsTheLastFailureDiagnostic pins the column
// ownership of provider_queue.error_code: only ResolveProviderItem (clears it on
// success) and FailProviderItem (writes the new code) may touch it.
//
// The claim used to clear it, which was harmless while a claim always led to a
// lookup. Since the provider worker claims before it consumes the daily budget,
// a shut gate hands the row straight back through
// ReleaseProviderItems - and a row that never sent a request would have lost the
// error_code of its previous failed attempt.
func TestProviderQueue_ReleaseKeepsTheLastFailureDiagnostic(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	if _, err := st.EnqueueProviderItem(ctx, QueueItem{
		Provider: "ipapi_is", IP: "198.51.100.7", Priority: 1, NextRunAtNs: 10, EnqueuedAtNs: 10,
	}, EnqueueIfMissing); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// A failed attempt leaves its diagnostic behind and parks the row.
	if err := st.FailProviderItem(ctx, "ipapi_is", "198.51.100.7", 1, 20, "PROVIDER_UNAVAILABLE", false); err != nil {
		t.Fatalf("FailProviderItem: %v", err)
	}
	if row := queueRow(t, st, "ipapi_is", "198.51.100.7"); row.ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("error_code = %q, want the failure to be recorded", row.ErrorCode)
	}

	// The gate is shut: claim, then hand the row back without a request.
	claimed, err := st.ClaimProviderItems(ctx, "ipapi_is", 20, 500, 5, "worker-a")
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v (err %v)", claimed, err)
	}
	if claimed[0].ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Errorf("claimed error_code = %q, want the previous diagnostic kept", claimed[0].ErrorCode)
	}
	if released, err := st.ReleaseProviderItems(ctx, "ipapi_is", []string{"198.51.100.7"}, "worker-a", 30); err != nil || released != 1 {
		t.Fatalf("release = %d (err %v), want 1", released, err)
	}

	row := queueRow(t, st, "ipapi_is", "198.51.100.7")
	if row.Status != QueueQueued || row.LeaseOwner != "" {
		t.Fatalf("released row = %+v, want queued without a lease", row)
	}
	if row.ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Errorf("error_code after release = %q, want %q: the row never sent a request, so its last failure is still the truth",
			row.ErrorCode, "PROVIDER_UNAVAILABLE")
	}

	// Success is what clears it.
	if err := st.ResolveProviderItem(ctx, "ipapi_is", "198.51.100.7"); err != nil {
		t.Fatalf("ResolveProviderItem: %v", err)
	}
	if row := queueRow(t, st, "ipapi_is", "198.51.100.7"); row.ErrorCode != "" {
		t.Errorf("error_code after resolve = %q, want it cleared", row.ErrorCode)
	}
}
