package subscription

import (
	"net/url"
	"strconv"
	"strings"

	"prism/internal/node"
)

// Share-link parsers added by WP06 §8 for the sing-box path.

// parseTuicURI parses tuic:// share links into a sing-box tuic outbound.
func parseTuicURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return ParsedNode{}, false
	}
	server := strings.TrimSpace(u.Hostname())
	if server == "" {
		return ParsedNode{}, false
	}
	if !isProxyURIPathAllowed(u.Path) {
		return ParsedNode{}, false
	}
	port, ok := parseRequiredURIPort(u)
	if !ok {
		return ParsedNode{}, false
	}
	if u.User == nil {
		return ParsedNode{}, false
	}
	uuid := strings.TrimSpace(u.User.Username())
	uuid = decodeUserinfo(uuid)
	password, _ := u.User.Password()
	password = decodeUserinfo(password)
	if uuid == "" {
		return ParsedNode{}, false
	}

	query := u.Query()
	tls := newClashEnabledTLS(strings.TrimSpace(query.Get("sni")), queryBool(query, "allow_insecure", "insecure", "allowinsecure"), splitALPN(query.Get("alpn")))
	if queryBool(query, "disable_sni") {
		tls["disable_sni"] = true
	}
	outbound := map[string]any{
		"type":        "tuic",
		"tag":         defaultTag(decodeTag(u.Fragment), "tuic", server, port),
		"server":      server,
		"server_port": port,
		"uuid":        uuid,
		"tls":         tls,
	}
	if password != "" {
		outbound["password"] = password
	}
	if congestion := strings.TrimSpace(query.Get("congestion_control")); congestion != "" {
		outbound["congestion_control"] = congestion
	}
	if relayMode := strings.TrimSpace(firstNonEmpty(query.Get("udp_relay_mode"), query.Get("udp_relay"))); relayMode != "" {
		outbound["udp_relay_mode"] = relayMode
	}
	return buildParsedNode(outbound)
}

// parseHysteriaURI parses hysteria:// (v1) share links into a hysteria outbound.
func parseHysteriaURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return ParsedNode{}, false
	}
	server := strings.TrimSpace(u.Hostname())
	if server == "" {
		return ParsedNode{}, false
	}
	if !isProxyURIPathAllowed(u.Path) {
		return ParsedNode{}, false
	}
	port, ok := parseRequiredURIPort(u)
	if !ok {
		return ParsedNode{}, false
	}

	query := u.Query()
	if protocol := strings.ToLower(strings.TrimSpace(query.Get("protocol"))); protocol != "" && protocol != "udp" {
		// sing-box has no hysteria v1 QUIC protocol selector.
		return ParsedNode{}, false
	}

	auth := strings.TrimSpace(firstNonEmpty(query.Get("auth"), query.Get("auth_str"), query.Get("authstr")))
	if auth == "" && u.User != nil {
		auth = strings.TrimSpace(u.User.Username())
		auth = decodeUserinfo(auth)
	}
	tls := newClashEnabledTLS(
		strings.TrimSpace(firstNonEmpty(query.Get("peer"), query.Get("sni"))),
		queryBool(query, "insecure", "allow_insecure", "allowinsecure"),
		splitALPN(query.Get("alpn")),
	)

	outbound := map[string]any{
		"type":        "hysteria",
		"tag":         defaultTag(decodeTag(u.Fragment), "hysteria", server, port),
		"server":      server,
		"server_port": port,
		"tls":         tls,
	}
	if auth != "" {
		outbound["auth_str"] = auth
	}
	if up := parseShareLinkRate(firstNonEmpty(query.Get("upmbps"), query.Get("up_mbps"), query.Get("up"))); up != 0 {
		outbound["up_mbps"] = up
	}
	if down := parseShareLinkRate(firstNonEmpty(query.Get("downmbps"), query.Get("down_mbps"), query.Get("down"))); down != 0 {
		outbound["down_mbps"] = down
	}
	if obfs := strings.TrimSpace(firstNonEmpty(query.Get("obfsParam"), query.Get("obfs-param"), query.Get("obfs"))); obfs != "" {
		outbound["obfs"] = obfs
	}
	if ports := normalizeHysteriaPortList(query.Get("mport")); len(ports) > 0 {
		outbound["server_ports"] = ports
	}
	if hopInterval := strings.TrimSpace(query.Get("hop-interval")); hopInterval != "" {
		outbound["hop_interval"] = hopInterval
	}
	if alpn := query.Get("alpn"); alpn != "" && len(tls) == 0 {
		outbound["tls"] = newClashEnabledTLS("", false, splitALPN(alpn))
	}
	return buildParsedNode(outbound)
}

// parseAnyTLSURI parses anytls:// share links into a sing-box anytls outbound.
func parseAnyTLSURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return ParsedNode{}, false
	}
	server := strings.TrimSpace(u.Hostname())
	if server == "" {
		return ParsedNode{}, false
	}
	if !isProxyURIPathAllowed(u.Path) {
		return ParsedNode{}, false
	}
	port, ok := parseRequiredURIPort(u)
	if !ok {
		return ParsedNode{}, false
	}

	password := ""
	if u.User != nil {
		password = strings.TrimSpace(u.User.Username())
		password = decodeUserinfo(password)
	}
	if password == "" {
		return ParsedNode{}, false
	}

	query := u.Query()
	tls := newClashEnabledTLS(
		strings.TrimSpace(query.Get("sni")),
		queryBool(query, "insecure", "allow_insecure", "allowinsecure"),
		splitALPN(query.Get("alpn")),
	)
	if fingerprint := strings.TrimSpace(firstNonEmpty(query.Get("fp"), query.Get("fingerprint"))); fingerprint != "" {
		applyUTLSFromValue(tls, fingerprint)
	}
	outbound := map[string]any{
		"type":        "anytls",
		"tag":         defaultTag(decodeTag(u.Fragment), "anytls", server, port),
		"server":      server,
		"server_port": port,
		"password":    password,
		"tls":         tls,
	}
	return buildParsedNode(outbound)
}

// parseSSHURI parses ssh:// share links into a sing-box ssh outbound.
// URI-embedded private keys are not supported (WP06 §8).
func parseSSHURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return ParsedNode{}, false
	}
	server := strings.TrimSpace(u.Hostname())
	if server == "" {
		return ParsedNode{}, false
	}
	if !isProxyURIPathAllowed(u.Path) {
		return ParsedNode{}, false
	}
	port := uriPortOrDefault(u, 22)
	if port == 0 {
		return ParsedNode{}, false
	}
	if u.User == nil {
		return ParsedNode{}, false
	}
	user := strings.TrimSpace(u.User.Username())
	user = decodeUserinfo(user)
	if user == "" {
		return ParsedNode{}, false
	}
	password, _ := u.User.Password()
	password = decodeUserinfo(password)
	query := u.Query()
	if keyPath := strings.TrimSpace(firstNonEmpty(query.Get("private_key"), query.Get("privatekey"))); keyPath != "" {
		// A file path is meaningless for the embedded runtime.
		return ParsedNode{}, false
	}

	outbound := map[string]any{
		"type":        "ssh",
		"tag":         defaultTag(decodeTag(u.Fragment), "ssh", server, port),
		"server":      server,
		"server_port": port,
		"user":        user,
	}
	if password != "" {
		outbound["password"] = password
	}
	if hostKey := strings.TrimSpace(firstNonEmpty(query.Get("host_key"), query.Get("hostkey"))); hostKey != "" {
		outbound["host_key"] = []string{hostKey}
	}
	return buildParsedNode(outbound)
}

// parseWireGuardShareURI parses wireguard:// and wg:// share links (§3).
// The node is emitted in form B (endpoint envelope).
func parseWireGuardShareURI(uri string) (ParsedNode, bool) {
	name, endpoint, ok := node.ParseWireGuardURI(uri)
	if !ok {
		return ParsedNode{}, false
	}
	raw, err := endpoint.EnvelopeObject(name)
	if err != nil {
		return ParsedNode{}, false
	}
	tag := name
	if tag == "" {
		tag = firstNonEmpty(endpoint.Peers[0].Address, "wireguard")
	}
	return ParsedNode{Tag: tag, RawOptions: raw}, true
}

// parseShareLinkRate converts up/down rate share-link values into a uint rate.
func parseShareLinkRate(raw string) uint64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// decodeUserinfo percent-decodes a share-link credential. net/url already
// decodes userinfo, so an unconditional unescape would corrupt base64 values
// that contain '+' or '%'.
func decodeUserinfo(raw string) string {
	if strings.Contains(raw, "%") {
		if decoded, err := url.QueryUnescape(raw); err == nil {
			return strings.TrimSpace(decoded)
		}
	}
	return strings.TrimSpace(raw)
}
