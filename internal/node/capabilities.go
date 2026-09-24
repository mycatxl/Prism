package node

import "sync"

// Engine capability declarations (WP06 §10). These lists are the single source
// of truth for the capabilities API and for the protocol matrix test; they must
// stay in sync with what the parsers actually emit.

const (
	// SingboxVersion is the locked sing-box version (decision R9).
	SingboxVersion = "1.14.2"
	// MihomoVersion is the locked mihomo version used by WP07.
	MihomoVersion = "1.19.31"
)

// SingboxOutboundTypes lists the sing-box outbound types Prism builds.
func SingboxOutboundTypes() []string {
	return []string{
		"anytls",
		"http",
		"hysteria",
		"hysteria2",
		"shadowsocks",
		"shadowtls",
		"snell",
		"socks",
		"ssh",
		"trojan",
		"tuic",
		"vless",
		"vmess",
	}
}

// MihomoFallbackTypes lists the node types that only the mihomo fallback kernel
// can represent (WP07). Without the with_mihomo tag they are reported as
// ENGINE_NOT_BUILT.
func MihomoFallbackTypes() []string {
	return []string{
		"ssr",
		"mieru",
		"snell(v1-3)",
		"vless(xhttp/encryption)",
		"masque",
		"trusttunnel",
		"sudoku",
		"shadowquic",
		"gost-relay",
		"wireguard(amnezia)",
		"ss(restls/kcptun/gost-plugin)",
	}
}

// ShareLinkSchemes lists the share-link schemes the parser recognises.
func ShareLinkSchemes() []string {
	return []string{
		"vmess",
		"vmess1",
		"vless",
		"trojan",
		"ss",
		"ssd",
		"ssr",
		"hysteria",
		"hysteria2",
		"hy2",
		"tuic",
		"anytls",
		"wireguard",
		"wg",
		"ssh",
		"socks",
		"socks5",
		"socks5h",
		"http",
		"https",
		"tg",
		"netch",
		"mierus",
	}
}

// FileFormats lists the subscription file formats the parser recognises.
func FileFormats() []string {
	return []string{
		"singbox-json",
		"clash-yaml",
		"clash-json",
		"surge",
		"uri-lines",
		"base64",
		"plain-proxy-lines",
		"ovpn",
		"prism-openvpn-bundle",
	}
}

// EngineCapability describes one kernel for the capabilities API.
type EngineCapability struct {
	Name          string   `json:"name"`
	Built         bool     `json:"built"`
	Version       string   `json:"version"`
	OutboundTypes []string `json:"outbound_types,omitempty"`
	EndpointTypes []string `json:"endpoint_types,omitempty"`
	FallbackTypes []string `json:"fallback_types,omitempty"`
}

// engineRuntimes records the engines whose runtime layer is linked into this
// binary and can really construct node documents. The engine layer
// (internal/outbound) registers itself in an init function.
//
// This exists so a build tag alone can never make the API claim a capability the
// binary does not have: `Built` is the AND of the tag seam and a runtime
// registration (docs/ENGINE_DECISIONS.md D-1).
var engineRuntimes = struct {
	mu        sync.RWMutex
	available map[string]bool
}{available: map[string]bool{}}

// RegisterEngineRuntime declares that the calling package can construct nodes of
// engine `name` at runtime.
func RegisterEngineRuntime(name string) {
	engineRuntimes.mu.Lock()
	defer engineRuntimes.mu.Unlock()
	engineRuntimes.available[name] = true
}

// EngineRuntimeRegistered reports whether an engine runtime registered itself.
func EngineRuntimeRegistered(name string) bool {
	engineRuntimes.mu.RLock()
	defer engineRuntimes.mu.RUnlock()
	return engineRuntimes.available[name]
}

// EngineCapabilities returns the engine capability list.
func EngineCapabilities() []EngineCapability {
	return []EngineCapability{
		{
			Name:          EngineSingbox,
			Built:         true,
			Version:       SingboxVersion,
			OutboundTypes: SingboxOutboundTypes(),
			EndpointTypes: EndpointTypes(),
		},
		{
			Name: EngineMihomo,
			// Building with `with_mihomo` flips the mihomo tag seam in
			// mihomo_built.go, but the only runtime in this tree rejects mihomo
			// proxy documents with ENGINE_NOT_BUILT (internal/outbound,
			// SingboxRuntime.Build) and never registers a mihomo runtime. The
			// API must therefore report false even for that build: `fallback_types`
			// describes what mihomo *would* cover, not what this binary can do.
			Built:         mihomoBuildAvailable && EngineRuntimeRegistered(EngineMihomo),
			Version:       MihomoVersion,
			FallbackTypes: MihomoFallbackTypes(),
		},
	}
}
