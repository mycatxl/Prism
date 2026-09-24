// OpenVPN .ovpn profile parsing (WP06 §4).
//
// sing-box ships no .ovpn reader: the openvpn-client endpoint only consumes
// option.OpenVPNClientEndpointOptions. This file turns the supported directive
// subset of an OpenVPN client profile into those options, and refuses anything
// the endpoint cannot represent with an explicit reason instead of letting
// box.New fail later (fact F8).
package node

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxOpenVPNProfileBytes bounds a single .ovpn profile.
	maxOpenVPNProfileBytes = 1 << 20
	// maxOpenVPNLineBytes bounds one profile line / PEM line.
	maxOpenVPNLineBytes = 64 * 1024
	// defaultOpenVPNPort is the OpenVPN default remote port.
	defaultOpenVPNPort = 1194
)

// OpenVPNParseError reports why a profile cannot be represented as an
// openvpn-client endpoint. Reason is one of the parse-report reason codes and
// Detail names the offending directive without echoing credentials.
type OpenVPNParseError struct {
	Reason string
	Detail string
}

// Error implements error.
func (e *OpenVPNParseError) Error() string {
	return e.Reason + ": " + e.Detail
}

func openVPNErrorf(reason string, format string, args ...any) *OpenVPNParseError {
	return &OpenVPNParseError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// OpenVPNProfile is a parsed .ovpn client profile.
type OpenVPNProfile struct {
	// Name is the display name (bundle name, or subscription name plus the
	// first remote host).
	Name string
	// Options holds openvpn-client endpoint option fields (without "type").
	Options map[string]any
	// Notes records directives that were accepted and ignored, so the parse
	// report can explain what was dropped.
	Notes []string
	// Remote is the first remote host, used for naming.
	Remote string
}

// Detail renders the ignored-directive notes as one report string.
func (p OpenVPNProfile) Detail() string {
	if len(p.Notes) == 0 {
		return ""
	}
	return "ignored: " + strings.Join(p.Notes, ", ")
}

// MainObject renders the endpoint payload as a sing-box endpoint object.
func (p OpenVPNProfile) MainObject(tag string) (json.RawMessage, error) {
	object := make(map[string]any, len(p.Options)+2)
	for key, value := range p.Options {
		object[key] = value
	}
	object["type"] = "openvpn-client"
	if tag != "" {
		object["tag"] = tag
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode openvpn-client endpoint: %w", err)
	}
	return encoded, nil
}

// EnvelopeObject renders the profile as a form B endpoint document.
func (p OpenVPNProfile) EnvelopeObject(tag string) (json.RawMessage, error) {
	main, err := p.MainObject("")
	if err != nil {
		return nil, err
	}
	envelope := Envelope{
		PrismNode: EnvelopeVersion,
		Engine:    EngineSingbox,
		Kind:      KindEndpoint,
		Name:      p.Name,
		Main:      main,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode openvpn envelope: %w", err)
	}
	return encoded, nil
}

// LooksLikeOpenVPN reports whether text is an OpenVPN client profile rather
// than JSON, YAML, Surge or a share-link list (WP06 §4.1).
func LooksLikeOpenVPN(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '{', '[':
		return false
	}
	var (
		hasRemote bool
		hasClient bool
		hasDevTun bool
		hasCA     bool
		hasSecret bool
		scanner   = bufio.NewScanner(strings.NewReader(trimmed))
	)
	scanner.Buffer(make([]byte, 0, 64*1024), maxOpenVPNLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		switch strings.ToLower(line) {
		case "<ca>":
			hasCA = true
			continue
		case "<secret>":
			hasSecret = true
			continue
		case "client":
			hasClient = true
			continue
		}
		fields := tokenizeOpenVPNLine(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "remote":
			if len(fields) >= 2 {
				hasRemote = true
			}
		case "dev":
			if len(fields) >= 2 && strings.HasPrefix(strings.ToLower(fields[1]), "tun") {
				hasDevTun = true
			}
		case "secret":
			hasSecret = true
		}
	}
	if hasRemote && (hasCA || hasSecret) {
		return true
	}
	return hasClient || hasDevTun || hasCA
}

// openVPNRejectedDirectives lists the directives WP06 §4.2 refuses outright.
// They are either outside what an endpoint can express or would require Prism
// to read files from the host.
var openVPNRejectedDirectives = map[string]string{
	"pkcs12":                "pkcs12 bundles are not supported",
	"http-proxy":            "the outer proxy is chosen by Prism, not by the profile",
	"socks-proxy":           "the outer proxy is chosen by Prism, not by the profile",
	"plugin":                "OpenVPN plugins have no sing-box equivalent",
	"askpass":               "interactive password prompts are not supported",
	"management":            "the management interface is not supported",
	"ifconfig-pool-persist": "server-only directive",
	"server":                "server-only directive",
	"client-config-dir":     "server-only directive",
	"mode":                  "server-only directive",
	"push":                  "server-only directive",
}

// openVPNMappedDirectives lists the directives this parser consumes. Any other
// directive is accepted and recorded as a note so the parse report can explain
// what the profile asked for and did not get.
var openVPNMappedDirectives = map[string]bool{
	"remote":                true,
	"remote-random":         true,
	"proto":                 true,
	"port":                  true,
	"dev":                   true,
	"ca":                    true,
	"cert":                  true,
	"key":                   true,
	"tls-auth":              true,
	"tls-crypt":             true,
	"tls-crypt-v2":          true,
	"secret":                true,
	"ifconfig":              true,
	"key-direction":         true,
	"cipher":                true,
	"data-ciphers":          true,
	"ncp-ciphers":           true,
	"data-ciphers-fallback": true,
	"auth":                  true,
	"verify-x509-name":      true,
	"remote-cert-tls":       true,
	"tls-version-min":       true,
	"tls-version-max":       true,
	"tls-cipher":            true,
	"comp-lzo":              true,
	"compress":              true,
	"allow-compression":     true,
	"tun-mtu":               true,
	"mssfix":                true,
	"fragment":              true,
	"keepalive":             true,
	"ping":                  true,
	"ping-restart":          true,
	"reneg-sec":             true,
	"replay-window":         true,
	"pull-filter":           true,
	"route-nopull":          true,
	"auth-user-pass":        true,
	"peer-fingerprint":      true,
}

// openVPNMaterialDirectives are the directives that carry key material inline.
var openVPNMaterialDirectives = map[string]string{
	"ca":           "ca",
	"cert":         "cert",
	"key":          "key",
	"tls-auth":     "tls-auth",
	"tls-crypt":    "tls-crypt",
	"tls-crypt-v2": "tls-crypt-v2",
	"secret":       "secret",
}

type openVPNDirective struct {
	args      []string
	inline    string
	hasInline bool
}

// ParseOpenVPNProfile parses .ovpn text into openvpn-client endpoint options.
//
// name is used for the display name when the profile itself has none;
// username/password come from a Prism bundle and supply the credentials when
// the profile requests auth-user-pass without an inline credential block.
func ParseOpenVPNProfile(text string, name string, username string, password string) (OpenVPNProfile, error) {
	if len(text) > maxOpenVPNProfileBytes {
		return OpenVPNProfile{}, openVPNErrorf(ReasonInvalid, "profile exceeds %d bytes", maxOpenVPNProfileBytes)
	}
	directives, err := collectOpenVPNDirectives(text)
	if err != nil {
		return OpenVPNProfile{}, err
	}

	builder := &openVPNBuilder{options: map[string]any{}}
	if err := builder.apply(directives, username, password); err != nil {
		return OpenVPNProfile{}, err
	}
	profile := OpenVPNProfile{
		Name:    strings.TrimSpace(name),
		Options: builder.options,
		Notes:   builder.notes(),
		Remote:  builder.firstRemote,
	}
	if profile.Name == "" && profile.Remote != "" {
		profile.Name = "openvpn-" + profile.Remote
	}
	return profile, nil
}

// collectOpenVPNDirectives tokenizes the profile, handling inline blocks.
func collectOpenVPNDirectives(text string) (map[string][]openVPNDirective, error) {
	directives := make(map[string][]openVPNDirective)
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), maxOpenVPNLineBytes)
	var (
		blockName string
		blockBody strings.Builder
	)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if blockName != "" {
			if strings.EqualFold(trimmed, "</"+blockName+">") {
				directives[blockName] = append(directives[blockName], openVPNDirective{
					inline:    blockBody.String(),
					hasInline: true,
				})
				blockName = ""
				blockBody.Reset()
				continue
			}
			blockBody.WriteString(line)
			blockBody.WriteString("\n")
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "<") {
			closeIndex := strings.Index(trimmed, ">")
			if closeIndex > 1 {
				tag := strings.ToLower(strings.TrimSpace(trimmed[1:closeIndex]))
				if !strings.HasPrefix(tag, "/") {
					blockName = tag
					blockBody.Reset()
					continue
				}
			}
			// A closing tag without an opener is not a directive.
			continue
		}
		fields := tokenizeOpenVPNLine(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		directives[name] = append(directives[name], openVPNDirective{args: fields[1:]})
	}
	if err := scanner.Err(); err != nil {
		return nil, openVPNErrorf(ReasonInvalid, "read profile: %v", err)
	}
	if blockName != "" {
		return nil, openVPNErrorf(ReasonInvalid, "unterminated <%s> block", blockName)
	}
	return directives, nil
}

// tokenizeOpenVPNLine splits a directive line honoring single and double quotes.
func tokenizeOpenVPNLine(line string) []string {
	var (
		fields  []string
		current strings.Builder
		quote   rune
		started bool
	)
	flush := func() {
		if !started {
			return
		}
		fields = append(fields, current.String())
		current.Reset()
		started = false
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\r':
			flush()
		default:
			started = true
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

type openVPNBuilder struct {
	options     map[string]any
	noteList    []string
	tls         map[string]any
	servers     []map[string]any
	firstRemote string
	proto       string
	defaultPort uint16
	mode        string
}

func (b *openVPNBuilder) note(format string, args ...any) {
	entry := fmt.Sprintf(format, args...)
	for _, existing := range b.noteList {
		if existing == entry {
			return
		}
	}
	b.noteList = append(b.noteList, entry)
}

func (b *openVPNBuilder) notes() []string {
	if len(b.noteList) == 0 {
		return nil
	}
	sorted := append([]string(nil), b.noteList...)
	sort.Strings(sorted)
	return sorted
}

// sortedNames returns the directive names in a deterministic order.
func sortedNames(directives map[string][]openVPNDirective) []string {
	names := make([]string, 0, len(directives))
	for name := range directives {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// firstDirective returns the value of a directive. An inline block wins over a
// bare occurrence: a profile may carry both a `ca` file path and a later
// `<ca>` block, and only the inline form is usable.
func firstDirective(directives map[string][]openVPNDirective, name string) (openVPNDirective, bool) {
	entries := directives[name]
	if len(entries) == 0 {
		return openVPNDirective{}, false
	}
	for _, entry := range entries {
		if entry.hasInline {
			return entry, true
		}
	}
	return entries[0], true
}

func directiveFirstArg(entry openVPNDirective) string {
	if len(entry.args) == 0 {
		return ""
	}
	return strings.TrimSpace(entry.args[0])
}

// apply walks the profile: refusals first, then device, mode and the rest.
func (b *openVPNBuilder) apply(directives map[string][]openVPNDirective, username string, password string) error {
	if err := b.rejectUnsupported(directives); err != nil {
		return err
	}
	if err := b.applyDev(directives); err != nil {
		return err
	}
	if err := b.applyMode(directives); err != nil {
		return err
	}
	if err := b.applyRemotesAndTuning(directives, username, password); err != nil {
		return err
	}
	for _, name := range sortedNames(directives) {
		if !openVPNMappedDirectives[name] {
			b.note("%s", name)
		}
	}
	return nil
}

func (b *openVPNBuilder) rejectUnsupported(directives map[string][]openVPNDirective) error {
	for _, name := range sortedNames(directives) {
		if detail, rejected := openVPNRejectedDirectives[name]; rejected {
			return openVPNErrorf(ReasonUnsupportedFeature, "%s: %s", name, detail)
		}
		if block, material := openVPNMaterialDirectives[name]; material {
			if err := rejectOpenVPNFilePath(name, block, directives[name]); err != nil {
				return err
			}
		}
		if name == "auth-user-pass" {
			for _, entry := range directives[name] {
				if len(entry.args) > 0 {
					return openVPNErrorf(ReasonUnsupportedFeature,
						"auth-user-pass: file path is not supported; provide credentials in the Prism bundle")
				}
			}
		}
	}
	return nil
}

// rejectOpenVPNFilePath refuses ca/cert/key/tls-auth/tls-crypt/secret that name a
// file instead of an inline block: Prism stores node documents, not files.
func rejectOpenVPNFilePath(directive string, block string, entries []openVPNDirective) error {
	for _, entry := range entries {
		if entry.hasInline {
			continue
		}
		argument := strings.TrimSpace(directiveFirstArg(entry))
		if argument == "" || strings.EqualFold(argument, "[inline]") {
			continue
		}
		return openVPNErrorf(ReasonUnsupportedFeature,
			"%s: file path is not supported; inline the material as <%s>...</%s>",
			directive, block, block)
	}
	return nil
}

func (b *openVPNBuilder) applyDev(directives map[string][]openVPNDirective) error {
	entry, ok := firstDirective(directives, "dev")
	if !ok {
		// Without dev the profile is still a tun client: the endpoint always
		// builds a tun device.
		return nil
	}
	device := strings.ToLower(directiveFirstArg(entry))
	switch {
	case device == "":
		return nil
	case strings.HasPrefix(device, "tun"):
		return nil
	default:
		return openVPNErrorf(ReasonUnsupportedFeature,
			"dev %s: only tun devices are supported", device)
	}
}

// applyMode decides between tls and static_key and fills the crypto options.
func (b *openVPNBuilder) applyMode(directives map[string][]openVPNDirective) error {
	if secret, hasSecret := firstDirective(directives, "secret"); hasSecret {
		return b.applyStaticKeyMode(directives, secret)
	}

	b.tls = map[string]any{}
	b.options["tls"] = b.tls

	if ca, ok := firstDirective(directives, "ca"); ok && ca.hasInline {
		b.tls["certificate"] = []string{ca.inline}
	}
	if cert, ok := firstDirective(directives, "cert"); ok && cert.hasInline {
		b.tls["client_certificate"] = []string{cert.inline}
	}
	if key, ok := firstDirective(directives, "key"); ok && key.hasInline {
		b.tls["client_key"] = []string{key.inline}
	}
	// Fact F8: the endpoint refuses to build without a CA or a fingerprint, so
	// the profile is validated here instead of failing later in the runtime.
	fingerprints := openVPNFingerprints(directives)
	if len(fingerprints) > 0 {
		b.tls["peer_fingerprint"] = fingerprints
	}
	if _, hasCA := b.tls["certificate"]; !hasCA && len(fingerprints) == 0 {
		return openVPNErrorf(ReasonInvalid,
			"tls mode requires an inline <ca> block or a peer-fingerprint")
	}
	if err := b.applyControlWrap(directives); err != nil {
		return err
	}
	if err := b.applyTLSNames(directives); err != nil {
		return err
	}
	return b.applyTLSVersions(directives)
}

func (b *openVPNBuilder) applyStaticKeyMode(directives map[string][]openVPNDirective, secret openVPNDirective) error {
	if !secret.hasInline {
		return openVPNErrorf(ReasonUnsupportedFeature,
			"secret: file path is not supported; inline the key as <secret>...</secret>")
	}
	b.mode = "static_key"
	b.options["mode"] = "static_key"
	b.options["static_key"] = []string{secret.inline}
	if _, hasTLS := firstDirective(directives, "ca"); hasTLS {
		return openVPNErrorf(ReasonInvalid, "static_key mode does not use tls options")
	}
	if direction, ok := firstDirective(directives, "key-direction"); ok {
		value := directiveFirstArg(direction)
		switch value {
		case "0":
			b.options["key_direction"] = "server"
		case "1":
			b.options["key_direction"] = "client"
		case "":
		default:
			return openVPNErrorf(ReasonInvalid, "key-direction %s", value)
		}
	}
	ifconfig, ok := firstDirective(directives, "ifconfig")
	if !ok || len(ifconfig.args) < 2 {
		return openVPNErrorf(ReasonInvalid, "static_key mode requires ifconfig <local> <peer>")
	}
	local := strings.TrimSpace(ifconfig.args[0])
	peer := strings.TrimSpace(ifconfig.args[1])
	prefix, ok := NormalizeWireGuardPrefix(local)
	if !ok {
		return openVPNErrorf(ReasonInvalid, "ifconfig local address %q", local)
	}
	b.options["address"] = []string{prefix}
	b.options["peer_address"] = peer
	return nil
}

func openVPNFingerprints(directives map[string][]openVPNDirective) []string {
	var fingerprints []string
	for _, entry := range directives["peer-fingerprint"] {
		for _, argument := range entry.args {
			for _, part := range strings.FieldsFunc(argument, func(r rune) bool { return r == ':' || r == ' ' }) {
				normalized := strings.ToLower(strings.TrimSpace(part))
				if normalized == "" {
					continue
				}
				fingerprints = append(fingerprints, normalized)
			}
		}
	}
	return fingerprints
}

func (b *openVPNBuilder) applyControlWrap(directives map[string][]openVPNDirective) error {
	wrap := map[string]any{}
	if entry, ok := firstDirective(directives, "tls-auth"); ok {
		if !entry.hasInline {
			return openVPNErrorf(ReasonUnsupportedFeature,
				"tls-auth: file path is not supported; inline the key as <tls-auth>...</tls-auth>")
		}
		wrap["type"] = "tls_auth"
		wrap["key"] = []string{entry.inline}
	}
	for _, name := range []string{"tls-crypt", "tls-crypt-v2"} {
		entry, ok := firstDirective(directives, name)
		if !ok {
			continue
		}
		if !entry.hasInline {
			return openVPNErrorf(ReasonUnsupportedFeature,
				"%s: file path is not supported; inline the key as <%s>...</%s>", name, name, name)
		}
		if _, configured := wrap["type"]; configured {
			return openVPNErrorf(ReasonInvalid, "both tls-auth and %s are configured", name)
		}
		wrap["type"] = strings.ReplaceAll(name, "-", "_")
		wrap["key"] = []string{entry.inline}
	}
	direction, hasDirection := firstDirective(directives, "key-direction")
	if len(wrap) == 0 {
		if hasDirection {
			return openVPNErrorf(ReasonInvalid,
				"key-direction requires tls-auth; tls mode reads tls.control_wrap.direction")
		}
		return nil
	}
	if wrap["type"] == "tls_auth" {
		if hasDirection {
			switch directiveFirstArg(direction) {
			case "0":
				wrap["direction"] = "server"
			case "1":
				wrap["direction"] = "client"
			case "":
			default:
				return openVPNErrorf(ReasonInvalid, "key-direction %s", directiveFirstArg(direction))
			}
		}
	} else if hasDirection {
		return openVPNErrorf(ReasonInvalid, "key-direction is only supported with tls-auth")
	}
	b.tls["control_wrap"] = wrap
	return nil
}

func (b *openVPNBuilder) applyTLSNames(directives map[string][]openVPNDirective) error {
	if entry, ok := firstDirective(directives, "verify-x509-name"); ok {
		if len(entry.args) == 0 {
			return openVPNErrorf(ReasonInvalid, "verify-x509-name without a name")
		}
		name := strings.TrimSpace(entry.args[0])
		nameType := "subject"
		if len(entry.args) > 1 {
			switch strings.ToLower(strings.TrimSpace(entry.args[1])) {
			case "subject":
				nameType = "subject"
			case "name":
				nameType = "name"
			case "name-prefix":
				nameType = "name-prefix"
			default:
				return openVPNErrorf(ReasonInvalid, "verify-x509-name type %s", entry.args[1])
			}
		}
		if name != "" {
			b.tls["server_name"] = name
			b.tls["server_name_type"] = nameType
		}
	}
	if _, ok := firstDirective(directives, "remote-cert-tls"); ok {
		b.tls["remote_certificate_tls"] = "server"
	}
	return nil
}

func (b *openVPNBuilder) applyTLSVersions(directives map[string][]openVPNDirective) error {
	// .ovpn directive → sing-box option.OpenVPNOutboundTLSOptions field.
	versionFields := []struct {
		directive string
		field     string
	}{
		{directive: "tls-version-min", field: "version_min"},
		{directive: "tls-version-max", field: "version_max"},
	}
	for _, mapping := range versionFields {
		entry, ok := firstDirective(directives, mapping.directive)
		if !ok {
			continue
		}
		version := openVPNTLSVersion(directiveFirstArg(entry))
		if version == "" {
			return openVPNErrorf(ReasonUnsupportedFeature, "%s %s", mapping.directive, directiveFirstArg(entry))
		}
		b.tls[mapping.field] = version
	}
	if entry, ok := firstDirective(directives, "tls-cipher"); ok {
		cipher := directiveFirstArg(entry)
		if cipher == "" {
			return openVPNErrorf(ReasonInvalid, "tls-cipher without a value")
		}
		b.tls["cipher"] = cipher
	}
	return nil
}

// openVPNTLSVersion maps a tls-version-min/max argument to a sing-box version.
func openVPNTLSVersion(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "1.0", "1.1", "1.2", "1.3":
		return fields[0]
	default:
		return ""
	}
}

func (b *openVPNBuilder) applyRemotesAndTuning(
	directives map[string][]openVPNDirective,
	username string,
	password string,
) error {
	b.collectRemotes(directives)
	if len(b.servers) == 0 {
		return openVPNErrorf(ReasonInvalid, "remote is required")
	}
	b.options["servers"] = b.servers
	if b.proto != "" {
		b.options["network"] = b.proto
	}
	if len(directives["remote-random"]) > 0 {
		b.options["remote_random"] = true
	}
	if err := b.applyCrypto(directives); err != nil {
		return err
	}
	if err := b.applyCompression(directives); err != nil {
		return err
	}
	if err := b.applySizesAndTiming(directives); err != nil {
		return err
	}
	b.applyPullOptions(directives)
	if b.mode == "" {
		if err := b.applyCredentials(directives, username, password); err != nil {
			return err
		}
	}
	return nil
}

func (b *openVPNBuilder) collectRemotes(directives map[string][]openVPNDirective) {
	if entry, ok := firstDirective(directives, "proto"); ok {
		if network, known := openVPNNetwork(directiveFirstArg(entry)); known {
			b.proto = network
		}
	}
	if entry, ok := firstDirective(directives, "port"); ok {
		if port, err := strconv.ParseUint(directiveFirstArg(entry), 10, 16); err == nil && port > 0 {
			b.defaultPort = uint16(port)
		}
	}
	for _, entry := range directives["remote"] {
		if len(entry.args) == 0 {
			continue
		}
		host := strings.TrimSpace(entry.args[0])
		if host == "" {
			continue
		}
		port := b.defaultPort
		if port == 0 {
			port = defaultOpenVPNPort
		}
		network := ""
		if len(entry.args) > 1 {
			if parsed, err := strconv.ParseUint(entry.args[1], 10, 16); err == nil && parsed > 0 {
				port = uint16(parsed)
			} else if value, known := openVPNNetwork(entry.args[1]); known {
				network = value
			}
		}
		if network == "" && len(entry.args) > 2 {
			if value, known := openVPNNetwork(entry.args[2]); known {
				network = value
			}
		}
		server := map[string]any{"server": host, "server_port": port}
		if network != "" {
			server["network"] = network
		}
		b.servers = append(b.servers, server)
		if b.firstRemote == "" {
			b.firstRemote = host
		}
	}
}

// openVPNNetwork maps a proto value to a sing-box openvpn network value.
// tcp-client is OpenVPN's client-side TCP alias for tcp.
func openVPNNetwork(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "udp", "udp4", "udp6", "tcp", "tcp4", "tcp6":
		return normalized, true
	case "tcp-client":
		return "tcp", true
	default:
		return "", false
	}
}

func (b *openVPNBuilder) applyCrypto(directives map[string][]openVPNDirective) error {
	if b.mode == "static_key" {
		if entry, ok := firstDirective(directives, "cipher"); ok {
			if cipher := openVPNCipherName(directiveFirstArg(entry)); cipher != "" {
				b.options["cipher"] = cipher
			}
		}
		return nil
	}
	dataCiphers := openVPNList(directives, "data-ciphers", "ncp-ciphers")
	if len(dataCiphers) > 0 {
		b.options["data_ciphers"] = dataCiphers
	}
	if entry, ok := firstDirective(directives, "data-ciphers-fallback"); ok {
		if cipher := openVPNCipherName(directiveFirstArg(entry)); cipher != "" {
			b.options["data_ciphers_fallback"] = cipher
		}
	}
	// sing-box only accepts `cipher` in static_key mode, so a TLS profile's
	// `cipher` becomes the fallback data cipher; when the profile negotiates
	// explicit data-ciphers, `cipher` is only a hint and is dropped.
	if entry, ok := firstDirective(directives, "cipher"); ok {
		if _, hasFallback := b.options["data_ciphers_fallback"]; !hasFallback {
			if cipher := openVPNCipherName(directiveFirstArg(entry)); cipher != "" {
				if len(dataCiphers) > 0 {
					b.note("cipher %s (data-ciphers takes precedence)", cipher)
				} else {
					b.options["data_ciphers_fallback"] = cipher
				}
			}
		}
	}
	if entry, ok := firstDirective(directives, "auth"); ok {
		raw := directiveFirstArg(entry)
		auth := openVPNAuthName(raw)
		if auth == "" && strings.TrimSpace(raw) != "" {
			return openVPNErrorf(ReasonUnsupportedFeature,
				"auth %s (sing-box needs a canonical OpenVPN digest name)", raw)
		}
		if auth != "" {
			b.options["auth"] = auth
		}
	}
	return nil
}

// canonicalOpenVPNAuth lists the data-channel digests sing-box accepts.
var canonicalOpenVPNAuth = map[string]bool{
	"SHA1": true, "SHA224": true, "SHA256": true, "SHA384": true, "SHA512": true,
	"RIPEMD160": true, "MD5": true, "NONE": true,
}

// openVPNAuthName normalizes an `auth` value; empty means "not canonical".
func openVPNAuthName(raw string) string {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if canonicalOpenVPNAuth[name] {
		return name
	}
	return ""
}

// openVPNList flattens colon separated cipher directives.
func openVPNList(directives map[string][]openVPNDirective, names ...string) []string {
	var result []string
	for _, name := range names {
		for _, entry := range directives[name] {
			for _, argument := range entry.args {
				for _, part := range strings.Split(argument, ":") {
					cipher := openVPNCipherName(part)
					if cipher == "" {
						continue
					}
					result = append(result, cipher)
				}
			}
		}
	}
	return result
}

// openVPNCipherName uppercases a data-channel cipher; sing-box requires the
// canonical OpenVPN spelling.
func openVPNCipherName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return strings.ToUpper(trimmed)
}

func (b *openVPNBuilder) applyCompression(directives map[string][]openVPNDirective) error {
	if entry, ok := firstDirective(directives, "comp-lzo"); ok {
		value := "yes"
		if len(entry.args) > 0 {
			value = strings.ToLower(strings.TrimSpace(entry.args[0]))
		}
		switch value {
		case "yes", "adaptive", "no":
			b.options["compression_lzo"] = value
		default:
			return openVPNErrorf(ReasonUnsupportedFeature, "comp-lzo %s", value)
		}
	}
	if entry, ok := firstDirective(directives, "compress"); ok {
		value := "stub-v2"
		if len(entry.args) > 0 {
			value = strings.ToLower(strings.TrimSpace(entry.args[0]))
		}
		switch value {
		case "lz4", "lz4-v2", "stub", "stub-v2", "no":
			b.options["compression"] = value
		default:
			return openVPNErrorf(ReasonUnsupportedFeature, "compress %s", value)
		}
	}
	if entry, ok := firstDirective(directives, "allow-compression"); ok {
		value := strings.ToLower(strings.TrimSpace(directiveFirstArg(entry)))
		switch value {
		case "no", "yes", "asym":
			// sing-box refuses allow_compression next to a statically enabled
			// compression, so the explicit `compress`/`comp-lzo` value wins.
			if _, hasLZO := b.options["compression_lzo"]; hasLZO {
				b.note("allow-compression %s (comp-lzo takes precedence)", value)
			} else if _, hasCompression := b.options["compression"]; hasCompression && value == "no" {
				b.note("allow-compression no (compress takes precedence)")
			} else {
				b.options["allow_compression"] = value
			}
		case "":
		default:
			return openVPNErrorf(ReasonUnsupportedFeature, "allow-compression %s", value)
		}
	}
	return nil
}

func (b *openVPNBuilder) applySizesAndTiming(directives map[string][]openVPNDirective) error {
	if value, ok, err := openVPNUintDirective(directives, "tun-mtu"); err != nil {
		return err
	} else if ok {
		b.options["mtu"] = value
	}
	if value, ok, err := openVPNUintDirective(directives, "mssfix"); err != nil {
		return err
	} else if ok {
		b.options["mss_fix"] = value
	}
	if value, ok, err := openVPNUintDirective(directives, "fragment"); err != nil {
		return err
	} else if ok {
		b.options["fragment"] = value
	}

	pingInterval := ""
	pingRestart := ""
	if entry, ok := firstDirective(directives, "keepalive"); ok {
		if len(entry.args) < 2 {
			return openVPNErrorf(ReasonInvalid, "keepalive requires two values")
		}
		pingInterval = entry.args[0]
		pingRestart = entry.args[1]
	}
	if entry, ok := firstDirective(directives, "ping"); ok {
		if value := directiveFirstArg(entry); value != "" {
			pingInterval = value
		}
	}
	if entry, ok := firstDirective(directives, "ping-restart"); ok {
		if value := directiveFirstArg(entry); value != "" {
			pingRestart = value
		}
	}
	if pingInterval != "" {
		seconds, err := strconv.ParseUint(pingInterval, 10, 32)
		if err != nil {
			return openVPNErrorf(ReasonInvalid, "ping interval %s", pingInterval)
		}
		b.options["ping_interval"] = fmt.Sprintf("%ds", seconds)
	}
	if pingRestart != "" {
		seconds, err := strconv.ParseUint(pingRestart, 10, 32)
		if err != nil {
			return openVPNErrorf(ReasonInvalid, "ping-restart interval %s", pingRestart)
		}
		b.options["ping_restart"] = fmt.Sprintf("%ds", seconds)
	}
	if entry, ok := firstDirective(directives, "reneg-sec"); ok {
		seconds, err := strconv.ParseUint(directiveFirstArg(entry), 10, 32)
		if err != nil {
			return openVPNErrorf(ReasonInvalid, "reneg-sec %s", directiveFirstArg(entry))
		}
		if seconds == 0 {
			b.options["renegotiate_disabled"] = true
		} else {
			b.options["renegotiate_interval"] = fmt.Sprintf("%ds", seconds)
		}
	}
	if entry, ok := firstDirective(directives, "replay-window"); ok {
		if len(entry.args) > 0 {
			size, err := strconv.ParseUint(entry.args[0], 10, 32)
			if err != nil {
				return openVPNErrorf(ReasonInvalid, "replay-window %s", entry.args[0])
			}
			b.options["replay_window"] = size
		}
	}
	return nil
}

func openVPNUintDirective(directives map[string][]openVPNDirective, name string) (uint64, bool, error) {
	entry, ok := firstDirective(directives, name)
	if !ok {
		return 0, false, nil
	}
	value := directiveFirstArg(entry)
	if value == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, false, openVPNErrorf(ReasonInvalid, "%s %s", name, value)
	}
	return parsed, true, nil
}

func (b *openVPNBuilder) applyPullOptions(directives map[string][]openVPNDirective) {
	if len(directives["route-nopull"]) > 0 {
		b.options["route_no_pull"] = true
	}
	var filters []map[string]any
	for _, entry := range directives["pull-filter"] {
		if len(entry.args) < 2 {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(entry.args[0]))
		switch action {
		case "accept", "ignore", "reject":
		default:
			continue
		}
		text := strings.Join(entry.args[1:], " ")
		if strings.TrimSpace(text) == "" {
			continue
		}
		filters = append(filters, map[string]any{"action": action, "text": text})
	}
	if len(filters) > 0 {
		b.options["pull_filters"] = filters
	}
}

// applyCredentials resolves the username/password pair. Inline
// <auth-user-pass> values win over the Prism bundle values.
func (b *openVPNBuilder) applyCredentials(
	directives map[string][]openVPNDirective,
	username string,
	password string,
) error {
	requested := false
	if entry, ok := firstDirective(directives, "auth-user-pass"); ok {
		requested = true
		if entry.hasInline {
			var values []string
			for _, line := range strings.Split(entry.inline, "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" {
					values = append(values, trimmed)
				}
			}
			if len(values) == 0 {
				return openVPNErrorf(ReasonInvalid, "credentials required")
			}
			if len(values) > 0 {
				b.options["username"] = values[0]
			}
			if len(values) > 1 {
				b.options["password"] = values[1]
			}
		}
	}
	user := strings.TrimSpace(username)
	pass := strings.TrimSpace(password)
	if stored, ok := b.options["username"].(string); ok && stored != "" {
		user = stored
	}
	if stored, ok := b.options["password"].(string); ok && stored != "" {
		pass = stored
	}
	if !requested {
		if user == "" && pass == "" {
			return nil
		}
		b.options["username"] = user
		b.options["password"] = pass
		return nil
	}
	if user == "" && pass == "" {
		return openVPNErrorf(ReasonInvalid, "credentials required")
	}
	b.options["username"] = user
	b.options["password"] = pass
	return nil
}
