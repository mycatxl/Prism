package export

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"prism/internal/node"
)

// Direct proxy-address export. One line per node, in the form a browser or any
// other tool that talks to the node itself expects:
//
//	socks5://user:password@host:port
//	https://user:password@host:port
//
// This is deliberately not the share-link export, and it shares no rendering
// code with it. A share link carries the whole node - protocol options,
// transport, TLS material - so a proxy client can rebuild it, and it exists for
// every protocol Prism can carry. An address carries only what a client that
// already speaks HTTP or SOCKS5 needs. That is why a node of any other protocol
// is skipped with a reason rather than emitted in a spelling the target cannot
// use: a list of addresses is meant to be pasted into something, and one wrong
// line in it is a connection that fails much later, far from this export.

// exportProxy renders one direct proxy address per representable node.
func exportProxy(items []preparedItem, report *Report) ([]byte, int) {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		address, ok := proxyAddress(item)
		if !ok {
			skip[struct{}](report, item.Name, NotRepresentable(FormatProxy))
			continue
		}
		lines = append(lines, address)
	}
	if len(lines) == 0 {
		// An empty pool or a filter that matches only other protocols
		// legitimately renders an empty list; the caller reads the report.
		return []byte{}, 0
	}
	return []byte(strings.Join(lines, "\n") + "\n"), len(lines)
}

// proxyAddress renders one node as a direct proxy address. ok is false for a
// node whose protocol has no address spelling a browser can use.
func proxyAddress(item preparedItem) (string, bool) {
	object, ok := proxyAddressDocument(item)
	if !ok {
		return "", false
	}
	scheme, ok := proxyAddressScheme(object)
	if !ok {
		return "", false
	}
	authority, ok := proxyAddressAuthority(object)
	if !ok {
		return "", false
	}
	return linkURL(scheme, proxyAddressUserinfo(object)+authority, nil, item.Name), true
}

// proxyAddressDocument returns the object holding the node's own connection
// details, for either document shape.
//
// A sing-box outbound keeps them in Main. A mihomo proxy keeps them in Proxy:
// ParseNodeDoc moves the envelope's proxy object there so the exporters do not
// have to know which shape they were handed. A chain is refused because its
// reachable address is its detour's, not its own, and an endpoint (a WireGuard
// node) has no proxy address at all.
func proxyAddressDocument(item preparedItem) (map[string]any, bool) {
	switch item.Doc.Kind {
	case node.DocOutbound:
		object, err := objectMap(item.Doc.Main)
		if err != nil {
			return nil, false
		}
		return object, true
	case node.DocProxy:
		object, err := objectMap(item.Doc.Proxy)
		if err != nil {
			return nil, false
		}
		return object, true
	}
	return nil, false
}

// proxyAddressScheme maps a node onto the scheme a client dials it with.
//
// HTTP is the only protocol that gains a second spelling from TLS, so it is the
// only one where the TLS block decides the scheme. SOCKS over TLS has no
// spelling a browser understands, so such a node is refused rather than
// exported as a plain SOCKS address that would silently fail to authenticate
// the transport.
func proxyAddressScheme(object map[string]any) (string, bool) {
	typeName := strings.ToLower(strings.TrimSpace(mapString(object, "type")))
	tls, _ := parseClashTLS(object)

	switch typeName {
	case "http", "https":
		if typeName == "https" || tls.Enabled {
			return "https", true
		}
		return "http", true
	case "socks", "socks5", "socks4", "socks4a":
		if tls.Enabled {
			return "", false
		}
		return socksAddressScheme(typeName, object), true
	}
	return "", false
}

// socksAddressScheme keeps the SOCKS version, which the scheme is the only
// place to put. A mihomo proxy names the version in its type; a sing-box
// outbound names it in a "version" field and leaves it empty for SOCKS5.
func socksAddressScheme(typeName string, object map[string]any) string {
	switch typeName {
	case "socks4", "socks4a":
		return typeName
	}
	switch socksNodeVersion(object) {
	case "4":
		return "socks4"
	case "4a":
		return "socks4a"
	default:
		return "socks5"
	}
}

// proxyAddressAuthority renders "host:port" for either document shape.
//
// A sing-box outbound names the port server_port and a mihomo proxy names it
// port, so both spellings are read. The port is validated here rather than
// trusted: an address that a client cannot dial is worse than a skip, because
// the failure surfaces at connection time instead of at export time.
func proxyAddressAuthority(object map[string]any) (string, bool) {
	server := strings.TrimSpace(mapString(object, "server"))
	port := mapUint(object, "server_port")
	if port == 0 {
		port = mapUint(object, "port")
	}
	if server == "" || port == 0 || port > 65535 {
		return "", false
	}
	return net.JoinHostPort(server, strconv.FormatUint(port, 10)), true
}

// proxyAddressUserinfo renders the credential part of the address, including
// the trailing "@".
//
// A password with no user name is still carried, in the user position, because
// that is how the share-link parsers read it back: emitting it as a bare
// password would produce an address the parser splits differently than the
// exporter wrote it.
func proxyAddressUserinfo(object map[string]any) string {
	username := mapString(object, "username")
	password := mapString(object, "password")
	switch {
	case username != "" && password != "":
		return url.UserPassword(username, password).String() + "@"
	case username != "":
		return url.User(username).String() + "@"
	case password != "":
		return url.User(password).String() + "@"
	}
	return ""
}
