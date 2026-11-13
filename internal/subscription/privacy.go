package subscription

import (
	"encoding/json"
	"prism/internal/node"
)

// ScrubContent preserves the original source unless a rejected document exists.
// Once scrubbed, store a lossless bundle of surviving normalized documents,
// including chain dependencies; never retain the rejected source as a backup.
func ScrubContent(content string, rejected func(node.Hash, json.RawMessage) bool) (string, error) {
	if content == "" || rejected == nil {
		return content, nil
	}
	result, err := ParseWithReport([]byte(content))
	if err != nil {
		return "", err
	}
	kept := make([]ParsedNode, 0, len(result.Nodes))
	changed := false
	for _, n := range result.Nodes {
		if rejected(node.HashFromRawOptions(n.RawOptions), n.RawOptions) {
			changed = true
			continue
		}
		kept = append(kept, n)
	}
	if !changed {
		return content, nil
	}
	b, err := json.Marshal(map[string]any{"prism_nodes": kept})
	return string(b), err
}

// SetPrivacyFilter is installed before a subscription is published. The filter
// reads only the pool's hash fence and the offline document classifier.
func (s *Subscription) SetPrivacyFilter(f func(node.Hash, json.RawMessage) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.privacyFilter = f
}

// PurgePrivacyHash synchronizes with content and map publication, not opMu:
// rejection may be discovered while a refresh already holds opMu.
func (s *Subscription) PurgePrivacyHash(h node.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managedNodes.Load().Delete(h)
	clean, err := ScrubContent(s.content, func(hash node.Hash, _ json.RawMessage) bool { return hash == h })
	if err == nil && clean != s.content {
		s.content = clean
		s.configVersion.Add(1)
	}
}
