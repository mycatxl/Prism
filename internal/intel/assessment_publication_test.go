package intel

import (
	"context"
	"net/netip"
	"testing"

	"prism/internal/intel/store"
)

func TestAssessIPPublishesCommittedSnapshot(t *testing.T) {
	enabled := false
	svc := newAutoEnqueueService(t, &enabled)
	defer svc.Stop()
	ip := netip.MustParseAddr("203.0.113.19")
	calls := 0
	a := NewAssessor(AssessorOptions{Store: svc.Store(), Snapshot: svc.Snapshot(), OnChange: func(ips []netip.Addr) {
		calls++
		if len(ips) != 1 || ips[0] != ip {
			t.Fatal("notification must identify the committed exit")
		}
		if _, ok, err := svc.Store().GetAssessment(context.Background(), ip.String()); err != nil || !ok {
			t.Fatal("notification preceded persistence")
		}
		if _, ok := svc.Snapshot().Assessment(ip); !ok {
			t.Fatal("notification preceded snapshot publication")
		}
	}})
	row, err := a.AssessIP(context.Background(), ip)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("single assessment notifications = %d, want 1", calls)
	}
	if row.PurityScore.Valid {
		t.Fatal("missing evidence became a score")
	}
	if n := a.AssessMany(context.Background(), []netip.Addr{ip}); n != 1 || calls != 2 {
		t.Fatalf("batch must publish exactly once per commit: done=%d calls=%d", n, calls)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.AssessIP(canceled, ip); err == nil || calls != 2 {
		t.Fatal("failed assessment must not publish")
	}
	// Snapshot's permanent fence must still reject a late assessment callback.
	svc.Snapshot().PurgePrivacyNode("deleted", []string{ip.String()})
	svc.Snapshot().PrivacyIPDeleted = func(raw string) bool { return raw == ip.String() }
	svc.Snapshot().SetAssessmentFromRow(store.Assessment{IP: ip.String(), State: "valid"})
	if _, ok := svc.Snapshot().Assessment(ip); ok {
		t.Fatal("late publication resurrected a deleted exit")
	}
}
