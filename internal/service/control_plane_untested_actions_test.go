package service

import (
	"context"
	"testing"

	"prism/internal/model"
)

// testNodeHash is a syntactically valid node hash: node.ParseHex wants exactly
// 16 bytes of hex, and a hash no pool holds exercises the not-found branch.
const testNodeHash = "000102030405060708090a0b0c0d0e0f"

// TestGetIPLoadRefusesAPlatformThatIsNotRegistered pins the guard, not the
// aggregation: the aggregation is the router's own and is covered there.
//
// The route is the only reader of this method, and an unregistered platform id
// has to come back as NOT_FOUND rather than as an empty list - an empty list
// reads to an operator as "this platform has no leases", which is a different
// answer from "this platform does not exist".
func TestGetIPLoadRefusesAPlatformThatIsNotRegistered(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	if _, err := cp.GetIPLoad("no-such-platform"); err == nil {
		t.Fatal("GetIPLoad answered for a platform that is not registered")
	} else {
		assertServiceErrorCode(t, err, "NOT_FOUND")
	}

	entries, err := cp.GetIPLoad(plat.ID)
	if err != nil {
		t.Fatalf("GetIPLoad(%q): %v", plat.ID, err)
	}
	if entries == nil {
		t.Fatal("GetIPLoad returned a nil slice, want an empty one: the route serialises it as null otherwise")
	}
	if len(entries) != 0 {
		t.Fatalf("GetIPLoad(%q) = %v, want no entries for a platform with no leases", plat.ID, entries)
	}
}

// TestGetIPLoadCountsWhatTheRouterHolds checks the entries reflect the leases
// that were placed, so the number on the page is the number in the router.
func TestGetIPLoadCountsWhatTheRouterHolds(t *testing.T) {
	cp, plat := newLeaseInheritanceTestService()

	seedLease(t, cp, model.Lease{
		PlatformID: plat.ID, UserID: "u-1", Account: "a-1",
		NodeHash: testNodeHash, EgressIP: "203.0.113.10", CreatedAtNs: 1,
	})
	seedLease(t, cp, model.Lease{
		PlatformID: plat.ID, UserID: "u-2", Account: "a-2",
		NodeHash: testNodeHash, EgressIP: "203.0.113.10", CreatedAtNs: 2,
	})

	entries, err := cp.GetIPLoad(plat.ID)
	if err != nil {
		t.Fatalf("GetIPLoad: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("GetIPLoad returned %d entries, want one per distinct egress IP: %v", len(entries), entries)
	}
	if entries[0].EgressIP != "203.0.113.10" {
		t.Fatalf("egress_ip = %q, want 203.0.113.10", entries[0].EgressIP)
	}
	if entries[0].LeaseCount != 2 {
		t.Fatalf("lease_count = %d, want 2", entries[0].LeaseCount)
	}
}

// TestProbeQualityRefusesInputItCannotActOn covers the three guards in front of
// the job it would queue: a malformed hash, a service with no pool, and a hash
// that names no node.
func TestProbeQualityRefusesInputItCannotActOn(t *testing.T) {
	ctx := context.Background()

	if _, err := (&ControlPlaneService{}).ProbeQuality(ctx, "not-a-hash"); err == nil {
		t.Fatal("ProbeQuality accepted a malformed node hash")
	} else {
		assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	}

	if _, err := (&ControlPlaneService{}).ProbeQuality(ctx, testNodeHash); err == nil {
		t.Fatal("ProbeQuality answered without an intel subsystem")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}

	cp, _ := newLeaseInheritanceTestService()
	if _, err := cp.ProbeQuality(ctx, testNodeHash); err == nil {
		t.Fatal("ProbeQuality queued a job for a node the pool does not hold")
	} else {
		assertServiceErrorCode(t, err, "NOT_FOUND")
	}
}

// TestReviewIPPureRefusesInputItCannotActOn is the same three guards on the
// other legacy action.
func TestReviewIPPureRefusesInputItCannotActOn(t *testing.T) {
	ctx := context.Background()

	if _, err := (&ControlPlaneService{}).ReviewIPPure(ctx, "not-a-hash"); err == nil {
		t.Fatal("ReviewIPPure accepted a malformed node hash")
	} else {
		assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	}

	if _, err := (&ControlPlaneService{}).ReviewIPPure(ctx, testNodeHash); err == nil {
		t.Fatal("ReviewIPPure answered without an intel subsystem")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}

	cp, _ := newLeaseInheritanceTestService()
	if _, err := cp.ReviewIPPure(ctx, testNodeHash); err == nil {
		t.Fatal("ReviewIPPure queued a job for a node the pool does not hold")
	} else {
		assertServiceErrorCode(t, err, "NOT_FOUND")
	}
}
