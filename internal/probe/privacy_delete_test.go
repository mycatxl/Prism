package probe

import (
	"prism/internal/node"
	"testing"
	"time"
)

func TestPrivacyDeletionRemovesDirtyInflightRequeue(t *testing.T) {
	pool, h, _ := newProbeFailureTestPool(t, `{"type":"http","tls":{"enabled":true}}`)
	m := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
		t.Fatal("deleted node contacted")
		return nil, 0, nil
	}})
	m.TriggerImmediateEgressProbe(h)
	key := probeTaskKey{hash: h, kind: probeTaskKindEgress}
	state, ok := m.markTaskRunning(key)
	if !ok {
		t.Fatal("not running")
	}
	m.TriggerImmediateEgressProbe(h) // makes the in-flight state dirty
	pool.DeletePrivacyNode(h, node.PrivacyOwnAsset, "")
	m.ForgetNode(h)
	m.finishTask(key, state)
	if m.taskQueue.high.len()+m.taskQueue.normal.len() != 0 {
		t.Fatal("finish requeued deleted node")
	}
	if _, ok := m.taskStates.Load(key); ok {
		t.Fatal("probe state recreated")
	}
	if m.enqueueProbe(h, probeTaskKindEgress, probePriorityHigh) {
		t.Fatal("accepted removed node")
	}
}
