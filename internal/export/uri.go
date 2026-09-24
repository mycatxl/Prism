package export

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"prism/internal/node"
)

// Share-link export (WP11 §2.3). The v2rayn format is the very same URI lines
// with the whole list base64-encoded, which is what v2rayN subscriptions use.
//
// The exported links are the inverse of internal/subscription's share-link
// parsers: export -> parse yields the same node hash (the hash ignores the
// tag/naming fields).

// exportV2rayN renders one base64 blob of share-link lines.
func exportV2rayN(items []preparedItem, report *Report) ([]byte, int) {
	lines, exported := shareLinkLines(items, report)
	if len(lines) == 0 {
		return []byte{}, exported
	}
	body := strings.Join(lines, "\n")
	return []byte(base64.StdEncoding.EncodeToString([]byte(body))), exported
}

// exportURI renders plain share-link lines.
func exportURI(items []preparedItem, report *Report) ([]byte, int) {
	lines, exported := shareLinkLines(items, report)
	if len(lines) == 0 {
		return []byte{}, exported
	}
	return []byte(strings.Join(lines, "\n") + "\n"), exported
}

// shareLinkLines renders one share link per representable node.
func shareLinkLines(items []preparedItem, report *Report) ([]string, int) {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		link, ok := shareLink(item)
		if !ok {
			link, ok = wireguardLink(item)
		}
		if !ok {
			skip[struct{}](report, item.Name, NotRepresentable(FormatURI))
			continue
		}
		lines = append(lines, link)
	}
	return lines, len(lines)
}

// shareLink renders one node as a share link. ok is false for a node shape or
// protocol the clean share-link syntax cannot carry.
func shareLink(item preparedItem) (string, bool) {
	if item.Doc.Engine != node.EngineSingbox || item.Doc.Kind != node.DocOutbound {
		return "", false
	}
	object, err := objectMap(item.Doc.Main)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(mapString(object, "type")) {
	case "vmess":
		return vmessLink(object, item.Name)
	case "vless":
		return v2rayLink("vless", object, item.Name)
	case "trojan":
		return trojanLink(object, item.Name)
	case "shadowsocks":
		return shadowsocksLink(object, item.Name)
	case "hysteria2":
		return hysteria2Link(object, item.Name)
	case "hysteria":
		return hysteriaLink(object, item.Name)
	case "tuic":
		return tuicLink(object, item.Name)
	case "anytls":
		return anytlsLink(object, item.Name)
	case "socks":
		// socks5:// carries plain userinfo; the legacy socks:// scheme expects a
		// base64 payload, so the export always uses the modern spelling.
		return proxyLink("socks5", object, item.Name)
	case "http":
		return proxyLink("http", object, item.Name)
	}
	return "", false
}

// serverAddress renders the "host:port" authority of a node object.
func serverAddress(object map[string]any) (string, string, uint64, bool) {
	server := strings.TrimSpace(mapString(object, "server"))
	port := mapUint(object, "server_port")
	if server == "" || port == 0 || port > 65535 {
		return "", "", 0, false
	}
	return server, net.JoinHostPort(server, strconv.FormatUint(port, 10)), port, true
}

// --- vmess: v2rayN JSON v2, base64 encoded ---

type vmessShareJSON struct {
	V    string `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port string `json:"port"`
	ID   string `json:"id"`
	Aid  string `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni,omitempty"`
	ALPN string `json:"alpn,omitempty"`
	FP   string `json:"fp,omitempty"`
}

func vmessLink(object map[string]any, name string) (string, bool) {
	server, _, port, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return "", false
	}
	payload := vmessShareJSON{
		V:    "2",
		PS:   name,
		Add:  server,
		Port: strconv.FormatUint(port, 10),
		ID:   uuid,
		Aid:  strconv.FormatUint(mapUint(object, "alter_id"), 10),
		Scy:  mapString(object, "security"),
		Type: "none",
	}
	if payload.Scy == "" {
		payload.Scy = "auto"
	}
	if tls, ok := parseClashTLS(object); ok {
		if tls.Enabled {
			payload.TLS = "tls"
		}
		payload.SNI = tls.ServerName
		payload.ALPN = strings.Join(tls.ALPN, ",")
		payload.FP = tls.Fingerprint
	}
	transport := mapObject(object, "transport")
	if transport != nil {
		switch strings.ToLower(mapString(transport, "type")) {
		case "ws":
			payload.Net = "ws"
			payload.Path = mapString(transport, "path")
			if headers := mapObject(transport, "headers"); headers != nil {
				payload.Host = mapString(headers, "Host")
			}
		case "grpc":
			payload.Net = "grpc"
			payload.Path = mapString(transport, "service_name")
		case "http":
			payload.Net = "h2"
			payload.Path = mapString(transport, "path")
			payload.Host = strings.Join(mapStringSlice(transport, "host"), ",")
		case "httpupgrade":
			payload.Net = "httpupgrade"
			payload.Path = mapString(transport, "path")
			payload.Host = mapString(transport, "host")
		case "quic":
			payload.Net = "quic"
		default:
			payload.Net = "tcp"
		}
	}
	if payload.Net == "" {
		payload.Net = "tcp"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded), true
}

// --- vless: standard query form ---

func v2rayLink(scheme string, object map[string]any, name string) (string, bool) {
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	query := url.Values{}
	security := "none"
	if tls, ok := parseClashTLS(object); ok {
		if tls.Enabled {
			security = "tls"
		}
		if tls.Reality {
			security = "reality"
		}
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("allowInsecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
		if tls.Fingerprint != "" {
			query.Set("fp", tls.Fingerprint)
		}
		if tls.PublicKey != "" {
			query.Set("pbk", tls.PublicKey)
		}
		if tls.ShortID != "" {
			query.Set("sid", tls.ShortID)
		}
	}
	query.Set("security", security)
	if flow := mapString(object, "flow"); flow != "" {
		query.Set("flow", flow)
	}
	applyV2RayTransportQuery(query, object)

	return linkURL(scheme, uuid+"@"+authority, query, name), true
}

func applyV2RayTransportQuery(query url.Values, object map[string]any) {
	transport := mapObject(object, "transport")
	if transport == nil {
		return
	}
	switch strings.ToLower(mapString(transport, "type")) {
	case "ws":
		query.Set("type", "ws")
		if path := mapString(transport, "path"); path != "" {
			query.Set("path", path)
		}
		if headers := mapObject(transport, "headers"); headers != nil {
			if host := mapString(headers, "Host"); host != "" {
				query.Set("host", host)
			}
		}
	case "grpc":
		query.Set("type", "grpc")
		if serviceName := mapString(transport, "service_name"); serviceName != "" {
			query.Set("serviceName", serviceName)
		}
	case "http":
		query.Set("type", "h2")
		if path := mapString(transport, "path"); path != "" {
			query.Set("path", path)
		}
		if hosts := mapStringSlice(transport, "host"); len(hosts) > 0 {
			query.Set("host", strings.Join(hosts, ","))
		}
	case "httpupgrade":
		query.Set("type", "httpupgrade")
		if path := mapString(transport, "path"); path != "" {
			query.Set("path", path)
		}
		if host := mapString(transport, "host"); host != "" {
			query.Set("host", host)
		}
	case "quic":
		query.Set("type", "quic")
	}
}

// --- trojan ---

func trojanLink(object map[string]any, name string) (string, bool) {
	password := mapString(object, "password")
	if password == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	query := url.Values{}
	if tls, ok := parseClashTLS(object); ok {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("allowInsecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
		if tls.Fingerprint != "" {
			query.Set("fp", tls.Fingerprint)
		}
	}
	applyV2RayTransportQuery(query, object)
	return linkURL("trojan", url.User(password).String()+"@"+authority, query, name), true
}

// --- shadowsocks (SIP002) ---

func shadowsocksLink(object map[string]any, name string) (string, bool) {
	method := mapString(object, "method")
	password := mapString(object, "password")
	if method == "" || password == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	query := url.Values{}
	if plugin := mapString(object, "plugin"); plugin != "" {
		spec := strings.ToLower(plugin)
		if opts := strings.TrimSpace(mapString(object, "plugin_opts")); opts != "" {
			spec += ";" + opts
		}
		query.Set("plugin", spec)
	}
	return linkURL("ss", userinfo+"@"+authority, query, name), true
}

// --- hysteria2 / hysteria / tuic / anytls ---

func hysteria2Link(object map[string]any, name string) (string, bool) {
	if !shareLinkRateSupported(object, "up_mbps", "down_mbps") {
		return "", false
	}
	password := mapString(object, "password")
	if password == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	query := url.Values{}
	if tls, ok := parseClashTLS(object); ok {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("insecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
		if tls.Fingerprint != "" {
			query.Set("fp", tls.Fingerprint)
		}
	}
	if ports := mapStringSlice(object, "server_ports"); len(ports) > 0 {
		query.Set("mport", strings.Join(ports, ","))
	}
	if up := mapUint(object, "up_mbps"); up > 0 {
		query.Set("up", strconv.FormatUint(up, 10))
	}
	if down := mapUint(object, "down_mbps"); down > 0 {
		query.Set("down", strconv.FormatUint(down, 10))
	}
	if obfs := mapObject(object, "obfs"); obfs != nil {
		if obfsType := mapString(obfs, "type"); obfsType != "" {
			query.Set("obfs", obfsType)
		}
		if obfsPassword := mapString(obfs, "password"); obfsPassword != "" {
			query.Set("obfs-password", obfsPassword)
		}
	}
	if hop := mapString(object, "hop_interval"); hop != "" {
		query.Set("hop-interval", hop)
	}
	return linkURL("hysteria2", url.User(password).String()+"@"+authority, query, name), true
}

// shareLinkRateSupported reports whether the hysteria byte-rate fields can be
// expressed as a share-link Mbps value. A byte rate is not a Mbps rate, so a
// node carrying one is skipped rather than exported with a wrong number.
func shareLinkRateSupported(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		if _, ok := value.(float64); !ok {
			return false
		}
	}
	return true
}

func hysteriaLink(object map[string]any, name string) (string, bool) {
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	// sing-box's hysteria outbound only speaks UDP, which the share-link
	// `protocol` parameter cannot express, so a non-UDP node is skipped.
	if network := strings.ToLower(mapString(object, "network")); network != "" && network != "udp" {
		return "", false
	}
	query := url.Values{}
	if auth := mapString(object, "auth_str"); auth != "" {
		query.Set("auth", auth)
	}
	if tls, ok := parseClashTLS(object); ok {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("insecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
	}
	if obfs := mapString(object, "obfs"); obfs != "" {
		query.Set("obfs", obfs)
	}
	if ports := mapStringSlice(object, "server_ports"); len(ports) > 0 {
		query.Set("mport", strings.Join(ports, ","))
	}
	if up := hysteriaRateFromMap(object, "up"); up != "" {
		query.Set("up", up)
	}
	if down := hysteriaRateFromMap(object, "down"); down != "" {
		query.Set("down", down)
	}
	return linkURL("hysteria", authority, query, name), true
}

func tuicLink(object map[string]any, name string) (string, bool) {
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	query := url.Values{}
	if tls, ok := parseClashTLS(object); ok {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("allow_insecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
	}
	if controller := mapString(object, "congestion_control"); controller != "" {
		query.Set("congestion_control", controller)
	}
	if relayMode := mapString(object, "udp_relay_mode"); relayMode != "" {
		query.Set("udp_relay_mode", relayMode)
	}
	if zeroRTT, ok := object["zero_rtt_handshake"].(bool); ok && zeroRTT {
		query.Set("reduce-rtt", "1")
	}
	userinfo := url.User(uuid)
	if password := mapString(object, "password"); password != "" {
		userinfo = url.UserPassword(uuid, password)
	}
	return linkURL("tuic", userinfo.String()+"@"+authority, query, name), true
}

func anytlsLink(object map[string]any, name string) (string, bool) {
	password := mapString(object, "password")
	if password == "" {
		return "", false
	}
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	query := url.Values{}
	if tls, ok := parseClashTLS(object); ok {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("insecure", "1")
		}
		if len(tls.ALPN) > 0 {
			query.Set("alpn", strings.Join(tls.ALPN, ","))
		}
		if tls.Fingerprint != "" {
			query.Set("fp", tls.Fingerprint)
		}
	}
	return linkURL("anytls", url.User(password).String()+"@"+authority, query, name), true
}

// --- socks / http ---

func proxyLink(scheme string, object map[string]any, name string) (string, bool) {
	_, authority, _, ok := serverAddress(object)
	if !ok {
		return "", false
	}
	username := mapString(object, "username")
	password := mapString(object, "password")
	tls, _ := parseClashTLS(object)
	if tls.Enabled && scheme == "http" {
		scheme = "https"
	}
	if scheme != "https" && tls.Enabled {
		// A TLS-carrying socks node has no share-link spelling.
		return "", false
	}
	userinfo := ""
	if username != "" {
		if password != "" {
			userinfo = url.UserPassword(username, password).String() + "@"
		} else {
			userinfo = url.User(username).String() + "@"
		}
	} else if password != "" {
		userinfo = url.User(password).String() + "@"
	}
	query := url.Values{}
	if scheme == "https" {
		if tls.ServerName != "" {
			query.Set("sni", tls.ServerName)
		}
		if tls.Insecure {
			query.Set("allowInsecure", "1")
		}
	}
	return linkURL(scheme, userinfo+authority, query, name), true
}

// linkURL renders a share link with a decoded fragment name.
func linkURL(scheme string, authority string, query url.Values, name string) string {
	var builder strings.Builder
	builder.WriteString(scheme)
	builder.WriteString("://")
	builder.WriteString(authority)
	if encoded := query.Encode(); encoded != "" {
		builder.WriteString("/?")
		builder.WriteString(encoded)
	}
	if name != "" {
		builder.WriteString("#")
		builder.WriteString(url.PathEscape(name))
	}
	return builder.String()
}

// wireguardLink renders a form B wireguard endpoint as a wireguard:// share
// link, the inverse of node.ParseWireGuardURI. A multi-peer endpoint, or one
// whose keys or address are missing, is refused rather than exported
// incompletely.
func wireguardLink(item preparedItem) (string, bool) {
	if item.Doc.Engine != node.EngineSingbox || item.Doc.Kind != node.DocEndpoint || item.Doc.Type != "wireguard" {
		return "", false
	}
	object, err := objectMap(item.Doc.Main)
	if err != nil {
		return "", false
	}
	privateKey := mapString(object, "private_key")
	if privateKey == "" {
		return "", false
	}
	peers := mapObjectSlice(object, "peers")
	if len(peers) != 1 {
		return "", false
	}
	peer := peers[0]
	server := strings.TrimSpace(mapString(peer, "address"))
	publicKey := mapString(peer, "public_key")
	if server == "" || publicKey == "" {
		return "", false
	}
	port := mapUint(peer, "port")
	if port == 0 || port > 65535 {
		return "", false
	}
	addresses := mapStringSlice(object, "address")
	if len(addresses) == 0 {
		return "", false
	}

	query := url.Values{}
	query.Set("publickey", publicKey)
	// The link carries one address list; several entries are joined by comma
	// and re-split by the parser.
	query.Set("address", strings.Join(addresses, ","))
	if preSharedKey := mapString(peer, "pre_shared_key"); preSharedKey != "" {
		query.Set("presharedkey", preSharedKey)
	}
	if keepalive := mapUint(peer, "persistent_keepalive_interval"); keepalive > 0 {
		query.Set("keepalive", strconv.FormatUint(keepalive, 10))
	}
	if allowedIPs := mapStringSlice(peer, "allowed_ips"); len(allowedIPs) > 0 {
		query.Set("allowedips", strings.Join(allowedIPs, ","))
	}
	if reserved := mapUintSlice(peer, "reserved"); len(reserved) == 3 {
		query.Set("reserved", strconv.Itoa(reserved[0])+","+strconv.Itoa(reserved[1])+","+strconv.Itoa(reserved[2]))
	}
	if mtu := mapUint(object, "mtu"); mtu > 0 {
		query.Set("mtu", strconv.FormatUint(mtu, 10))
	}

	authority := net.JoinHostPort(server, strconv.FormatUint(port, 10))
	return linkURL("wireguard", url.User(privateKey).String()+"@"+authority, query, item.Name), true
}
