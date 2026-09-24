package outbound

import (
	"encoding/json"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/subscription"
)

// Shared WireGuard fixtures for the protocol matrix (WP06 §3): every source
// must produce the same sing-box wireguard endpoint payload.

const (
	wireGuardPrivateKey = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	wireGuardPublicKey  = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="

	// Legacy sing-box outbound (form A, `type: "wireguard"`).
	wireGuardLegacyOptions = `{"type":"wireguard","tag":"wg-jp","server":"1.2.3.4","server_port":51820,` +
		`"private_key":"` + wireGuardPrivateKey + `","peer_public_key":"` + wireGuardPublicKey + `",` +
		`"local_address":["10.0.0.2/32"],"reserved":[1,2,3],"mtu":1280}`

	// sing-box endpoint object (top-level `endpoints` array).
	wireGuardEndpointObjectJSON = `{"type":"wireguard","tag":"wg-jp","address":["10.0.0.2/32"],` +
		`"private_key":"` + wireGuardPrivateKey + `","mtu":1280,"peers":[{"address":"1.2.3.4","port":51820,` +
		`"public_key":"` + wireGuardPublicKey + `","reserved":[1,2,3]}]}`

	// Clash / mihomo proxy entry.
	wireGuardClashProxyJSON = `{"name":"wg-jp","type":"wireguard","server":"1.2.3.4","port":51820,` +
		`"private-key":"` + wireGuardPrivateKey + `","public-key":"` + wireGuardPublicKey + `",` +
		`"ip":"10.0.0.2/32","reserved":[1,2,3],"mtu":1280}`

	// Surge proxy line.
	// Surge's comma-separated option list cannot express a 3-byte reserved
	// value, so this line leaves it out.
	wireGuardSurgeLine = "wg-jp = wireguard, 1.2.3.4, 51820, private-key=" + wireGuardPrivateKey +
		", public-key=" + wireGuardPublicKey + ", ip=10.0.0.2/32, mtu=1280"

	// v2rayN share link.
	wireGuardShareLink = "wireguard://" + wireGuardPrivateKey + "@1.2.3.4:51820?publickey=" +
		wireGuardPublicKey + "&reserved=1,2,3&address=10.0.0.2/32&mtu=1280#wg-jp"

	// dialProbeTimeout bounds the loopback dial probe used by §6.5 checks.
	dialProbeTimeout = 2 * time.Second
)

// TestWireGuardSourcesShareEndpointPayload implements WP06 §3: the five
// WireGuard sources must all resolve to the same endpoint JSON.
func TestWireGuardSourcesShareEndpointPayload(t *testing.T) {
	sources := []struct {
		name  string
		input string
	}{
		{name: "legacy-outbound", input: `{"outbounds":[` + wireGuardLegacyOptions + `]}`},
		{name: "endpoint-array", input: `{"endpoints":[` + wireGuardEndpointObjectJSON + `]}`},
		{name: "clash", input: `{"proxies":[` + wireGuardClashProxyJSON + `]}`},
		{name: "surge", input: "[Proxy]\n" + wireGuardSurgeLine + "\n"},
		{name: "uri", input: wireGuardShareLink},
	}

	payloads := make(map[string]map[string]any, len(sources))
	for _, source := range sources {
		parsed, err := subscription.ParseGeneralSubscription([]byte(source.input))
		if err != nil {
			t.Fatalf("%s: parse: %v", source.name, err)
		}
		if len(parsed) != 1 {
			t.Fatalf("%s: parsed %d nodes, want 1", source.name, len(parsed))
		}
		doc, err := node.ParseNodeDoc(parsed[0].RawOptions)
		if err != nil {
			t.Fatalf("%s: ParseNodeDoc: %v", source.name, err)
		}
		if doc.Kind != node.DocEndpoint {
			t.Fatalf("%s: kind %s, want endpoint", source.name, doc.Kind)
		}
		if doc.Type != "wireguard" {
			t.Fatalf("%s: type %q, want wireguard", source.name, doc.Type)
		}
		payloads[source.name] = endpointPayload(t, doc.Main)
	}

	pivot := "legacy-outbound"
	want := payloads[pivot]
	if len(want) == 0 {
		t.Fatal("empty wireguard payload")
	}
	for name, payload := range payloads {
		expected := want
		if name == "surge" {
			// Surge cannot express a 3-byte reserved value; the field is
			// asserted for the other four sources below.
			expected = withoutPeerReserved(t, want)
			payload = withoutPeerReserved(t, payload)
		}
		if canonicalJSON(t, payload) != canonicalJSON(t, expected) {
			t.Fatalf("%s endpoint payload:\n got %s\nwant %s", name,
				canonicalJSON(t, payload), canonicalJSON(t, expected))
		}
	}

	// reserved is preserved by every source that can express it.
	for _, name := range []string{"legacy-outbound", "endpoint-array", "clash", "uri"} {
		peers, ok := payloads[name]["peers"].([]any)
		if !ok || len(peers) != 1 {
			t.Fatalf("%s: peers: %#v", name, payloads[name]["peers"])
		}
		peer, ok := peers[0].(map[string]any)
		if !ok {
			t.Fatalf("%s: peer: %#v", name, peers[0])
		}
		// sing-box stores reserved as a []uint8, which JSON renders as the
		// base64 form of the three bytes.
		switch reserved := peer["reserved"].(type) {
		case string:
			if reserved != "AQID" {
				t.Fatalf("%s: reserved: %#v", name, reserved)
			}
		case []any:
			if len(reserved) != 3 {
				t.Fatalf("%s: reserved: %#v", name, reserved)
			}
		default:
			t.Fatalf("%s: reserved: %#v", name, peer["reserved"])
		}
	}
}

// endpointPayload decodes an endpoint object into a mutable map without tag.
func endpointPayload(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode endpoint payload: %v", err)
	}
	delete(payload, "tag")
	return payload
}

// withoutPeerReserved returns a shallow copy with the first peer's reserved
// field removed.
func withoutPeerReserved(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	copied := make(map[string]any, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	peers, ok := copied["peers"].([]any)
	if !ok || len(peers) == 0 {
		return copied
	}
	peer, ok := peers[0].(map[string]any)
	if !ok {
		return copied
	}
	peerCopy := make(map[string]any, len(peer))
	for key, value := range peer {
		peerCopy[key] = value
	}
	delete(peerCopy, "reserved")
	copied["peers"] = []any{peerCopy}
	return copied
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("canonical JSON: %v", err)
	}
	return string(encoded)
}
