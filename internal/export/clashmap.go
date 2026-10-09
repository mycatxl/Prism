package export

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Reverse mapping sing-box outbound -> mihomo (Clash Meta) proxy (WP11 §2.2).
//
// Every unsupported case returns ok == false so the caller can skip the node
// with a reason instead of emitting a broken proxy entry.

// clashProxyFromSingbox converts one sing-box outbound object.
func clashProxyFromSingbox(object map[string]any) (map[string]any, bool) {
	switch strings.ToLower(mapString(object, "type")) {
	case "shadowsocks":
		return clashShadowsocks(object)
	case "vmess":
		return clashVMess(object)
	case "vless":
		return clashVLESS(object)
	case "trojan":
		return clashTrojan(object)
	case "hysteria2":
		return clashHysteria2(object)
	case "hysteria":
		return clashHysteria(object)
	case "tuic":
		return clashTUIC(object)
	case "anytls":
		return clashAnyTLS(object)
	case "ssh":
		return clashSSH(object)
	case "socks":
		return clashSOCKS(object)
	case "http":
		return clashHTTP(object)
	case "wireguard":
		return clashWireGuard(object)
	case "snell":
		return clashSnell(object)
	case "openvpn-client":
		return clashOpenVPNClient(object)
		// "openconnect" is deliberately absent: mihomo has no openconnect proxy type,
		// and this repository's own Clash importer (subscription.clashProtocolNames)
		// does not recognise one either. Emitting `type: openconnect` would produce a
		// document mihomo refuses to load, so the node is skipped with a reason
		// instead. §2.2 lists only the openvpn-client form for the same reason.
	}
	return nil, false
}

func clashShadowsocks(object map[string]any) (map[string]any, bool) {
	cipher := mapString(object, "method")
	password := mapString(object, "password")
	if cipher == "" || password == "" {
		return nil, false
	}
	proxy := clashBase(object, "ss")
	proxy["cipher"] = cipher
	proxy["password"] = password
	if plugin := strings.ToLower(mapString(object, "plugin")); plugin != "" {
		applyShadowsocksPlugin(proxy, plugin, mapString(object, "plugin_opts"))
	}
	return proxy, true
}

// applyShadowsocksPlugin translates a sing-box shadowsocks plugin into the
// mihomo plugin spelling (obfs and v2ray-plugin are interchangeable).
//
// The two directions must agree on both the write shape and the option names:
// mihomo reads the obfs parameters from plugin-opts.{mode,host}, and this
// repository's own importer canonicalises them to obfs=/obfs-host= (see
// subscription.normalizeSimpleObfsOptions). Writing a top-level obfs/obfs-host
// pair and reading only mode/host lost every obfs parameter on the way back out.
func applyShadowsocksPlugin(proxy map[string]any, plugin string, rawOpts string) {
	options := parsePluginOptions(rawOpts)
	// The same option may arrive under either spelling.
	firstOption := func(keys ...string) string {
		for _, key := range keys {
			if value := options[key]; value != "" {
				return value
			}
		}
		return ""
	}
	switch {
	case strings.Contains(plugin, "obfs"):
		pluginOpts := map[string]any{}
		if mode := firstOption("mode", "obfs"); mode != "" {
			pluginOpts["mode"] = mode
		}
		if host := firstOption("host", "obfs-host", "obfs_host"); host != "" {
			pluginOpts["host"] = host
		}
		proxy["plugin"] = "obfs"
		if len(pluginOpts) > 0 {
			proxy["plugin-opts"] = pluginOpts
		}
	case strings.Contains(plugin, "v2ray-plugin"):
		pluginOpts := map[string]any{
			// v2ray-plugin is a websocket transport by definition.
			"mode": "websocket",
		}
		if options["tls"] != "" {
			pluginOpts["tls"] = true
		}
		if host := options["host"]; host != "" {
			pluginOpts["host"] = host
		}
		if path := options["path"]; path != "" {
			pluginOpts["path"] = path
		}
		proxy["plugin"] = "v2ray-plugin"
		proxy["plugin-opts"] = pluginOpts
	case strings.Contains(plugin, "shadow-tls"):
		// shadow-tls is a detour chain, not a plugin (see clashChainProxy); a
		// bare shadowsocks node carrying it has no Clash equivalent, so the
		// plugin is left off rather than emitted wrongly.
	default:
		// An unknown plugin has no Clash spelling; the proxy stays plain.
	}
}

// parsePluginOptions splits a sing-box plugin_opts string ("a=b;c").
func parsePluginOptions(raw string) map[string]string {
	options := map[string]string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if key, value, ok := strings.Cut(part, "="); ok {
			options[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
			continue
		}
		options[strings.ToLower(part)] = "true"
	}
	return options
}

func clashVMess(object map[string]any) (map[string]any, bool) {
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return nil, false
	}
	proxy := clashBase(object, "vmess")
	proxy["uuid"] = uuid
	proxy["alterId"] = int(mapUint(object, "alter_id"))
	security := mapString(object, "security")
	if security == "" {
		security = "auto"
	}
	proxy["cipher"] = security
	applyClashTLS(proxy, object)
	applyClashV2RayTransport(proxy, object)
	return proxy, true
}

func clashVLESS(object map[string]any) (map[string]any, bool) {
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return nil, false
	}
	proxy := clashBase(object, "vless")
	proxy["uuid"] = uuid
	if flow := mapString(object, "flow"); flow != "" {
		proxy["flow"] = flow
	}
	applyClashTLS(proxy, object)
	applyClashV2RayTransport(proxy, object)
	return proxy, true
}

func clashTrojan(object map[string]any) (map[string]any, bool) {
	password := mapString(object, "password")
	if password == "" {
		return nil, false
	}
	proxy := clashBase(object, "trojan")
	proxy["password"] = password
	applyClashTLS(proxy, object)
	applyClashV2RayTransport(proxy, object)
	return proxy, true
}

func clashHysteria2(object map[string]any) (map[string]any, bool) {
	password := mapString(object, "password")
	if password == "" {
		return nil, false
	}
	proxy := clashBase(object, "hysteria2")
	proxy["password"] = password
	applyClashTLS(proxy, object)
	if obfs := mapObject(object, "obfs"); obfs != nil {
		if obfsType := mapString(obfs, "type"); obfsType != "" {
			proxy["obfs"] = obfsType
		}
		if obfsPassword := mapString(obfs, "password"); obfsPassword != "" {
			proxy["obfs-password"] = obfsPassword
		}
	}
	if ports := mapStringSlice(object, "server_ports"); len(ports) > 0 {
		proxy["ports"] = strings.Join(ports, ",")
	}
	if up := mapUint(object, "up_mbps"); up > 0 {
		proxy["up"] = int(up)
	}
	if down := mapUint(object, "down_mbps"); down > 0 {
		proxy["down"] = int(down)
	}
	return proxy, true
}

func clashHysteria(object map[string]any) (map[string]any, bool) {
	proxy := clashBase(object, "hysteria")
	if auth := mapString(object, "auth_str"); auth != "" {
		proxy["auth-str"] = auth
	}
	if obfs := mapString(object, "obfs"); obfs != "" {
		proxy["obfs"] = obfs
	}
	if ports := mapStringSlice(object, "server_ports"); len(ports) > 0 {
		proxy["ports"] = strings.Join(ports, ",")
	} else if ports := mapString(object, "server_ports"); ports != "" {
		proxy["ports"] = ports
	}
	if up := hysteriaRateFromMap(object, "up_mbps", "up"); up != "" {
		proxy["up"] = up
	}
	if down := hysteriaRateFromMap(object, "down_mbps", "down"); down != "" {
		proxy["down"] = down
	}
	applyClashTLS(proxy, object)
	return proxy, true
}

// hysteriaRateFromMap renders a hysteria up/down value the way mihomo expects
// it: a number keeps its string form, an explicit rate keeps its unit.
func hysteriaRateFromMap(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		}
	}
	return ""
}

func clashTUIC(object map[string]any) (map[string]any, bool) {
	uuid := mapString(object, "uuid")
	if uuid == "" {
		return nil, false
	}
	proxy := clashBase(object, "tuic")
	proxy["uuid"] = uuid
	if password := mapString(object, "password"); password != "" {
		proxy["password"] = password
	}
	if controller := mapString(object, "congestion_control"); controller != "" {
		proxy["congestion-controller"] = controller
	}
	if relayMode := mapString(object, "udp_relay_mode"); relayMode != "" {
		proxy["udp-relay-mode"] = relayMode
	}
	applyClashTLS(proxy, object)
	return proxy, true
}

func clashAnyTLS(object map[string]any) (map[string]any, bool) {
	password := mapString(object, "password")
	if password == "" {
		return nil, false
	}
	proxy := clashBase(object, "anytls")
	proxy["password"] = password
	applyClashTLS(proxy, object)
	return proxy, true
}

func clashSSH(object map[string]any) (map[string]any, bool) {
	proxy := clashBase(object, "ssh")
	if user := mapString(object, "user"); user != "" {
		proxy["username"] = user
	}
	if password := mapString(object, "password"); password != "" {
		proxy["password"] = password
	}
	if privateKey := mapString(object, "private_key"); privateKey != "" {
		proxy["private-key"] = privateKey
	}
	if passphrase := mapString(object, "private_key_passphrase"); passphrase != "" {
		proxy["private-key-passphrase"] = passphrase
	}
	return proxy, true
}

func clashSOCKS(object map[string]any) (map[string]any, bool) {
	proxy := clashBase(object, "socks5")
	if username := mapString(object, "username"); username != "" {
		proxy["username"] = username
	}
	if password := mapString(object, "password"); password != "" {
		proxy["password"] = password
	}
	return proxy, true
}

func clashHTTP(object map[string]any) (map[string]any, bool) {
	proxy := clashBase(object, "http")
	if username := mapString(object, "username"); username != "" {
		proxy["username"] = username
	}
	if password := mapString(object, "password"); password != "" {
		proxy["password"] = password
	}
	applyClashTLS(proxy, object)
	return proxy, true
}

func clashSnell(object map[string]any) (map[string]any, bool) {
	psk := mapString(object, "psk")
	if psk == "" {
		return nil, false
	}
	if version := mapUint(object, "version"); version != 0 && version != 4 {
		// sing-box 1.14 speaks Snell v4/v6; this reverse mapping covers v4.
		return nil, false
	}
	proxy := clashBase(object, "snell")
	proxy["psk"] = psk
	proxy["version"] = 4
	if mode := mapString(object, "obfs_mode"); mode != "" {
		obfs := map[string]any{"mode": mode}
		if host := mapString(object, "obfs_host"); host != "" {
			obfs["host"] = host
		}
		proxy["obfs-opts"] = obfs
	}
	return proxy, true
}

func clashWireGuard(object map[string]any) (map[string]any, bool) {
	privateKey := mapString(object, "private_key")
	if privateKey == "" {
		return nil, false
	}
	peers := mapObjectSlice(object, "peers")
	if len(peers) == 0 {
		return nil, false
	}
	peer := peers[0]
	server := mapString(peer, "address")
	// mihomo dials a literal endpoint address; a host name there would change
	// the dial path (mihomo uses the system resolver for it), so Prism refuses
	// instead of emitting a proxy whose behaviour differs from the node.
	if server == "" || !looksLikeIPLiteral(server) {
		return nil, false
	}
	port := mapUint(peer, "port")
	if port == 0 {
		return nil, false
	}
	publicKey := mapString(peer, "public_key")
	if publicKey == "" {
		return nil, false
	}
	proxy := clashBase(object, "wireguard")
	proxy["server"] = server
	proxy["port"] = int(port)
	proxy["private-key"] = privateKey
	proxy["public-key"] = publicKey
	if preSharedKey := mapString(peer, "pre_shared_key"); preSharedKey != "" {
		proxy["pre-shared-key"] = preSharedKey
	}
	// The canonical form stores `reserved` as a []uint8, which Go's JSON codec
	// renders as a base64 string ("AQID"), so the number-array reader never
	// matched and every WARP-style node lost its reserved bytes and failed the
	// handshake on the client. Both spellings are accepted.
	if reserved := wireGuardReserved(peer); len(reserved) == 3 {
		proxy["reserved"] = reserved
	}
	if allowedIPs := mapStringSlice(peer, "allowed_ips"); len(allowedIPs) > 0 {
		proxy["allowed-ips"] = allowedIPs
	}
	var ipv4, ipv6 []string
	for _, address := range mapStringSlice(object, "address") {
		if strings.Contains(address, ":") {
			ipv6 = append(ipv6, address)
		} else {
			ipv4 = append(ipv4, address)
		}
	}
	if len(ipv4) > 0 {
		proxy["ip"] = strings.Join(ipv4, ",")
	}
	if len(ipv6) > 0 {
		proxy["ipv6"] = strings.Join(ipv6, ",")
	}
	if mtu := mapUint(object, "mtu"); mtu > 0 {
		proxy["mtu"] = int(mtu)
	}
	return proxy, true
}

// looksLikeIPLiteral reports whether value is an IPv4 or IPv6 literal.
func looksLikeIPLiteral(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, ":") {
		return true
	}
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || number > 255 {
			return false
		}
	}
	return true
}

func clashOpenVPNClient(object map[string]any) (map[string]any, bool) {
	// mihomo's OpenVPN proxy implements the TLS mode only.
	if strings.ToLower(mapString(object, "mode")) == "static_key" {
		return nil, false
	}
	servers := mapObjectSlice(object, "servers")
	if len(servers) == 0 {
		return nil, false
	}
	server := mapString(servers[0], "server")
	if server == "" {
		return nil, false
	}
	tls := mapObject(object, "tls")
	if tls == nil {
		return nil, false
	}

	proxy := map[string]any{"type": "openvpn", "server": server}
	if port := mapUint(servers[0], "server_port"); port > 0 {
		proxy["port"] = int(port)
	}
	if proto := firstNonEmptyString(mapString(servers[0], "network"), mapString(object, "network")); proto != "" {
		proxy["proto"] = openVPNProto(proto)
	}
	if ca := mapStringSlice(tls, "certificate"); len(ca) > 0 {
		proxy["ca"] = strings.Join(ca, "\n")
	}
	if cert := mapStringSlice(tls, "client_certificate"); len(cert) > 0 {
		proxy["cert"] = strings.Join(cert, "\n")
	}
	if key := mapStringSlice(tls, "client_key"); len(key) > 0 {
		proxy["key"] = strings.Join(key, "\n")
	}
	if wrap := mapObject(tls, "control_wrap"); wrap != nil {
		if key := mapStringSlice(wrap, "key"); len(key) > 0 {
			target := "tls-auth"
			if wrapType := mapString(wrap, "type"); wrapType != "" && wrapType != "tls_auth" {
				target = strings.ReplaceAll(wrapType, "_", "-")
			}
			proxy[target] = strings.Join(key, "\n")
			if direction := mapString(wrap, "direction"); direction != "" {
				proxy["key-direction"] = direction
			}
		}
	}
	if cipher := firstNonEmptyString(mapString(object, "data_ciphers_fallback"), mapString(tls, "cipher")); cipher != "" {
		proxy["cipher"] = cipher
	}
	if ciphers := mapStringSlice(object, "data_ciphers"); len(ciphers) > 0 {
		proxy["data-ciphers"] = ciphers
	}
	if auth := mapString(object, "auth"); auth != "" {
		proxy["auth"] = auth
	}
	if username := mapString(object, "username"); username != "" {
		proxy["username"] = username
	}
	if password := mapString(object, "password"); password != "" {
		proxy["password"] = password
	}
	return proxy, true
}

// openVPNProto renders an OpenVPN network value the way mihomo expects it.
func openVPNProto(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "tcp", "tcp4", "tcp6":
		return "tcp"
	default:
		return "udp"
	}
}

// clashChainProxy converts the one chain shape mihomo can express: a
// shadowsocks node whose first hop is shadow-tls becomes the ss + shadow-tls
// plugin form. Every other chain has no Clash equivalent.
func clashChainProxy(item preparedItem) (map[string]any, bool) {
	if len(item.Doc.Deps) == 0 {
		return nil, false
	}
	main, err := objectMap(item.Doc.Main)
	if err != nil || strings.ToLower(mapString(main, "type")) != "shadowsocks" {
		return nil, false
	}
	dep, err := objectMap(item.Doc.Deps[0])
	if err != nil || strings.ToLower(mapString(dep, "type")) != "shadowtls" {
		return nil, false
	}
	proxy, ok := clashShadowsocks(main)
	if !ok {
		return nil, false
	}
	pluginOpts := map[string]any{}
	// The Clash shadow-tls plugin's `host` is the *disguise* SNI the client
	// presents, not the server it connects to (that is the proxy's own server).
	// The importer stores it in the dep's tls.server_name (chains.go), so
	// reading dep.server here replaced the front domain with the real address and
	// broke the handshake: the server saw the wrong SNI. Fall back to dep.server
	// only for a document that carries no TLS block at all.
	host := mapString(dep, "server")
	if tls := mapObject(dep, "tls"); tls != nil {
		if serverName := mapString(tls, "server_name"); serverName != "" {
			host = serverName
		}
	}
	if host != "" {
		pluginOpts["host"] = host
	}
	if password := mapString(dep, "password"); password != "" {
		pluginOpts["password"] = password
	}
	if version := mapUint(dep, "version"); version > 0 {
		pluginOpts["version"] = int(version)
	}
	if len(pluginOpts) == 0 {
		return nil, false
	}
	proxy["plugin"] = "shadow-tls"
	proxy["plugin-opts"] = pluginOpts
	return proxy, true
}

// clashBase renders the fields every proxy type shares.
func clashBase(object map[string]any, clashType string) map[string]any {
	proxy := map[string]any{"type": clashType}
	if server := mapString(object, "server"); server != "" {
		proxy["server"] = server
	}
	if port := mapUint(object, "server_port"); port > 0 {
		proxy["port"] = int(port)
	}
	return proxy
}

// clashTLS is the subset of a sing-box TLS object the Clash mapping needs.
type clashTLS struct {
	Enabled     bool
	ServerName  string
	Insecure    bool
	Fingerprint string
	Reality     bool
	PublicKey   string
	ShortID     string
	ALPN        []string
}

func parseClashTLS(object map[string]any) (clashTLS, bool) {
	raw, ok := object["tls"]
	if !ok || raw == nil {
		return clashTLS{}, false
	}
	tls := clashTLS{}
	switch typed := raw.(type) {
	case bool:
		tls.Enabled = typed
	case map[string]any:
		if enabled, ok := typed["enabled"].(bool); ok {
			tls.Enabled = enabled
		}
		tls.ServerName = mapString(typed, "server_name")
		if insecure, ok := typed["insecure"].(bool); ok {
			tls.Insecure = insecure
		}
		if utls := mapObject(typed, "utls"); utls != nil {
			tls.Fingerprint = mapString(utls, "fingerprint")
		}
		if reality := mapObject(typed, "reality"); reality != nil && tls.Enabled {
			tls.Reality = true
			tls.PublicKey = mapString(reality, "public_key")
			tls.ShortID = mapString(reality, "short_id")
		}
		tls.ALPN = mapStringSlice(typed, "alpn")
	default:
		return clashTLS{}, false
	}
	return tls, true
}

// applyClashTLS fills the Clash TLS fields shared by vmess, vless, trojan,
// hysteria, hysteria2, tuic, anytls and http.
func applyClashTLS(proxy map[string]any, object map[string]any) {
	tls, ok := parseClashTLS(object)
	if !ok {
		return
	}
	if tls.Enabled {
		proxy["tls"] = true
	}
	// The SNI fields belong to the TLS block, so they are gated on the same flag
	// the rest of it is. Emitting `sni` without `tls: true` produced a proxy that
	// claims a server name while running in plaintext, which is not a shape any
	// node can produce — and reality was already gated while these were not.
	if tls.Enabled && tls.ServerName != "" {
		proxy["sni"] = tls.ServerName
		proxy["servername"] = tls.ServerName
	}
	if tls.Insecure {
		proxy["skip-cert-verify"] = true
	}
	if len(tls.ALPN) > 0 {
		proxy["alpn"] = tls.ALPN
	}
	if tls.Fingerprint != "" {
		proxy["client-fingerprint"] = tls.Fingerprint
	}
	if tls.Reality {
		reality := map[string]any{}
		if tls.PublicKey != "" {
			reality["public-key"] = tls.PublicKey
		}
		if tls.ShortID != "" {
			reality["short-id"] = tls.ShortID
		}
		if len(reality) > 0 {
			proxy["reality-opts"] = reality
		}
	}
}

// applyClashV2RayTransport maps the sing-box transport object onto the Clash
// network fields.
func applyClashV2RayTransport(proxy map[string]any, object map[string]any) {
	transport := mapObject(object, "transport")
	if transport == nil {
		return
	}
	switch strings.ToLower(mapString(transport, "type")) {
	case "ws":
		proxy["network"] = "ws"
		wsOpts := map[string]any{}
		if path := mapString(transport, "path"); path != "" {
			wsOpts["path"] = path
		}
		if headers := mapObject(transport, "headers"); headers != nil {
			if host := mapString(headers, "Host"); host != "" {
				wsOpts["headers"] = map[string]any{"Host": host}
			}
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
	case "grpc":
		proxy["network"] = "grpc"
		if serviceName := mapString(transport, "service_name"); serviceName != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "http":
		proxy["network"] = "h2"
		httpOpts := map[string]any{}
		if path := mapString(transport, "path"); path != "" {
			httpOpts["path"] = path
		}
		if hosts := mapStringSlice(transport, "host"); len(hosts) > 0 {
			httpOpts["host"] = hosts
		}
		if len(httpOpts) > 0 {
			proxy["h2-opts"] = httpOpts
		}
	case "httpupgrade":
		// httpupgrade is its own Clash network type. Writing `network: http` here
		// made the round trip lossy: the importer reads `http` as the sing-box
		// `http` transport (the h2 family) and only `httpupgrade` as this one, so
		// an exported node came back as a different transport and stopped working.
		proxy["network"] = "httpupgrade"
		httpOpts := map[string]any{}
		if path := mapString(transport, "path"); path != "" {
			httpOpts["path"] = path
		}
		if host := mapString(transport, "host"); host != "" {
			httpOpts["host"] = host
		}
		if headers := mapObject(transport, "headers"); len(headers) > 0 {
			httpOpts["headers"] = headers
		}
		if len(httpOpts) > 0 {
			proxy["http-upgrade-opts"] = httpOpts
		}
	case "quic":
		proxy["network"] = "quic"
	default:
		// A transport Clash cannot express leaves the proxy on its default
		// network; the node stays dialable in the common case.
	}
}

// --- small typed accessors over a decoded JSON object ---

func mapString(object map[string]any, key string) string {
	value, ok := object[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	}
	return ""
}

func mapUint(object map[string]any, key string) uint64 {
	value, ok := object[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}
	return 0
}

func mapUintSlice(object map[string]any, key string) []int {
	value, ok := object[key]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		number, ok := item.(float64)
		if !ok {
			return nil
		}
		out = append(out, int(number))
	}
	return out
}

// wireGuardReserved reads a wireguard peer's `reserved` field in either spelling.
//
// The canonical node form (node.WireGuardPeer.Reserved) is a []uint8, which Go's
// JSON codec encodes as a base64 string, so the value that reaches the exporter is
// "AQID" and not [1,2,3]. The number-array form is accepted too, because a
// hand-written or legacy document may use it.
func wireGuardReserved(peer map[string]any) []int {
	value, ok := peer["reserved"]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case string:
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(typed))
		if err != nil {
			return nil
		}
		out := make([]int, 0, len(decoded))
		for _, b := range decoded {
			out = append(out, int(b))
		}
		return out
	case []any:
		out := make([]int, 0, len(typed))
		for _, item := range typed {
			number, ok := item.(float64)
			if !ok {
				return nil
			}
			out = append(out, int(number))
		}
		return out
	default:
		return nil
	}
}

func mapStringSlice(object map[string]any, key string) []string {
	value, ok := object[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, text)
		}
		return out
	}
	return nil
}

func mapObject(object map[string]any, key string) map[string]any {
	value, ok := object[key]
	if !ok {
		return nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return typed
}

func mapObjectSlice(object map[string]any, key string) []map[string]any {
	value, ok := object[key]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		typed, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, typed)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
