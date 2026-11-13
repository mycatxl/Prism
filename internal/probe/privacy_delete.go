package probe

import "prism/internal/node"

// ForgetNode physically removes queued tokens and state. The pool hash fence
// prevents a finishing task from enqueuing itself again.
func (m *ProbeManager) ForgetNode(h node.Hash) {
	m.privacyMu.Lock()
	defer m.privacyMu.Unlock()
	m.taskQueue.mu.Lock()
	for _, b := range []*probeTaskBuffer{&m.taskQueue.high, &m.taskQueue.normal} {
		kept := make([]probeTask, 0, b.len())
		for _, t := range b.items[b.head:] {
			if t.key.hash != h {
				kept = append(kept, t)
			}
		}
		b.items = kept
		b.head = 0
	}
	m.taskQueue.mu.Unlock()
	for _, kind := range []probeTaskKind{probeTaskKindEgress, probeTaskKindLatency} {
		m.taskStates.Delete(probeTaskKey{hash: h, kind: kind})
	}
}
