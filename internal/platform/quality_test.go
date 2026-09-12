package platform

import (
	"net/netip"
	"regexp"
	"testing"

	"prism/internal/node"
	"prism/internal/quality"
)

func TestPlatform_EvaluateNode_QualityPolicy_MinScore(t *testing.T) {
	minScore := 70
	policy := QualityPolicy{
		MinScore: &minScore,
	}
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// Quality score 80 passes MinScore 70.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		score := 80
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:       "valid",
				PurityScore: &score,
			},
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 1 {
		t.Fatal("node with score >= MinScore should be routable")
	}

	// Quality score 60 fails MinScore 70.
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy)
	qualityLookup2 := func(addr netip.Addr) quality.Summary {
		score := 60
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:       "valid",
				PurityScore: &score,
			},
		}
	}

	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup2)

	if p2.View().Size() != 0 {
		t.Fatal("node with score < MinScore should not be routable")
	}
}

func TestPlatform_EvaluateNode_QualityPolicy_IPTypes(t *testing.T) {
	policy := QualityPolicy{
		IPTypes: []string{"residential", "mobile"},
	}
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// Residential IP passes.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:       "valid",
				NetworkType: "residential",
			},
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 1 {
		t.Fatal("node with allowed IP type should be routable")
	}

	// Datacenter IP fails.
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy)
	qualityLookup2 := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:       "valid",
				NetworkType: "datacenter",
			},
		}
	}

	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup2)

	if p2.View().Size() != 0 {
		t.Fatal("node with non-allowed IP type should not be routable")
	}
}

func TestPlatform_EvaluateNode_QualityPolicy_ExcludeHighRisk(t *testing.T) {
	excludeHighRisk := true
	policy := QualityPolicy{
		ExcludeHighRisk: &excludeHighRisk,
	}
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// High risk verdict fails.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:   "valid",
				Verdict: "high_risk",
			},
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 0 {
		t.Fatal("node with high_risk verdict should not be routable")
	}

	// Non-high-risk verdict passes.
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy)
	qualityLookup2 := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:   "valid",
				Verdict: "clean",
			},
		}
	}

	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup2)

	if p2.View().Size() != 1 {
		t.Fatal("node with non-high-risk verdict should be routable")
	}
}

func TestPlatform_EvaluateNode_QualityPolicy_ExcludeTor(t *testing.T) {
	policy := QualityPolicy{
		ExcludeTor: true,
	}
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// Tor node fails.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:    "valid",
				TorRoles: []string{"exit"},
			},
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 0 {
		t.Fatal("node with Tor role should not be routable when ExcludeTor is true")
	}

	// Non-Tor node passes.
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy)
	qualityLookup2 := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:    "valid",
				TorRoles: []string{},
			},
		}
	}

	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup2)

	if p2.View().Size() != 1 {
		t.Fatal("node without Tor role should be routable")
	}
}

func TestPlatform_EvaluateNode_QualityPolicy_Empty(t *testing.T) {
	policy := QualityPolicy{} // Empty policy
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// Node should pass when policy is empty.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		score := 0
		return quality.Summary{
			Assessment: &quality.Assessment{
				State:       "valid",
				PurityScore: &score,
			},
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 1 {
		t.Fatal("node should be routable when quality policy is empty")
	}
}

func TestPlatform_EvaluateNode_QualityPolicy_NoAssessment(t *testing.T) {
	minScore := 70
	policy := QualityPolicy{
		MinScore:      &minScore,
		UnknownAction: "exclude",
	}
	p := NewPlatformWithPolicy("p1", "Test", nil, nil, policy)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// No assessment available, UnknownAction=exclude.
	qualityLookup := func(addr netip.Addr) quality.Summary {
		return quality.Summary{
			Assessment: nil,
		}
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p.View().Size() != 0 {
		t.Fatal("node without assessment should not be routable when UnknownAction=exclude")
	}

	// UnknownAction=allow should pass.
	policy2 := QualityPolicy{
		MinScore:      &minScore,
		UnknownAction: "allow",
	}
	p2 := NewPlatformWithPolicy("p2", "Test", nil, nil, policy2)

	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup, qualityLookup)

	if p2.View().Size() != 1 {
		t.Fatal("node without assessment should be routable when UnknownAction=allow")
	}
}

// Helper to create platform with quality policy.
func NewPlatformWithPolicy(id, name string, regexFilters []*regexp.Regexp, regionFilters []string, policy QualityPolicy) *Platform {
	p := NewPlatform(id, name, regexFilters, regionFilters)
	p.QualityPolicy = policy
	return p
}

