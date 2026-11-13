package store

import "context"

// WithEvidenceLock serializes evidence writes and assessment publication for
// an IP. The lock spans the read, persistence and snapshot update, so a slow
// assessment cannot publish over a newer provider result.
func (s *Store) WithEvidenceLock(ip string, fn func() error) error {
	var shard uint32 = 2166136261
	for i := range ip {
		shard = (shard ^ uint32(ip[i])) * 16777619
	}
	lock := &s.evidenceLocks[shard%uint32(len(s.evidenceLocks))]
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// SetEvidenceCommitted installs the service's synchronous projection hook.
// It runs after an accepted write, with that IP's evidence lock held. It may
// read evidence and write assessments, but must not reenter WithEvidenceLock.
func (s *Store) SetEvidenceCommitted(hook func(context.Context, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evidenceCommitted = hook
}
