package export

import (
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/node"
	"prism/internal/subscription"
)

// TestAudit_WireGuardRoundTripKeepsMultiValueAllowedIPs is the F2 regression
// test for the wireguard share link. The two sides used to disagree:
//
//   - the exporter writes every allowed-ips entry as ONE comma-joined query
//     value (uri.go:608-610: strings.Join(allowedIPs, ","));
//   - the parser read that value with stringListOption (node/wireguard.go:444),
//     whose string branch returns []string{typed} without splitting on commas,
//     and normalizeWireGuardPrefixList then fed "0.0.0.0/0,::/0" to
//     NormalizeWireGuardPrefix, which fails on the comma.
//
// The address field never had this problem: wireGuardAddressOptions splits on
// commas explicitly (wireguard.go:138). Because the default peer allowed-ips is
// ["0.0.0.0/0","::/0"] (node/wireguard.go:33-35), every wireguard node was
// exported as a link Prism itself refused to import.
//
// The fix splits comma-joined allowed-ips entries in wireGuardSinglePeer
// (node/wireguard.go, splitWireGuardPrefixEntries) and keeps the export format
// unchanged, so this test must stay green.
func TestAudit_WireGuardRoundTripKeepsMultiValueAllowedIPs(t *testing.T) {
	raw := json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"wg-default",` +
		`"main":{"type":"wireguard","address":["10.7.0.2/32"],"private_key":"WG-PRIVATE-KEY-FIXTURE",` +
		`"peers":[{"address":"203.0.113.9","port":51820,"public_key":"WG-PUBLIC-KEY-FIXTURE",` +
		`"allowed_ips":["0.0.0.0/0","::/0"]}]}}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("the fixture itself does not parse: %v", err)
	}
	items := []Item{{
		Name:       "wg-default",
		Hash:       "cccccccccccccccccccccccccccccccc",
		RawOptions: raw,
	}}

	body, _, report, err := Export(items, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
	}
	link := strings.TrimSpace(string(body))
	t.Logf("exported link: %s", link)
	if !strings.Contains(link, "allowedips=") {
		t.Fatalf("the exported link carries no allowedips parameter: %s", link)
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
	}
	got := node.HashFromRawOptions(result.Nodes[0].RawOptions).Hex()
	want := node.HashFromRawOptions(raw).Hex()
	t.Logf("round-trip hash = %s, original hash = %s", got, want)
	if got != want {
		t.Errorf("the exported wireguard link did not round-trip: got %s, want %s (%s)",
			got, want, result.Nodes[0].RawOptions)
	}
}
