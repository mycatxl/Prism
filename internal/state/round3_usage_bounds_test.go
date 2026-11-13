package state

import (
	"math"
	"testing"
	"time"

	"prism/internal/model"
)

func TestRound3UsagePersistenceStaysIntegral(t *testing.T) {
	for _, atomicFlush := range []bool{false, true} {
		r := newTestStateRepo(t)
		write := func(rows []model.UsageRow) error {
			if atomicFlush {
				_, err := r.FlushUsageHourlyAndCharge(rows, nil)
				return err
			}
			return r.UpsertUsageHourly(rows)
		}
		for _, ts := range []int64{int64(time.Hour), 2 * int64(time.Hour)} {
			for _, n := range []int64{math.MaxInt64, 1} {
				if err := write([]model.UsageRow{{KeyID: "key", UserID: "user", PlatformID: "p", TSNs: ts, IngressBytes: n, EgressBytes: n, Requests: n, Errors: n}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		check := func() {
			t.Helper()
			for _, list := range []func() ([]model.UsageRow, error){func() ([]model.UsageRow, error) { return r.ListUsageByUser("user", 0, int64(24*time.Hour)) }, func() ([]model.UsageRow, error) { return r.ListUsageDailyRange(0, int64(24*time.Hour)) }} {
				rows, err := list()
				if err != nil || len(rows) != 1 {
					t.Fatalf("usage read: %+v %v", rows, err)
				}
				row := rows[0]
				if row.IngressBytes != math.MaxInt64 || row.EgressBytes != math.MaxInt64 || row.Requests != math.MaxInt64 || row.Errors != math.MaxInt64 {
					t.Fatalf("usage overflow: %+v", row)
				}
			}
		}
		check()
		if n, err := r.SummarizeUsageHourlyBefore(int64(24 * time.Hour)); err != nil || n != 2 {
			t.Fatalf("summary: %d %v", n, err)
		}
		check()
		if err := write([]model.UsageRow{{KeyID: "key", UserID: "user", PlatformID: "p", TSNs: 3 * int64(time.Hour), IngressBytes: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SummarizeUsageHourlyBefore(int64(24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		check()
		if err := write([]model.UsageRow{{KeyID: "bad", IngressBytes: -1}}); err == nil {
			t.Fatal("negative usage accepted")
		}
	}
}

func TestRound3UsageAggregationKeepsPrecisionAboveFloatRange(t *testing.T) {
	r := newTestStateRepo(t)
	for i, n := range []int64{1 << 53, 1} {
		if err := r.UpsertUsageHourly([]model.UsageRow{{KeyID: "key", UserID: "user", TSNs: int64(i) * int64(time.Hour), IngressBytes: n}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.ListUsageByUser("user", 0, int64(24*time.Hour))
	if err != nil || len(rows) != 1 || rows[0].IngressBytes != (1<<53)+1 {
		t.Fatalf("precision lost: %+v %v", rows, err)
	}
}

func TestRound3SubscriptionUsageBounds(t *testing.T) {
	r := newTestStateRepo(t)
	mustInsertUser(t, r, "user", "usage-bound")
	if err := r.UpsertUserSubscription(model.UserSubscription{ID: "sub", UserID: "user", Status: model.SubscriptionStatusActive}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{math.MaxInt64, 1} {
		if err := r.AddSubscriptionUsedBytes("sub", n); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := r.GetUserSubscription("sub")
	if err != nil || sub.PeriodUsedBytes != math.MaxInt64 {
		t.Fatalf("usage=%+v err=%v", sub, err)
	}
	if err := r.AddSubscriptionUsedBytes("sub", math.MinInt64); err != nil {
		t.Fatal(err)
	}
	sub, err = r.GetUserSubscription("sub")
	if err != nil || sub.PeriodUsedBytes != 0 {
		t.Fatalf("usage=%+v err=%v", sub, err)
	}
}
