package export

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"prism/internal/node"
)

// Share-link export. The v2rayn format is the very same URI lines
// with the whole list base64-encoded, which is what v2rayN subscriptions use.
//
// The exported links are the inverse of internal/subscription's share-link
// parsers: export -> parse yields the same node hash (the hash ignores the
// tag/naming fields). The one exception is a node that carries several
// tls.certificate entries: the share link has a single ca-str parameter, so they
// come back as one entry holding the newline join, which is the same trust
// material to sing-box but a different document.

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
		// base64 payload, so the export always uses a modern spelling. The
		// scheme still has to follow the node's version: a SOCKS4 node exported
		// as socks5:// came back as SOCKS5, which is a different protocol (no
		// username/password auth, different remote-DNS semantics).
		return proxyLink(socksShareLinkScheme(object), object, item.Name)
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
	// AllowInsecure is the v2rayN spelling of "do not verify the certificate".
	// parseVmessFromMap reads it out of this same payload (allowInsecure,
	// allow_insecure, insecure, ...), so omitting it silently turned an
	// insecure node into a verifying one after the round trip.
	AllowInsecure bool `json:"allowInsecure,omitempty"`
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
		// Only inside a TLS block: the parser treats allowInsecure as a reason
		// to enable TLS, so writing it for a plaintext node would add a TLS
		// layer the node never had.
		payload.AllowInsecure = tls.Enabled && tls.Insecure
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
	// The certificate material lives in the raw TLS object, which clashTLS does
	// not carry. Dropping it is a security downgrade: the imported node would
	// fall back to the system roots instead of pinning the server's key.
	applyHysteria2CertificateQuery(query, object)
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

// applyHysteria2CertificateQuery writes the sing-box certificate material of a
// hysteria2 TLS object as the share-link parameters the parser reads back:
//
//	tls.certificate_public_key_sha256 -> pinSHA256 (comma separated)
//	tls.certificate_path              -> ca
//	tls.certificate                   -> ca-str (one PEM document)
//
// Every one of them is a trust anchor, so dropping any of them silently
// re-enables the system roots for a node that pinned its server.
func applyHysteria2CertificateQuery(query url.Values, object map[string]any) {
	tls := mapObject(object, "tls")
	if tls == nil {
		return
	}
	if pins := mapStringSlice(tls, "certificate_public_key_sha256"); len(pins) > 0 {
		query.Set("pinSHA256", strings.Join(pins, ","))
	}
	if certificatePath := mapString(tls, "certificate_path"); certificatePath != "" {
		query.Set("ca", certificatePath)
	}
	// sing-box joins the certificate list with "\n" before parsing it, and the
	// parser stores a single ca-str back into tls.certificate as one entry, so
	// the newline is the faithful join. url.Values.Encode percent-encodes it.
	if certificates := mapStringSlice(tls, "certificate"); len(certificates) > 0 {
		query.Set("ca-str", strings.Join(certificates, "\n"))
	}
}

// shareLinkRateSupported reports whether the Mbps rate fields of a node can be
// expressed as a share-link up/down value.
//
// The share link carries the rate as decimal digits that the parser reads back
// with strconv.ParseUint, so a value that is not a whole positive number does
// not survive it: a string would be dropped and a fraction would lose its
// fractional part. sing-box is no more permissive - its up_mbps is an int and
// it refuses both `"20"` and `20.5` - so such a node is skipped rather than
// exported with a rate that differs from the one it had.
func shareLinkRateSupported(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		number, ok := value.(float64)
		if !ok {
			return false
		}
		if number != math.Trunc(number) {
			return false
		}
	}
	return true
}

// shareLinkMbps renders one numeric Mbps field for the share-link up/down
// parameter. A zero (or negative) value means "not set" in sing-box, so it is
// left out rather than written as 0; the value is always a whole number, which
// shareLinkRateSupported has already checked for the fields it guards.
func shareLinkMbps(object map[string]any, keys ...string) string {
	for _, key := range keys {
		number, ok := object[key].(float64)
		if !ok || number <= 0 {
			continue
		}
		return strconv.FormatUint(uint64(number), 10)
	}
	return ""
}

// bytesPerMbps is the byte rate one whole Mbps stands for in sing-box:
// sing-quic's hysteria.MbpsToBps is 125000, and byteformats spells the same
// number as the network unit "Mbps" (MByte/8). That equality is what makes
// `up: "20 Mbps"` and `up_mbps: 20` the same rate, so a byte rate that is a
// whole number of Mbps round-trips through the share link exactly.
const bytesPerMbps = 125000

// networkUnitValues is sing-box's byteformats networkUnitValueTable, the
// case-sensitive table a byte rate is resolved against first.
var networkUnitValues = map[string]uint64{
	"Bps":  1,
	"Kbps": 125,
	"KBps": 1000,
	"Mbps": bytesPerMbps,
	"MBps": 1000000,
	"Gbps": 125000000,
	"GBps": 1000000000,
	"Tbps": 125000000000,
	"TBps": 1000000000000,
	"Pbps": 125000000000000,
	"PBps": 1000000000000000,
	"Ebps": 125000000000000000,
	"EBps": 1000000000000000000,
}

// byteUnitValues is byteformats' lower-case fallback table (unitValueTable),
// which byteformats.NetworkBytesCompat falls back to when the case-sensitive
// lookup misses.
var byteUnitValues = map[string]uint64{
	"b":   1,
	"k":   1000,
	"kb":  1000,
	"ki":  1 << 10,
	"kib": 1 << 10,
	"m":   1000000,
	"mb":  1000000,
	"mi":  1 << 20,
	"mib": 1 << 20,
	"g":   1000000000,
	"gb":  1000000000,
	"gi":  1 << 30,
	"gib": 1 << 30,
	"t":   1000000000000,
	"tb":  1000000000000,
	"ti":  1 << 40,
	"tib": 1 << 40,
	"p":   1000000000000000,
	"pb":  1000000000000000,
	"pi":  1 << 50,
	"pib": 1 << 50,
	"e":   1000000000000000000,
	"eb":  1000000000000000000,
	"ei":  1 << 60,
	"eib": 1 << 60,
}

// hysteriaShareLinkRates resolves the up and down parameters of a hysteria v1
// share link.
//
// sing-box's hysteria outbound takes up_mbps/down_mbps (whole Mbps) or up/down
// (a byte rate) and prefers the byte rate when both are set, so the byte rate
// wins whenever it carries a value. The share link has no byte-rate parameter,
// but whole Mbps is an exact unit for it: a byte rate of N is N/125000 Mbps
// (see bytesPerMbps). A rate that is not a whole number of Mbps cannot be
// written without changing the node's effective rate, and ok is false for it so
// the caller skips the node instead of exporting a wrong number.
func hysteriaShareLinkRates(object map[string]any) (up string, down string, ok bool) {
	for _, field := range []struct {
		byteKey string
		mbpsKey string
		out     *string
	}{
		{byteKey: "up", mbpsKey: "up_mbps", out: &up},
		{byteKey: "down", mbpsKey: "down_mbps", out: &down},
	} {
		mbps, set, expressible := hysteriaByteRateMbps(object[field.byteKey])
		if set {
			if !expressible {
				return "", "", false
			}
			*field.out = strconv.FormatUint(mbps, 10)
			continue
		}
		// No byte rate: the Mbps pair is the rate. sing-box types it as an
		// int, so a non-number is not a rate Prism can carry over, and a
		// fractional one is not a rate the kernel would accept either.
		value, exists := object[field.mbpsKey]
		if !exists || value == nil {
			continue
		}
		number, isNumber := value.(float64)
		if !isNumber {
			return "", "", false
		}
		if number != math.Trunc(number) {
			return "", "", false
		}
		*field.out = shareLinkMbps(object, field.mbpsKey)
	}
	return up, down, true
}

// hysteriaByteRateMbps converts one sing-box byte-rate field (up/down) into
// whole Mbps. set reports whether the field carries a rate at all: an empty or
// zero value means "not set" to sing-box, which then falls back to the Mbps
// field. expressible reports whether the rate is a whole number of Mbps.
func hysteriaByteRateMbps(value any) (mbps uint64, set bool, expressible bool) {
	var bytes uint64
	switch typed := value.(type) {
	case float64:
		switch {
		case typed == 0:
			return 0, false, false
		case typed < 0, typed != math.Trunc(typed), typed >= math.Exp2(64):
			return 0, true, false
		}
		bytes = uint64(typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false, false
		}
		parsed, parsedOK := parseNetworkByteRate(text)
		if !parsedOK {
			return 0, true, false
		}
		if parsed == 0 {
			return 0, false, false
		}
		bytes = parsed
	default:
		return 0, false, false
	}
	if bytes%bytesPerMbps != 0 {
		return 0, true, false
	}
	return bytes / bytesPerMbps, true, true
}

// parseNetworkByteRate parses a byte-rate string the way sing-box's
// byteformats.NetworkBytesCompat does: decimal digits followed by a unit,
// resolved against the case-sensitive network table first and the lower-case
// fallback table second. The unit is mandatory for sing-box too, so a bare
// "2500000" is not a rate either side accepts.
func parseNetworkByteRate(text string) (uint64, bool) {
	index := 0
	for index < len(text) && text[index] >= '0' && text[index] <= '9' {
		index++
	}
	if index == 0 {
		return 0, false
	}
	value, err := strconv.ParseUint(text[:index], 10, 64)
	if err != nil {
		return 0, false
	}
	unit := strings.TrimSpace(text[index:])
	if unit == "" {
		return 0, false
	}
	unitValue, ok := networkUnitValues[unit]
	if !ok {
		unitValue, ok = byteUnitValues[strings.ToLower(unit)]
		if !ok {
			return 0, false
		}
	}
	if unitValue == 0 || value > math.MaxUint64/unitValue {
		return 0, false
	}
	return value * unitValue, true
}

func hysteriaLink(object map[string]any, name string) (string, bool) {
	// sing-box's hysteria outbound takes either up_mbps/down_mbps (whole Mbps)
	// or up/down (a byte rate), and prefers the byte rate when both are set.
	// Whole Mbps is an exact unit for a byte rate (125000 bytes = 1 Mbps), so
	// the share link carries both spellings losslessly; only a rate that is not
	// a whole number of Mbps makes the node unrepresentable.
	up, down, ok := hysteriaShareLinkRates(object)
	if !ok {
		return "", false
	}
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
	// up/down are whole Mbps, which is the share-link unit the parser reads
	// back into up_mbps/down_mbps.
	if up != "" {
		query.Set("up", up)
	}
	if down != "" {
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

// socksNodeVersion normalises the node's SOCKS version field, shared by the two
// writers below. sing-box's socks outbound spells it "4", "4a" or "5"
// (option/socks.go, enum:"4,4a,5"); a missing or unrecognised value means the
// default, SOCKS5.
func socksNodeVersion(object map[string]any) string {
	switch strings.TrimSpace(strings.ToLower(mapString(object, "version"))) {
	case "4":
		return "4"
	case "4a":
		return "4a"
	case "5":
		return "5"
	default:
		return ""
	}
}

// socksShareLinkScheme picks the share-link scheme that carries the node's
// SOCKS version. The URI parser maps socks4:// / socks4a:// back to "4" / "4a"
// and leaves the field unset for socks5://, so the scheme is the only place the
// legacy version can live. An unknown or missing value keeps socks5, the
// spelling every modern node uses.
func socksShareLinkScheme(object map[string]any) string {
	switch socksNodeVersion(object) {
	case "4":
		return "socks4"
	case "4a":
		return "socks4a"
	default:
		return "socks5"
	}
}

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
	// The canonical peer carries the three bytes as the base64 string Go's JSON
	// codec writes for []uint8, so a reader that only understood the number
	// array never saw them; wireGuardReserved reads both spellings.
	if reserved := wireGuardReserved(peer); len(reserved) == 3 {
		query.Set("reserved", strconv.Itoa(reserved[0])+","+strconv.Itoa(reserved[1])+","+strconv.Itoa(reserved[2]))
	}
	if mtu := mapUint(object, "mtu"); mtu > 0 {
		query.Set("mtu", strconv.FormatUint(mtu, 10))
	}

	authority := net.JoinHostPort(server, strconv.FormatUint(port, 10))
	return linkURL("wireguard", url.User(privateKey).String()+"@"+authority, query, item.Name), true
}
