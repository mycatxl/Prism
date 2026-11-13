package main

import (
	"context"
	"net/netip"
	"prism/internal/node"
	"prism/internal/subscription"
	"slices"
)

// Install before bootstrap or workers. The event journal is a restart recovery
// instruction as well as a permanent, credential-free reimport fence.
func (a *prismApp) initPrivacyDeletion() error {
	engine := a.stateEngine
	pool := a.topoRuntime.pool
	engine.EnablePrivacyDeletion()
	pool.SetPrivacyJournal(func(h node.Hash, reason string, sources []string) error {
		bindings, err := engine.LoadAllSubscriptionNodes()
		if err != nil {
			return err
		}
		for _, b := range bindings {
			if b.NodeHash == h.Hex() && !slices.Contains(sources, b.SubscriptionID) {
				sources = append(sources, b.SubscriptionID)
			}
		}
		return engine.RecordPrivacyDeletion(h.Hex(), reason, sources)
	})
	pool.SetPrivacyDeleteHandler(func(h node.Hash, reason string, sources []string) error {
		a.topoRuntime.subManager.Range(func(_ string, s *subscription.Subscription) bool { s.PurgePrivacyHash(h); return true })
		a.topoRuntime.probeMgr.ForgetNode(h)
		if a.topoRuntime.router != nil {
			a.topoRuntime.router.DeleteNodeLeases(h)
		}
		if err := engine.PurgePrivacyNode(h.Hex()); err != nil {
			return err
		}
		if a.intelStore != nil {
			ips, err := a.intelStore.PurgePrivacyNode(context.Background(), h.Hex())
			if err != nil {
				return err
			}
			if a.intelSvc != nil {
				a.intelSvc.Snapshot().PurgePrivacyNode(h.Hex(), ips)
			}
		}
		return nil
	})
	events, err := engine.PrivacyDeletions()
	if err != nil {
		return err
	}
	for _, e := range events {
		h, err := node.ParseHex(e.NodeHash)
		if err != nil {
			return err
		}
		pool.DeletePrivacyNode(h, e.Reason, e.SubscriptionID)
	}
	// Disabled local sources may have no cached inventory row. Inspect their
	// documents too, before bootstrap can retain or republish them.
	subs, err := engine.ListSubscriptions()
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Content == "" {
			continue
		}
		parsed, err := subscription.ParseWithReport([]byte(sub.Content))
		if err != nil {
			return err
		}
		for _, n := range parsed.Nodes {
			if reason := node.PrivacyDeletionReason(n.RawOptions); reason != "" {
				pool.DeletePrivacyNode(node.HashFromRawOptions(n.RawOptions), reason, sub.ID)
			}
		}
	}
	// A previous version retained owned dual-stack exits only in intel.db.
	if a.intelStore != nil {
		policy, err := node.NewPrivacyPolicy(runtimeConfigSnapshot(a.runtimeCfg).NodePrivacy)
		if err != nil {
			return err
		}
		rows, err := a.intelStore.ListNodeEgress(context.Background())
		if err != nil {
			return err
		}
		for _, row := range rows {
			for _, raw := range []string{row.IPv4, row.IPv6} {
				ip, _ := netip.ParseAddr(raw)
				if policy.Denies(ip) {
					h, err := node.ParseHex(row.NodeHash)
					if err != nil {
						return err
					}
					pool.DeletePrivacyNode(h, node.PrivacyOwnAsset, "")
					break
				}
			}
		}
	}
	return nil
}
