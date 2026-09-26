package outbound

import (
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// Node-admission address policy (PRISM_DENY_PRIVATE_NODES).
//
// These tests reuse the mockPool / newTestEntry / countingBuilder fixtures from
// manager_test.go.

// TestNodeServerHosts pins the extraction of every dial target from a node
// document, at any nesting depth.
func TestNodeServerHosts(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "form A outbound",
			raw:  `{"type":"shadowsocks","server":"node.example.com","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
			want: []string{"node.example.com"},
		},
		{
			name: "envelope with main and deps",
			raw:  `{"prism_node":1,"main":{"type":"shadowsocks","server":"main.example.com","server_port":443,"method":"aes-256-gcm","password":"pw"},"deps":[{"type":"shadowtls","server":"dep.example.com","server_port":443,"password":"pw","version":3}]}`,
			want: []string{"dep.example.com", "main.example.com"},
		},
		{
			name: "wireguard endpoint peer",
			raw:  `{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"PRIV","peers":[{"address":"peer.example.com","port":51820,"public_key":"PUB"}]}`,
			want: []string{"peer.example.com"},
		},
		{
			name: "loopback target",
			raw:  `{"type":"shadowsocks","server":"127.0.0.1","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
			want: []string{"127.0.0.1"},
		},
		{
			name: "wildcard DNS target",
			raw:  `{"type":"shadowsocks","server":"127.0.0.1.nip.io","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
			want: []string{"127.0.0.1.nip.io"},
		},
		{
			name: "no server at all",
			raw:  `{"type":"socks","server_port":1080}`,
			want: nil,
		},
		{
			name: "malformed json",
			raw:  `{not json`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nodeServerHosts(json.RawMessage(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("nodeServerHosts = %v, want %v", got, tc.want)
			}
			seen := make(map[string]bool, len(got))
			for _, host := range got {
				seen[host] = true
			}
			for _, want := range tc.want {
				if !seen[want] {
					t.Fatalf("nodeServerHosts = %v, missing %q", got, want)
				}
			}
		})
	}
}

// TestEnsureNodeOutbound_DeniesForbiddenTargets pins the admission policy: a node
// whose server is a denied address never reaches the builder.
func TestEnsureNodeOutbound_DeniesForbiddenTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"loopback literal", `{"type":"ss","server":"127.0.0.1","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"private literal", `{"type":"ss","server":"10.0.0.5","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"metadata literal", `{"type":"ss","server":"169.254.169.254","server_port":80,"method":"aes-256-gcm","password":"pw"}`},
		{"localhost name", `{"type":"ss","server":"localhost","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"ipv6 loopback", `{"type":"ss","server":"::1","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"single label name", `{"type":"ss","server":"gateway","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"inet_aton loopback", `{"type":"ss","server":"2130706433","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{"cgnat literal", `{"type":"ss","server":"100.64.0.1","server_port":8388,"method":"aes-256-gcm","password":"pw"}`},
		{
			name: "chain dep is loopback",
			raw:  `{"prism_node":1,"main":{"type":"ss","server":"public.example.com","server_port":443,"method":"aes-256-gcm","password":"pw","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"127.0.0.1","server_port":443,"password":"pw","version":3}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := newTestEntry(tc.raw)
			pool := &mockPool{}
			pool.addEntry(entry)

			builder := &countingBuilder{}
			mgr := NewOutboundManager(pool, builder)
			mgr.SetDenyForbiddenTargets(true)
			mgr.EnsureNodeOutbound(entry.Hash)

			if entry.HasOutbound() {
				t.Fatal("a forbidden target was given an outbound")
			}
			if builder.Count() != 0 {
				t.Fatalf("the builder ran %d times for a forbidden target, want 0", builder.Count())
			}
			if entry.GetLastError() == "" {
				t.Fatal("a denied node must record why")
			}
			if !strings.Contains(entry.GetLastError(), "outbound denied") {
				t.Fatalf("LastError = %q, want it to say the target was denied", entry.GetLastError())
			}
		})
	}
}

// TestEnsureNodeOutbound_PolicyIsOptIn pins that the policy is off by default: a
// deployment that deliberately routes through a node on a private network (a home
// server, a jump host) keeps working.
func TestEnsureNodeOutbound_PolicyIsOptIn(t *testing.T) {
	raw := `{"type":"ss","server":"127.0.0.1","server_port":8388,"method":"aes-256-gcm","password":"pw"}`
	entry := newTestEntry(raw)
	pool := &mockPool{}
	pool.addEntry(entry)

	builder := &countingBuilder{}
	mgr := NewOutboundManager(pool, builder)
	// No SetDenyForbiddenTargets call: the zero value must not deny.
	mgr.EnsureNodeOutbound(entry.Hash)

	if builder.Count() == 0 {
		t.Fatal("with the policy off a private target must still reach the builder")
	}
	if entry.GetLastError() != "" {
		t.Fatalf("LastError = %q, want empty", entry.GetLastError())
	}
}

// TestEnsureNodeOutbound_PublicTargetBuilds pins that the policy does not get in
// the way of an ordinary node.
func TestEnsureNodeOutbound_PublicTargetBuilds(t *testing.T) {
	for _, raw := range []string{
		`{"type":"ss","server":"203.0.113.9","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
		`{"type":"ss","server":"8.8.8.8","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
		`{"type":"ss","server":"2606:4700:4700::1111","server_port":8388,"method":"aes-256-gcm","password":"pw"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			entry := newTestEntry(raw)
			pool := &mockPool{}
			pool.addEntry(entry)

			builder := &countingBuilder{}
			mgr := NewOutboundManager(pool, builder)
			mgr.SetDenyForbiddenTargets(true)
			mgr.EnsureNodeOutbound(entry.Hash)

			if builder.Count() == 0 {
				t.Fatal("a public target must reach the builder")
			}
			if entry.GetLastError() != "" {
				t.Fatalf("LastError = %q, want empty", entry.GetLastError())
			}
		})
	}
}

// TestSetDenyForbiddenTargets_NilReceiver pins the guard.
func TestSetDenyForbiddenTargets_NilReceiver(t *testing.T) {
	var mgr *OutboundManager
	mgr.SetDenyForbiddenTargets(true) // must not panic
}

// TestNodeServerHosts_DepthBound pins that a deeply nested hostile document
// cannot drive unbounded recursion.
func TestNodeServerHosts_DepthBound(t *testing.T) {
	var sb strings.Builder
	const depth = 64
	for i := 0; i < depth; i++ {
		sb.WriteString(`{"nested":`)
	}
	sb.WriteString(`{"server":"127.0.0.1"}`)
	for i := 0; i < depth; i++ {
		sb.WriteString(`}`)
	}
	// The walk stops at the depth bound, so the deeply buried target is not
	// reached. What matters is that the call returns instead of recursing away.
	_ = nodeServerHosts(json.RawMessage(sb.String()))
}

// TestNodeServerHosts_IgnoresNonStringServer pins that a non-string "server"
// value is skipped rather than crashing the walk.
func TestNodeServerHosts_IgnoresNonStringServer(t *testing.T) {
	raw := `{"type":"ss","server":{"nested":"value"},"server_port":8388}`
	if got := nodeServerHosts(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("nodeServerHosts = %v, want none for a non-string server", got)
	}
}

// TestForbiddenTargetReason_SkipsEmptyDocuments pins the guards: no raw options,
// no server field, and the disabled policy all short-circuit.
func TestForbiddenTargetReason_SkipsEmptyDocuments(t *testing.T) {
	pool := &mockPool{}
	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})

	// Disabled policy: even a loopback target is not denied.
	if _, denied := mgr.forbiddenTargetReason(newTestEntry(`{"type":"ss","server":"127.0.0.1"}`)); denied {
		t.Error("a disabled policy must not deny")
	}

	// Enabled policy, but nothing to classify.
	mgr.SetDenyForbiddenTargets(true)
	if _, denied := mgr.forbiddenTargetReason(newTestEntry(`{"type":"socks","server_port":1080}`)); denied {
		t.Error("a node with no server field must not be denied here")
	}
	if _, denied := mgr.forbiddenTargetReason(nil); denied {
		t.Error("a nil entry must not be denied")
	}
	// The reason names the offending host so the node detail view can show it.
	_, denied := mgr.forbiddenTargetReason(newTestEntry(`{"type":"ss","server":"10.0.0.5","server_port":8388}`))
	if !denied {
		t.Fatal("a private literal must be denied")
	}
}
