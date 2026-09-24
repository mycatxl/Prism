package node

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// WireGuardPeer is a wireguard endpoint peer (WP06 §3.1).
type WireGuardPeer struct {
	Address                     string   `json:"address,omitempty"`
	Port                        uint16   `json:"port,omitempty"`
	PublicKey                   string   `json:"public_key,omitempty"`
	PreSharedKey                string   `json:"pre_shared_key,omitempty"`
	AllowedIPs                  []string `json:"allowed_ips,omitempty"`
	PersistentKeepaliveInterval uint16   `json:"persistent_keepalive_interval,omitempty"`
	Reserved                    []uint8  `json:"reserved,omitempty"`
}

// WireGuardEndpoint is the sing-box wireguard endpoint payload (system=false).
type WireGuardEndpoint struct {
	Address    []string        `json:"address"`
	PrivateKey string          `json:"private_key"`
	MTU        uint32          `json:"mtu,omitempty"`
	Peers      []WireGuardPeer `json:"peers"`
}

// DefaultWireGuardAllowedIPs is the peer allowed-IP default of §3.1.
func DefaultWireGuardAllowedIPs() []string {
	return []string{"0.0.0.0/0", "::/0"}
}

// MainObject renders the endpoint payload as a sing-box endpoint object.
func (w WireGuardEndpoint) MainObject(tag string) (json.RawMessage, error) {
	object := map[string]any{
		"type":        "wireguard",
		"address":     append([]string(nil), w.Address...),
		"private_key": w.PrivateKey,
		"peers":       w.Peers,
	}
	if tag != "" {
		object["tag"] = tag
	}
	if w.MTU != 0 {
		object["mtu"] = w.MTU
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode wireguard endpoint: %w", err)
	}
	return encoded, nil
}

// EnvelopeObject renders the wireguard endpoint as a form B node envelope.
func (w WireGuardEndpoint) EnvelopeObject(name string) (json.RawMessage, error) {
	main, err := w.MainObject("")
	if err != nil {
		return nil, err
	}
	envelope := Envelope{
		PrismNode: EnvelopeVersion,
		Engine:    EngineSingbox,
		Kind:      KindEndpoint,
		Name:      name,
		Main:      main,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode wireguard envelope: %w", err)
	}
	return encoded, nil
}

// IsWireGuardEnvelope reports whether raw is a form B wireguard endpoint.
func IsWireGuardEnvelope(raw []byte) bool {
	envelope, ok, err := ParseEnvelope(raw)
	if !ok || err != nil || envelope.Engine != EngineSingbox || envelope.Kind != KindEndpoint {
		return false
	}
	typeName, err := TypeOfObject(envelope.Main)
	return err == nil && typeName == "wireguard"
}

// WireGuardEndpointFromLegacyOutbound converts a legacy sing-box WireGuard
// outbound (form A, `type: "wireguard"`) into wireguard endpoint options.
func WireGuardEndpointFromLegacyOutbound(raw []byte) (WireGuardEndpoint, error) {
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return WireGuardEndpoint{}, fmt.Errorf("parse legacy wireguard outbound: %w", err)
	}
	if typeName, _ := legacy["type"].(string); typeName != "" && typeName != "wireguard" {
		return WireGuardEndpoint{}, fmt.Errorf("INVALID:not a wireguard outbound: %s", typeName)
	}
	return WireGuardEndpointFromOptions(legacy)
}

// WireGuardEndpointFromOptions converts any normalized WireGuard option map
// (legacy sing-box outbound, Clash proxy, Surge proxy or URI query) into
// wireguard endpoint options. See WP06 §3.1 for the field mapping.
func WireGuardEndpointFromOptions(options map[string]any) (WireGuardEndpoint, error) {
	endpoint := WireGuardEndpoint{
		Address:    wireGuardAddressOptions(options),
		PrivateKey: strings.TrimSpace(stringOption(options, "private_key", "private-key")),
	}
	if endpoint.PrivateKey == "" {
		return WireGuardEndpoint{}, fmt.Errorf("INVALID:wireguard private key required")
	}
	if len(endpoint.Address) == 0 {
		return WireGuardEndpoint{}, fmt.Errorf("INVALID:wireguard local address required")
	}
	if mtu, ok := uintOption(options, "mtu"); ok {
		endpoint.MTU = uint32(mtu)
	}

	peers, err := wireGuardPeerOptions(options)
	if err != nil {
		return WireGuardEndpoint{}, err
	}
	if len(peers) == 0 {
		return WireGuardEndpoint{}, fmt.Errorf("INVALID:wireguard peer required")
	}
	endpoint.Peers = peers
	return endpoint, nil
}

func wireGuardAddressOptions(options map[string]any) []string {
	raw := stringListOption(options,
		"address", "local_address", "local-address", "ip", "ipv6",
		"self-ip", "self_ip", "self-ipv4", "self_ipv4", "self-ip-v6", "self_ip_v6", "self-ipv6", "self_ipv6",
	)
	addresses := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			normalized, ok := NormalizeWireGuardPrefix(part)
			if !ok || seen[normalized] {
				continue
			}
			seen[normalized] = true
			addresses = append(addresses, normalized)
		}
	}
	return addresses
}

func wireGuardPeerOptions(options map[string]any) ([]WireGuardPeer, error) {
	var peers []WireGuardPeer
	rawPeers := mapSliceOption(options, "peers")
	if len(rawPeers) > 0 {
		for _, rawPeer := range rawPeers {
			peer, ok, err := wireGuardSinglePeer(rawPeer)
			if err != nil {
				return nil, err
			}
			if ok {
				peers = append(peers, peer)
			}
		}
	}
	if len(peers) > 0 {
		return peers, nil
	}

	// Legacy outbound / Clash flat form: one peer built from the top-level keys.
	peer, ok, err := wireGuardSinglePeer(options)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []WireGuardPeer{peer}, nil
}

func wireGuardSinglePeer(options map[string]any) (WireGuardPeer, bool, error) {
	peer := WireGuardPeer{
		Address: strings.TrimSpace(stringOption(options, "server", "address", "host")),
	}
	if port, ok := uintOption(options, "server_port", "server-port", "port"); ok && port <= 65535 {
		peer.Port = uint16(port)
	}
	peer.PublicKey = strings.TrimSpace(stringOption(options,
		"public_key", "public-key", "peer_public_key", "peer-public-key", "publickey",
	))
	peer.PreSharedKey = strings.TrimSpace(stringOption(options,
		"pre_shared_key", "pre-shared-key", "presharedkey", "preshared_key", "preshared-key",
	))
	if peer.Address == "" || peer.PublicKey == "" || peer.Port == 0 {
		return WireGuardPeer{}, false, nil
	}

	allowedIPs := stringListOption(options, "allowed_ips", "allowed-ips", "allowedIPs")
	if len(allowedIPs) == 0 {
		allowedIPs = DefaultWireGuardAllowedIPs()
	}
	normalizedAllowed, err := normalizeWireGuardPrefixList(allowedIPs)
	if err != nil {
		return WireGuardPeer{}, false, err
	}
	peer.AllowedIPs = normalizedAllowed

	if keepalive, ok := uintOption(options, "persistent_keepalive_interval", "persistent-keepalive-interval", "persistent-keepalive", "persistent_keepalive"); ok && keepalive <= 65535 {
		peer.PersistentKeepaliveInterval = uint16(keepalive)
	}
	if reservedRaw, ok := firstValue(options, "reserved"); ok {
		reserved, err := parseWireGuardReserved(reservedRaw)
		if err != nil {
			return WireGuardPeer{}, false, err
		}
		peer.Reserved = reserved
	}
	return peer, true, nil
}

func normalizeWireGuardPrefixList(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, ok := NormalizeWireGuardPrefix(value)
		if !ok {
			return nil, fmt.Errorf("INVALID:wireguard prefix %q", value)
		}
		result = append(result, normalized)
	}
	return result, nil
}

// NormalizeWireGuardPrefix adds the host prefix length when the input has none.
func NormalizeWireGuardPrefix(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.Contains(raw, "/") {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return "", false
		}
		return prefix.String(), true
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "", false
	}
	bits := 32
	if addr.Is6() && !addr.Is4In6() {
		bits = 128
	}
	return netip.PrefixFrom(addr.Unmap(), bits).String(), true
}

// parseWireGuardReserved accepts [1,2,3], "1,2,3", "AQID" and three raw bytes.
func parseWireGuardReserved(raw any) ([]uint8, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case []any:
		result := make([]uint8, 0, len(typed))
		for _, entry := range typed {
			value, ok := numericByte(entry)
			if !ok {
				return nil, fmt.Errorf("INVALID:wireguard reserved value %v", entry)
			}
			result = append(result, value)
		}
		return validateWireGuardReserved(result)
	case []uint8:
		return validateWireGuardReserved(append([]uint8(nil), typed...))
	case []int:
		result := make([]uint8, 0, len(typed))
		for _, entry := range typed {
			if entry < 0 || entry > 255 {
				return nil, fmt.Errorf("INVALID:wireguard reserved value %d", entry)
			}
			result = append(result, uint8(entry))
		}
		return validateWireGuardReserved(result)
	case string:
		return parseWireGuardReservedString(typed)
	default:
		return nil, fmt.Errorf("INVALID:wireguard reserved value %T", raw)
	}
}

func parseWireGuardReservedString(raw string) ([]uint8, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		result := make([]uint8, 0, len(parts))
		for _, part := range parts {
			value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 8)
			if err != nil {
				return nil, fmt.Errorf("INVALID:wireguard reserved value %q", part)
			}
			result = append(result, uint8(value))
		}
		return validateWireGuardReserved(result)
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(raw)
		if err == nil && len(decoded) == 3 {
			return []uint8{decoded[0], decoded[1], decoded[2]}, nil
		}
	}
	return nil, fmt.Errorf("INVALID:wireguard reserved value %q", raw)
}

func numericByte(raw any) (uint8, bool) {
	switch typed := raw.(type) {
	case float64:
		if typed < 0 || typed > 255 || typed != float64(int(typed)) {
			return 0, false
		}
		return uint8(typed), true
	case int:
		if typed < 0 || typed > 255 {
			return 0, false
		}
		return uint8(typed), true
	case int64:
		if typed < 0 || typed > 255 {
			return 0, false
		}
		return uint8(typed), true
	case json.Number:
		value, err := typed.Int64()
		if err != nil || value < 0 || value > 255 {
			return 0, false
		}
		return uint8(value), true
	case uint8:
		return typed, true
	default:
		return 0, false
	}
}

func validateWireGuardReserved(reserved []uint8) ([]uint8, error) {
	if len(reserved) == 0 {
		return nil, nil
	}
	if len(reserved) != 3 {
		return nil, fmt.Errorf("INVALID:wireguard reserved needs 3 bytes, got %d", len(reserved))
	}
	return reserved, nil
}

// ParseWireGuardURI parses wireguard:// and wg:// share links (v2rayN format).
//
//	wireguard://<private_key>@host:port?publickey=..&presharedkey=..
//	  &reserved=1,2,3&address=10.0.0.2/32&mtu=1280#name
func ParseWireGuardURI(uri string) (string, WireGuardEndpoint, bool) {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return "", WireGuardEndpoint{}, false
	}
	query := parsed.Query()
	options := map[string]any{
		"server":      parsed.Hostname(),
		"server_port": parsed.Port(),
	}
	if parsed.User != nil {
		// net/url already decodes userinfo.
		options["private_key"] = strings.TrimSpace(parsed.User.Username())
	}
	for key, values := range query {
		if len(values) == 0 {
			continue
		}
		switch strings.ToLower(key) {
		case "publickey", "public_key", "public-key", "peer", "peer-public-key":
			options["public_key"] = values[0]
		case "presharedkey", "pre_shared_key", "pre-shared-key", "preshared-key":
			options["pre_shared_key"] = values[0]
		case "reserved":
			options["reserved"] = values[0]
		case "address", "local-address", "local_address", "ip", "ipv6":
			options["address"] = values[0]
		case "mtu":
			options["mtu"] = values[0]
		case "allowedips", "allowed_ips", "allowed-ips":
			options["allowed_ips"] = values[0]
		case "persistentkeepaliveinterval", "persistent_keepalive_interval", "keepalive":
			options["persistent_keepalive_interval"] = values[0]
		case "udp", "system", "interface_name", "system_interface", "gso":
			// ignored: Prism always builds system=false wireguard endpoints
		}
	}
	endpoint, err := WireGuardEndpointFromOptions(options)
	if err != nil {
		return "", WireGuardEndpoint{}, false
	}
	return decodeTag(parsed.Fragment), endpoint, true
}

func decodeTag(fragment string) string {
	if fragment == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(fragment); err == nil {
		fragment = decoded
	}
	return strings.TrimSpace(fragment)
}

// --- generic option readers -------------------------------------------------

func firstValue(options map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := options[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func mapSliceOption(options map[string]any, keys ...string) []map[string]any {
	raw, ok := firstValue(options, keys...)
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if item, ok := entry.(map[string]any); ok {
				result = append(result, item)
			}
		}
		return result
	default:
		return nil
	}
}

func stringListOption(options map[string]any, keys ...string) []string {
	raw, ok := firstValue(options, keys...)
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, entry := range typed {
			switch item := entry.(type) {
			case string:
				result = append(result, item)
			case float64:
				result = append(result, strconv.FormatFloat(item, 'f', -1, 64))
			}
		}
		return result
	default:
		return nil
	}
}

func stringOption(options map[string]any, keys ...string) string {
	raw, ok := firstValue(options, keys...)
	if !ok {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func uintOption(options map[string]any, keys ...string) (uint64, bool) {
	raw, ok := firstValue(options, keys...)
	if !ok {
		return 0, false
	}
	switch typed := raw.(type) {
	case float64:
		if typed < 0 || typed != float64(uint64(typed)) {
			return 0, false
		}
		return uint64(typed), true
	case int:
		if typed < 0 {
			return 0, false
		}
		return uint64(typed), true
	case uint64:
		return typed, true
	case json.Number:
		value, err := strconv.ParseUint(typed.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	case string:
		value, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}
