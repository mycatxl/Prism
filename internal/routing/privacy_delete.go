package routing

import (
	"github.com/puzpuzpuz/xsync/v4"
	"prism/internal/node"
)

func (r *Router) DeleteNodeLeases(h node.Hash) {
	r.privacyMu.Lock()
	defer r.privacyMu.Unlock()
	r.states.Range(func(id string, s *PlatformRoutingState) bool {
		s.Leases.Range(func(o leaseOwner, _ Lease) bool {
			var removed Lease
			deleted := false
			s.Leases.leases.Compute(o, func(l Lease, ok bool) (Lease, xsync.ComputeOp) {
				if !ok || l.NodeHash != h {
					return l, xsync.CancelOp
				}
				s.Leases.stats.Dec(l.EgressIP)
				removed = l
				deleted = true
				return l, xsync.DeleteOp
			})
			if deleted {
				r.emitLeaseEvent(LeaseEvent{Type: LeaseRemove, PlatformID: id, UserID: o.UserID, Account: o.Account, NodeHash: h, EgressIP: removed.EgressIP, CreatedAtNs: removed.CreatedAtNs})
			}
			return true
		})
		return true
	})
}
