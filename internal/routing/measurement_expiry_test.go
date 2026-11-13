package routing

import (
	"errors"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/platform"
)

func TestDefaultCachedRoutesRequireCurrentMeasurement(t *testing.T) {
	for _, account := range []string{"", "sticky"} {
		for _, invalid := range []string{"expired", "null", "pending", "changed_exit"} {
			t.Run(account+"/"+invalid, func(t *testing.T) {
				pool := newRouterTestPool()
				plat := platform.NewPlatform(platform.DefaultPlatformID, "Default", nil, nil)
				plat.StickyTTLNs = int64(time.Hour)
				pool.addPlatform(plat)
				h, entry := newRoutableEntry(t, `{"id":"measurement-expiry"}`, "203.0.113.10")
				pool.addEntry(h, entry)
				ip := entry.GetEgressIP()
				measured := intel.AssessmentLite{State: intel.StateValid, Score: 0, Verdict: intel.VerdictHighRisk, ValidUntil: time.Now().Add(time.Hour).UnixNano()}
				pool.quality.SetAssessment(ip, measured)
				pool.rebuildPlatformView(plat)
				router := newTestRouter(pool, nil)
				if _, err := router.RouteRequest("", "owner", account, "example.test"); err != nil {
					t.Fatalf("Default rejected a real zero score: %v", err)
				}
				switch invalid {
				case "expired":
					measured.ValidUntil = time.Now().Add(-time.Second).UnixNano()
				case "null":
					measured.Score = -1
				case "pending":
					measured.State = intel.StatePending
				case "changed_exit":
					entry.SetEgressIP(ip.Next())
				}
				pool.quality.SetAssessment(ip, measured)
				if !plat.View().Contains(h) {
					t.Fatal("fixture must retain cached membership")
				}
				if _, err := router.RouteRequest("", "owner", account, "example.test"); !errors.Is(err, ErrNoAvailableNodes) {
					t.Fatalf("cached route accepted %s measurement: %v", invalid, err)
				}
				if _, ok := pool.GetEntry(h); !ok {
					t.Fatal("measurement failure must not delete the node")
				}
			})
		}
	}
}

func TestOutboundHandoffRechecksPlanTierAndMeasurement(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform(platform.DefaultPlatformID, "Default", nil, nil)
	pool.addPlatform(plat)
	h, entry := newRoutableEntry(t, `{"id":"handoff"}`, "203.0.113.11")
	pool.addEntry(h, entry)
	ip := entry.GetEgressIP()
	a := intel.AssessmentLite{State: intel.StateValid, Score: 95, IPType: intel.IPTypeCode("residential"), ValidUntil: time.Now().Add(time.Hour).UnixNano()}
	pool.quality.SetAssessment(ip, a)
	pool.rebuildPlatformView(plat)
	router := newTestRouter(pool, nil)
	tiers := []string{"residential:excellent"}
	result, err := router.RouteRequestForPlatform(plat.ID, "owner", "", "example.test", tiers)
	if err != nil {
		t.Fatal(err)
	}
	if !router.RouteStillValid(result, tiers) {
		t.Fatal("current measured tier rejected")
	}
	a.IPType = intel.IPTypeCode("datacenter")
	pool.quality.SetAssessment(ip, a)
	if router.RouteStillValid(result, tiers) {
		t.Fatal("changed plan tier accepted at handoff")
	}
	if !router.RouteStillValid(result, nil) {
		t.Fatal("Default gained an IP-type restriction")
	}
	a.ValidUntil = time.Now().Add(-time.Second).UnixNano()
	pool.quality.SetAssessment(ip, a)
	if router.RouteStillValid(result, nil) {
		t.Fatal("expired score accepted at handoff")
	}
}
