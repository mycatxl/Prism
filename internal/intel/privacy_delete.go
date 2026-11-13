package intel

import "net/netip"

func (s *Snapshot) PurgePrivacyNode(hash string, orphanIPs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deletedNodes == nil {
		s.deletedNodes = map[string]bool{}
	}
	s.deletedNodes[hash] = true
	delete(s.checks, hash)
	for _, raw := range orphanIPs {
		ip, err := netip.ParseAddr(raw)
		if err == nil {
			delete(s.assessments, ip.Unmap())
		}
	}
}
