package topology

import (
	"encoding/json"
	"log"
	"prism/internal/node"
	"slices"
)

func (p *GlobalNodePool) PrivacyDeleted(h node.Hash) bool {
	_, ok := p.privacyDeleted.Load(h)
	return ok
}
func (p *GlobalNodePool) PrivacyDeletionReason(h node.Hash) string {
	v, ok := p.privacyDeleted.Load(h)
	if !ok {
		return ""
	}
	return v.(string)
}
func (p *GlobalNodePool) SetPrivacyDeleteHandler(f func(node.Hash, string, []string) error) {
	p.onPrivacyDelete = f
}
func (p *GlobalNodePool) SetPrivacyJournal(f func(node.Hash, string, []string) error) {
	p.onPrivacyJournal = f
}
func (p *GlobalNodePool) RejectPrivacyDocument(h node.Hash, raw json.RawMessage) bool {
	return p.PrivacyDeleted(h) || (p.PrivacyPolicyEnabled() && len(raw) > 0 && node.PrivacyDeletionReason(raw) != "")
}

// DeletePrivacyNode is the only privacy eviction path. Publication is fenced
// before callbacks, so refreshes and late results cannot restore the node.
// The handler journals the event and synchronously purges durable dependencies.
func (p *GlobalNodePool) DeletePrivacyNode(h node.Hash, reason, source string) {
	if reason != node.PrivacyPlaintext && reason != node.PrivacyIdentity && reason != node.PrivacyOwnAsset && reason != node.PrivacyEchoLeak && reason != "DIRECT_EGRESS" {
		return
	}
	p.privacyDeleteMu.Lock()
	defer p.privacyDeleteMu.Unlock()
	p.hashIndexMu.Lock()
	p.privacyDeleted.Store(h, reason)
	e, ok := p.nodes.Load(h)
	p.hashIndexMu.Unlock()
	sources := []string{}
	if ok {
		e.CancelPrivacyWork()
		sources = e.SubscriptionIDs()
	}
	if source != "" && !slices.Contains(sources, source) {
		sources = append(sources, source)
	}
	if p.onPrivacyJournal != nil {
		if err := p.onPrivacyJournal(h, reason, sources); err != nil {
			log.Fatalf("privacy journal write failed for %s: %v", h, err)
		}
	}
	p.hashIndexMu.Lock()
	p.nodes.Delete(h)
	p.hashIndex.Delete(h)
	p.hashIndexMu.Unlock()
	for _, id := range sources {
		if p.subLookup != nil {
			if sub := p.subLookup(id); sub != nil {
				sub.PurgePrivacyHash(h)
			}
		}
		if p.onSubNodeChanged != nil {
			p.onSubNodeChanged(id, h, false)
		}
	}
	p.removeEgressIndex(h)
	if ok && p.onNodeRemoved != nil {
		p.onNodeRemoved(h, e)
	}
	if p.onPrivacyDelete != nil {
		if err := p.onPrivacyDelete(h, reason, sources); err != nil {
			// A purge failure must not masquerade as a completed deletion or allow a
			// restart to revive unjournaled state. Stop before serving further traffic.
			log.Fatalf("privacy deletion persistence failed for %s: %v", h, err)
		}
	}
	p.notifyAllPlatformsDirty(h)
}
