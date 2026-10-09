//go:build with_utls && with_wireguard && with_quic

package export

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sJson "github.com/sagernet/sing/common/json"
)

// TestExportSingboxIsAcceptedByBoxNew is the strongest offline validity proof
// for the §2.1 output: sing-box itself deserializes the exported document into
// option.Options and constructs a box instance (it is never started, and no
// socket is opened).
//
// The build tag mirrors the feature requirements of the fixtures: the vless
// node uses reality (with_utls), the hysteria2 node uses QUIC (with_quic) and
// the wireguard endpoint needs with_wireguard. A build without one of those tags
// skips this file rather than failing on a protocol it cannot construct.
func TestExportSingboxIsAcceptedByBoxNew(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, _, report, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != len(items) {
		t.Fatalf("report = %+v", report)
	}

	// The shared fixtures carry placeholder keys (they feed the golden files of
	// every format); swap in well-formed keys so sing-box can build the
	// wireguard endpoint and the reality client.
	wgKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	realityKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32))
	body = bytes.ReplaceAll(body, []byte("WG-PRIVATE-KEY-FIXTURE"), []byte(wgKey))
	body = bytes.ReplaceAll(body, []byte("WG-PUBLIC-KEY-FIXTURE"), []byte(wgKey))
	body = bytes.ReplaceAll(body, []byte("PUBLIC-KEY-FIXTURE"), []byte(realityKey))

	// The chain fixture's shadowtls hop has no tls block (a parser fixture);
	// sing-box requires one, so add it the way a real subscription carries it.
	body = bytes.Replace(body, []byte(`"version": 3`), []byte(`"tls": {"enabled": true, "server_name": "www.example.com"}, "version": 3`), 1)

	// Decode with the registry-aware context, exactly like `sing-box check`
	// does, so every typed outbound/endpoint option is really parsed.
	ctx := include.Context(context.Background())
	options, err := sJson.UnmarshalExtendedContext[option.Options](ctx, body)
	if err != nil {
		t.Fatalf("sing-box option decoding rejected the export: %v", err)
	}
	if len(options.Outbounds) == 0 || len(options.Endpoints) == 0 {
		t.Fatalf("decoded options: %d outbounds, %d endpoints", len(options.Outbounds), len(options.Endpoints))
	}

	// The export is outbounds+endpoints only (no inbounds, route or dns);
	// sing-box accepts that as a complete configuration.
	options.Log = &option.LogOptions{Disabled: true}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatalf("box.New rejected the exported document: %v", err)
	}
	if instance == nil {
		t.Fatal("box.New returned a nil instance")
	}
	// Never Start(): constructing is enough to prove the document parses and
	// the outbound/endpoint registries accept every entry.
	_ = instance.Close()
}
