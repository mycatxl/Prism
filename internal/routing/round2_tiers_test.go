package routing

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/platform"
)

type tierAuditSnapshot map[netip.Addr]intel.AssessmentLite

func (s tierAuditSnapshot) Assessment(ip netip.Addr) (intel.AssessmentLite, bool) {
	v, ok := s[ip]
	return v, ok
}
func (s tierAuditSnapshot) CheckOutcome(string, string) (intel.OutcomeLite, bool) {
	return intel.OutcomeLite{}, false
}

func TestPlanIPTiersConstrainRandomStickyAndReplacementRoutes(t *testing.T) {
	for _, scenario := range []string{"random", "sticky", "same_ip_replacement", "no_permitted_node"} {
		t.Run(scenario, func(t *testing.T) {
			pool := newRouterTestPool()
			plat := platform.NewPlatform("tiers", "tiers", nil, nil)
			plat.StickyTTLNs = int64(time.Hour)
			pool.addPlatform(plat)
			allowedHash, allowed := newRoutableEntry(t, `{"id":"permitted"}`, "203.0.113.1")
			deniedHash, denied := newRoutableEntry(t, `{"id":"forbidden"}`, "203.0.113.2")
			siblingHash, sibling := newRoutableEntry(t, `{"id":"forbidden-sibling"}`, "203.0.113.2")
			unknownHash, unknown := newRoutableEntry(t, `{"id":"unassessed"}`, "203.0.113.3")
			pool.addEntry(allowedHash, allowed)
			pool.addEntry(deniedHash, denied)
			pool.addEntry(siblingHash, sibling)
			pool.addEntry(unknownHash, unknown)
			plat.SetQualitySnapshot(tierAuditSnapshot{
				allowed.GetEgressIP(): {State: intel.StateValid, Score: 95, ValidUntil: time.Now().Add(time.Hour).UnixNano(), IPType: intel.IPTypeCode("mobile"), Band: intel.BandCode("excellent")},
				denied.GetEgressIP():  {State: intel.StateValid, Score: 95, ValidUntil: time.Now().Add(time.Hour).UnixNano(), IPType: intel.IPTypeCode("residential"), Band: intel.BandCode("excellent")},
			})
			pool.rebuildPlatformView(plat)
			router := newTestRouter(pool, nil)
			account := ""
			if scenario != "random" {
				account = "shared"
				now := time.Now().UnixNano()
				router.ensurePlatformState(plat.ID).Leases.CreateLease("alice", account, Lease{NodeHash: deniedHash, EgressIP: denied.GetEgressIP(), CreatedAtNs: now, ExpiryNs: now + int64(time.Hour)})
			}
			if scenario == "same_ip_replacement" {
				pool.removeEntry(deniedHash)
			}
			if scenario == "no_permitted_node" {
				pool.removeEntry(allowedHash)
			}
			result, err := router.RouteRequestForPlatform(plat.ID, "alice", account, "cloudflare.com", []string{"residential:clean", "mobile:excellent"})
			if scenario == "no_permitted_node" {
				if !errors.Is(err, ErrNoAvailableNodes) {
					t.Fatalf("unentitled route result=%+v error=%v", result, err)
				}
				return
			}
			if err != nil || result.NodeHash != allowedHash {
				t.Fatalf("route escaped plan tiers: %+v error=%v", result, err)
			}
			if account != "" {
				lease, ok := router.ensurePlatformState(plat.ID).Leases.GetLease("alice", account)
				if !ok || lease.NodeHash != allowedHash {
					t.Fatal("sticky lease retained an unentitled node")
				}
			}
		})
	}
}
