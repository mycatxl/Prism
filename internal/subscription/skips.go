package subscription

import (
	"strings"

	"prism/internal/node"
)

// Clash/Surge node-type classification for the parse report (WP06 §9).
//
// convertClashProxyToNode returns a bare bool, so the reason a proxy was not
// imported is recomputed here from the same input map. The classifier is
// deliberately the inverse of the converter: any type it claims is convertible
// must be reachable in convertClashProxyPayload.

// clashProtocolNames maps a convertible Clash/Surge node type to the protocol
// name the capabilities surface uses.
var clashProtocolNames = map[string]string{
	"ss":          "shadowsocks",
	"shadowsocks": "shadowsocks",
	"vmess":       "vmess",
	"vless":       "vless",
	"trojan":      "trojan",
	"hysteria2":   "hysteria2",
	"hy2":         "hysteria2",
	"hysteria":    "hysteria",
	"socks":       "socks",
	"socks5":      "socks",
	"http":        "http",
	"wireguard":   "wireguard",
	"wg":          "wireguard",
	"snell":       "snell",
	"tuic":        "tuic",
	"anytls":      "anytls",
	"ssh":         "ssh",
}

// clashProxySkip explains why a Clash or Surge proxy map produced no node.
func clashProxySkip(proxy map[string]any, source string) SkippedNode {
	nodeType := strings.ToLower(strings.TrimSpace(getString(proxy, "type")))
	tag := strings.TrimSpace(firstNonEmpty(getString(proxy, "name"), getString(proxy, "tag")))

	switch nodeType {
	case "snell":
		version, hasVersion := getUint(proxy, "version")
		if !hasVersion || version != 4 {
			return skipSnellVersion(tag, source, version, hasVersion)
		}
		return skipNode(tag, "snell", source, node.ReasonInvalid, "snell psk required")
	case "wireguard", "wg":
		if firstNonNil(proxy["amnezia-wg-option"], proxy["amnezia-wg-options"], proxy["amnezia_wg_option"]) != nil {
			return skipNode(tag, "wireguard", source, node.ReasonUnsupportedFeature, "amnezia-wg-option")
		}
	case "vless", "vmess":
		network := normalizeV2RayNetwork(getString(proxy, "network"))
		if network == "xhttp" || network == "splithttp" {
			return skipNode(tag, nodeType, source, node.ReasonEngineNotBuilt, nodeType+"(xhttp)")
		}
		if network == "kcp" {
			return skipNode(tag, nodeType, source, node.ReasonUnsupportedFeature, "network kcp (mKCP)")
		}
		if nodeType == "vless" {
			if encryption := strings.TrimSpace(getString(proxy, "encryption")); encryption != "" && !strings.EqualFold(encryption, "none") {
				return skipNode(tag, "vless", source, node.ReasonEngineNotBuilt, "vless(encryption)")
			}
		}
	case "ss", "shadowsocks":
		if plugin := canonicalSSPluginName(getString(proxy, "plugin")); plugin == "restls" || plugin == "kcptun" || plugin == "gost-plugin" {
			return skipNode(tag, "shadowsocks", source, node.ReasonEngineNotBuilt, "ss("+plugin+")")
		}
	}

	if protocol, convertible := clashProtocolNames[nodeType]; convertible {
		// A type Prism can build, but this particular proxy misses required data
		// or uses a transport sing-box has no outbound for.
		return skipNode(tag, protocol, source, node.ReasonInvalid, "missing required fields or unsupported transport")
	}

	if skipped, ok := skipDeferredOrUnknown(tag, nodeType, source); ok {
		return skipped
	}
	return skipNode(tag, nodeType, source, node.ReasonUnsupportedProtocol, "type is not importable by Prism")
}
