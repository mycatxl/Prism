package service

import (
	"net/netip"
	"strings"

	"prism/internal/export"
	"prism/internal/node"
	"prism/internal/quality"
)

// ExportItem projects one node summary onto an export.Item (WP11 §1). ok is
// false when the node has no document to export, or when the node is no longer
// in the pool.
//
// The node document is copied into the item: the exporters never see the pool
// entry, so they cannot mutate pool state.
func (s *ControlPlaneService) ExportItem(summary NodeSummary) (export.Item, bool) {
	if s == nil || s.Pool == nil {
		return export.Item{}, false
	}
	hash, err := node.ParseHex(summary.NodeHash)
	if err != nil {
		return export.Item{}, false
	}
	entry, ok := s.Pool.GetEntry(hash)
	if !ok || len(entry.RawOptions) == 0 {
		return export.Item{}, false
	}

	item := export.Item{
		Name:         s.exportDisplayName(summary),
		Hash:         summary.NodeHash,
		Subscription: s.ExportSubscriptionName(hash),
		Healthy:      summary.IsHealthyAndEnabled(),
		Region:       summary.Region,
		RawOptions:   append([]byte(nil), entry.RawOptions...),
	}
	// The intel summary of the egress IP. It is attached even when no assessment
	// exists yet, so the analysis formats keep the egress facts.
	summaryQuality := s.NodeQualityForEgress(entry.GetEgressIP())
	item.Intel = &summaryQuality
	if summary.ReferenceLatencyMs != nil {
		item.LatencyMs = *summary.ReferenceLatencyMs
	}
	return item, true
}

// exportDisplayName resolves the node name used by every export. A node with
// no display tag falls back to its hash, which is stable and cannot collide
// inside one export.
func (s *ControlPlaneService) exportDisplayName(summary NodeSummary) string {
	if name := strings.TrimSpace(summary.DisplayTag); name != "" {
		return name
	}
	for _, tag := range summary.Tags {
		if name := strings.TrimSpace(tag.Tag); name != "" {
			return name
		}
	}
	return summary.NodeHash
}

// ExportSubscriptionName returns the name of the first enabled subscription
// that references the node, or "" when it has none.
func (s *ControlPlaneService) ExportSubscriptionName(hash node.Hash) string {
	if s == nil || s.SubMgr == nil {
		return ""
	}
	entry := s.PoolEntry(hash)
	if entry == nil {
		return ""
	}
	for _, subID := range entry.SubscriptionIDs() {
		sub := s.SubMgr.Lookup(subID)
		if sub == nil || !sub.Enabled() {
			continue
		}
		return sub.Name()
	}
	return ""
}

// NodeQualityForEgress exposes the projected WP10 summary of an egress IP. It
// is the same projection the node list uses, so an export and the UI never
// disagree about a purity band.
func (s *ControlPlaneService) NodeQualityForEgress(ip netip.Addr) quality.Summary {
	return s.nodeQuality(ip)
}

// PoolEntry returns the pool entry for a hash, or nil.
func (s *ControlPlaneService) PoolEntry(hash node.Hash) *node.NodeEntry {
	if s == nil || s.Pool == nil {
		return nil
	}
	entry, ok := s.Pool.GetEntry(hash)
	if !ok {
		return nil
	}
	return entry
}
