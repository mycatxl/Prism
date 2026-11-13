package state

import (
	"encoding/json"
	"prism/internal/model"
	"prism/internal/node"
	"strings"
	"testing"
	"time"
)

func TestPrivacyPurgeSerializesWithCapturedFlushAndScrubsSources(t *testing.T) {
	e, _, _ := newTestEngine(t)
	raw := json.RawMessage(`{"type":"http","server":"203.0.113.8","server_port":443,"tls":{"enabled":true},"password":"secret-delete-canary"}`)
	h := node.HashFromRawOptions(raw).Hex()
	content := `{"outbounds":[` + string(raw) + `,{"type":"shadowsocks","server":"203.0.113.9","server_port":443,"method":"aes-128-gcm","password":"keep-canary"}]}`
	for _, id := range []string{"a", "b"} {
		if err := e.UpsertSubscription(model.Subscription{ID: id, Name: id, SourceType: "local", Content: content, UpdateIntervalNs: int64(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	flushed := make(chan error, 1)
	deleted := make(chan error, 1)
	e.MarkNodeStatic(h)
	go func() {
		flushed <- e.FlushDirtySets(CacheReaders{ReadNodeStatic: func(string) *model.NodeStatic {
			close(entered)
			<-release
			return &model.NodeStatic{Hash: h, RawOptions: raw}
		}})
	}()
	<-entered
	go func() {
		if err := e.RecordPrivacyDeletion(h, node.PrivacyOwnAsset, []string{"a", "b"}); err != nil {
			deleted <- err
			return
		}
		deleted <- e.PurgePrivacyNode(h)
	}()
	close(release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	// A stale dirty marker or direct bulk writer cannot revive the captured row.
	if err := e.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: h, RawOptions: raw}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.LoadAllNodesStatic()
	if len(rows) != 0 {
		t.Fatal("snapshot resurrected inventory")
	}
	subs, _ := e.ListSubscriptions()
	for _, sub := range subs {
		if strings.Contains(sub.Content, "secret-delete-canary") || !strings.Contains(sub.Content, "keep-canary") {
			t.Fatal("source not scrubbed")
		}
	}
	events, _ := e.PrivacyDeletions()
	if len(events) != 2 {
		t.Fatal("missing source audit", events)
	}
	for _, sub := range subs {
		sub.Content = content
		if err := e.UpsertSubscription(sub); err != nil {
			t.Fatal(err)
		}
	}
	subs, _ = e.ListSubscriptions()
	for _, sub := range subs {
		if strings.Contains(sub.Content, "secret-delete-canary") {
			t.Fatal("content update revived document")
		}
	}
}
