package topology

import (
	"prism/internal/node"
	"prism/internal/subscription"
	"testing"
)

func TestPrivacyDeleteWinsAgainstRefreshAlreadyFetching(t *testing.T) {
	sm := NewSubscriptionManager()
	sub := subscription.NewSubscription("refresh", "refresh", "fixture", true, false)
	sm.Register(sub)
	pool := newTestPool(sm)
	sm.PrivacyFilter = pool.RejectPrivacyDocument
	sub.SetPrivacyFilter(pool.RejectPrivacyDocument)
	body := makeSubscriptionJSON(`{"type":"http","server":"203.0.113.5","server_port":443,"tls":{"enabled":true}}`)
	parsed, err := subscription.ParseGeneralSubscription(body)
	if err != nil {
		t.Fatal(err)
	}
	h := node.HashFromRawOptions(parsed[0].RawOptions)
	sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{"fixture"}})
	pool.AddNodeFromSub(h, parsed[0].RawOptions, sub.ID)
	fetched := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan struct{})
	scheduler := newTestScheduler(sm, pool, func(string) ([]byte, error) { close(fetched); <-resume; return body, nil })
	go func() { defer close(done); scheduler.UpdateSubscription(sub) }()
	<-fetched
	pool.DeletePrivacyNode(h, node.PrivacyOwnAsset, sub.ID)
	close(resume)
	<-done
	if pool.Size() != 0 || sub.ManagedNodes().Size() != 0 {
		t.Fatal("refresh revived deleted document or mapping")
	}
}
