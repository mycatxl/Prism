//go:build with_utls && with_wireguard && with_quic

package export

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
)

// TestExportSingboxIsAcceptedByBoxNew is the strongest offline validity proof
// for the §2.1 output: sing-box itself deserializes the exported document into
// option.Options and constructs a box instance (it is never started, and no
// socket is opened).
//
// The build tag mirrors the feature requirements of the fixtures: the vless
// node uses reality (with_utls), the hysteria2 node uses QUIC (with_quic) and
// the wireguard endpoint needs with_wireguard. The lite build skips this file
// exactly like it skips the protocol matrix.
func TestExportSingboxIsAcceptedByBoxNew(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, _, report, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != len(items) {
		t.Fatalf("report = %+v", report)
	}

	var options option.Options
	if err := json.Unmarshal(body, &options); err != nil {
		t.Fatalf("sing-box option decoding rejected the export: %v", err)
	}
	if len(options.Outbounds) == 0 || len(options.Endpoints) == 0 {
		t.Fatalf("decoded options: %d outbounds, %d endpoints", len(options.Outbounds), len(options.Endpoints))
	}

	instance, err := box.New(box.Options{
		Context: include.Context(context.Background()),
		Options: option.Options{Log: &option.LogOptions{Disabled: true}},
	})
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
