package subscription

import (
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/node"
)

// Detour chains.
//
// A chain node resolves the "outbound detour not found" failure by
// materialising every referenced hop as its own sing-box instance: SingboxRuntime
// creates deps[i] as <node tag>/d<i> and rewrites the detour references.

// maxDetourDepth bounds dependency recursion.
const maxDetourDepth = node.MaxChainDeps

// nonProxyDetourTypes are sing-box types that are usable as a detour target but
// are not proxy nodes of their own.
var nonProxyDetourTypes = map[string]bool{
	"shadowtls": true,
	"selector":  true,
	"urltest":   true,
	"direct":    true,
	"block":     true,
	"dns":       true,
}

// buildChainEnvelope renders main + deps as a form B chain node document.
func buildChainEnvelope(name string, main map[string]any, deps []map[string]any) (json.RawMessage, bool) {
	if len(deps) == 0 || len(deps) > maxDetourDepth {
		return nil, false
	}
	mainRaw, err := json.Marshal(main)
	if err != nil {
		return nil, false
	}
	depRaws := make([]json.RawMessage, 0, len(deps))
	for _, dep := range deps {
		depRaw, err := json.Marshal(dep)
		if err != nil {
			return nil, false
		}
		depRaws = append(depRaws, depRaw)
	}
	envelope := node.Envelope{
		PrismNode: node.EnvelopeVersion,
		Engine:    node.EngineSingbox,
		Kind:      node.KindChain,
		Name:      name,
		Main:      mainRaw,
		Deps:      depRaws,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// objectTag reads the tag of a sing-box config object.
func objectTag(raw json.RawMessage) string {
	var header outboundHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return ""
	}
	return header.Tag
}

// objectType reads the type of a sing-box config object.
func objectType(raw json.RawMessage) string {
	var header outboundHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return ""
	}
	return header.Type
}

// objectDetour reads the top-level detour reference of an object.
func objectDetour(raw json.RawMessage) string {
	var payload struct {
		Detour string `json:"detour"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Detour)
}

// decodeObject unmarshals a config object into a mutable map.
func decodeObject(raw json.RawMessage) (map[string]any, bool) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false
	}
	return object, true
}

// stripDetour removes the top-level detour reference of an object: a
// dangling `detour` is stripped from a standalone object so the object stays
// dialable — sing-box would otherwise refuse every dial with "outbound detour
// not found". ok is false when raw is not a JSON mapping.
func stripDetour(raw json.RawMessage) (json.RawMessage, bool) {
	object, ok := decodeObject(raw)
	if !ok {
		return nil, false
	}
	delete(object, "detour")
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// resolveDetourChainOrdered walks the detour graph and returns the referenced
// objects ordered nearest-first, each renamed to d<i> and linked to the next
// hop. resolved is false when a cycle is found (the node is refused). dangling
// names the reference the walk could not resolve, or "" when the walk stopped
// on its own: the caller keeps the resolvable prefix, so it has to report the
// cut rather than silently drop the tail of the chain.
func resolveDetourChainOrdered(start map[string]any, lookup func(tag string) (map[string]any, bool)) (deps []map[string]any, dangling string, resolved bool) {
	deps = make([]map[string]any, 0, maxDetourDepth)
	seen := make(map[string]bool, maxDetourDepth)
	current, _ := start["detour"].(string)

	for len(deps) < maxDetourDepth {
		tag := strings.TrimSpace(current)
		if tag == "" {
			return deps, "", true
		}
		if seen[tag] {
			// a detour cycle refuses the node.
			return nil, "", false
		}
		seen[tag] = true

		dep, found := lookup(tag)
		if !found {
			// A dangling reference: the node is kept but has no extra hop, so
			// the caller strips the detour - or, when the walk already resolved
			// hops, cuts the chain here - and reports the reference. An
			// empty deps slice means nothing resolved at all; a non-empty one
			// means the chain lost its tail.
			return deps, tag, true
		}
		copied := make(map[string]any, len(dep))
		for key, value := range dep {
			copied[key] = value
		}
		delete(copied, "tag")
		delete(copied, "detour")
		copied["tag"] = node.DepTag(len(deps))
		next, _ := dep["detour"].(string)
		deps = append(deps, copied)
		if strings.TrimSpace(next) == "" {
			return deps, "", true
		}
		// The hop we just added detours to the next one.
		copied["detour"] = node.DepTag(len(deps))
		current = next
	}
	return deps, "", true
}

// clampChainDepth trims a dependency list to the supported depth and clears the
// detour of the last hop.
func clampChainDepth(deps []map[string]any) []map[string]any {
	if len(deps) > maxDetourDepth {
		deps = deps[:maxDetourDepth]
	}
	for i, dep := range deps {
		if i == len(deps)-1 {
			delete(dep, "detour")
		} else {
			dep["detour"] = node.DepTag(i + 1)
		}
	}
	return deps
}

// parseSingboxDocument converts a whole sing-box document (outbounds plus the
// optional top-level endpoints array) into node documents.
//
// An object whose detour resolves inside the same document becomes a chain
// node; a dangling reference is reported and stripped from the object so
// that the node stays dialable. Objects that are recognised but not importable
// are reported.
func parseSingboxDocument(outbounds []json.RawMessage, endpoints []json.RawMessage, report *parseReport) []ParsedNode {
	type documentItem struct {
		raw        json.RawMessage
		fromEndpts bool
	}
	items := make([]documentItem, 0, len(outbounds)+len(endpoints))
	for _, raw := range outbounds {
		items = append(items, documentItem{raw: raw})
	}
	for _, raw := range endpoints {
		items = append(items, documentItem{raw: raw, fromEndpts: true})
	}

	index := make(map[string]map[string]any, len(items))
	referenced := make(map[string]bool, len(items))
	for _, item := range items {
		tag := objectTag(item.raw)
		if tag == "" {
			continue
		}
		if object, ok := decodeObject(item.raw); ok {
			index[tag] = object
		}
		if detour := objectDetour(item.raw); detour != "" {
			referenced[detour] = true
		}
	}
	lookup := func(tag string) (map[string]any, bool) {
		object, ok := index[tag]
		return object, ok
	}

	nodes := make([]ParsedNode, 0, len(items))
	for _, item := range items {
		typeName := objectType(item.raw)
		tag := objectTag(item.raw)
		if item.fromEndpts {
			if parsed, ok := parseSingboxEndpointItem(tag, typeName, item.raw, report); ok {
				nodes = append(nodes, parsed)
			}
			continue
		}
		if !supportedOutboundTypes[typeName] {
			if skipped, ok := skipDeferredOrUnknown(tag, typeName, SourceSingbox); ok {
				report.addSkip(skipped)
			}
			continue
		}
		if referenced[tag] && nonProxyDetourTypes[typeName] {
			// Referenced hops are materialised inside their chain node.
			continue
		}

		object, ok := decodeObject(item.raw)
		if !ok {
			report.addSkip(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid, "object is not a JSON mapping"))
			continue
		}
		if detour := objectDetour(item.raw); detour != "" {
			deps, dangling, resolved := resolveDetourChainOrdered(object, lookup)
			if !resolved {
				// A detour cycle: refuse the node.
				report.addSkip(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid, "detour cycle"))
				continue
			}
			if len(deps) == 0 {
				// the reference does not resolve inside this document, so
				// it is reported instead of being guessed and the detour is
				// stripped: the object stays dialable on its own. len(deps) is
				// only zero here — a blank detour never reaches this branch
				// (objectDetour above), a cycle returned false.
				//
				// It is a note, not a skip: the node is imported.
				if stripped, ok := stripDetour(item.raw); ok {
					report.addNote(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid,
						fmt.Sprintf("dangling detour %q was stripped", sanitizeSampleName(dangling))))
					nodes = append(nodes, ParsedNode{Tag: tag, RawOptions: stripped})
					continue
				}
			}
			if len(deps) > 0 {
				if dangling != "" {
					// The chain resolved some hops and then hit a reference that
					// names nothing. The resolvable prefix is still built (the
					// last hop ends without a detour), and the cut is reported:
					// the alternative is a node whose tail silently disappeared.
					report.addNote(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid,
						fmt.Sprintf("dangling detour %q was cut from the chain", sanitizeSampleName(dangling))))
				}
				main := make(map[string]any, len(object))
				for key, value := range object {
					main[key] = value
				}
				delete(main, "tag")
				main["detour"] = node.DepTag(0)
				if encoded, ok := buildChainEnvelope(tag, main, clampChainDepth(deps)); ok {
					nodes = append(nodes, ParsedNode{Tag: tag, RawOptions: encoded})
					continue
				}
			}
		}

		// Form A is preserved, including a legacy WireGuard outbound that the
		// runtime converts into an endpoint so the hash stays stable.
		nodes = append(nodes, ParsedNode{Tag: tag, RawOptions: json.RawMessage(append([]byte(nil), item.raw...))})
	}
	return nodes
}

// parseSingboxEndpointItem converts one entry of the top-level endpoints array
// ok is false for entry types that are not node candidates.
func parseSingboxEndpointItem(tag string, typeName string, raw json.RawMessage, report *parseReport) (ParsedNode, bool) {
	switch typeName {
	case "wireguard":
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			report.addSkip(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid, "endpoint is not a JSON mapping"))
			return ParsedNode{}, false
		}
		endpoint, err := node.WireGuardEndpointFromOptions(payload)
		if err != nil {
			return wrapEndpointObject(tag, raw), true
		}
		encoded, err := endpoint.EnvelopeObject(tag)
		if err != nil {
			report.addSkip(skipNode(tag, typeName, SourceSingbox, node.ReasonInvalid, err.Error()))
			return ParsedNode{}, false
		}
		return ParsedNode{Tag: wireGuardEndpointTag(tag, endpoint), RawOptions: encoded}, true
	case "openvpn-client", "openconnect":
		return wrapEndpointObject(tag, raw), true
	case "tailscale":
		// ENGINE_NOT_BUILT:tailscale — this binary has no with_tailscale tag.
		if skipped, ok := skipDeferredOrUnknown(tag, "tailscale", SourceSingbox); ok {
			report.addSkip(skipped)
		}
		return ParsedNode{}, false
	case "openvpn-server", "":
		// Not a node candidate: Prism dials out, it never accepts clients.
		return ParsedNode{}, false
	default:
		report.addSkip(skipNode(tag, typeName, SourceSingbox, node.ReasonUnsupportedProtocol, "unknown endpoint type"))
		return ParsedNode{}, false
	}
}

// applyClashDialerChains turns Clash dialer-proxy references that point at
// another proxy of the same document into chain nodes. A reference that
// cannot be resolved is stripped from the standalone node, which stays
// dialable, and reported through the caller's report so the drop is explainable
// rather than silent.
func applyClashDialerChains(nodes []ParsedNode, report *parseReport) []ParsedNode {
	index := make(map[string]map[string]any, len(nodes))
	for _, entry := range nodes {
		object, ok := decodeObject(entry.RawOptions)
		if !ok {
			continue
		}
		if node.IsEnvelope(entry.RawOptions) {
			main, ok := object["main"].(map[string]any)
			if !ok {
				continue
			}
			object = main
		}
		index[entry.Tag] = object
	}
	lookup := func(tag string) (map[string]any, bool) {
		object, ok := index[tag]
		return object, ok
	}

	result := make([]ParsedNode, 0, len(nodes))
	for _, entry := range nodes {
		detour := objectDetour(entry.RawOptions)
		object := index[entry.Tag]
		if detour == "" || object == nil {
			result = append(result, entry)
			continue
		}
		deps, dangling, resolved := resolveDetourChainOrdered(object, lookup)
		if !resolved {
			// A dialer-proxy cycle: refuse the chain, keep the node.
			result = append(result, entry)
			continue
		}
		if len(deps) == 0 {
			// the reference names no proxy of this document. Strip it so
			// the node keeps dialing directly instead of failing every dial
			// with "outbound detour not found", and report it so the operator
			// can see the hop was dropped.
			if stripped, ok := stripDetour(entry.RawOptions); ok {
				entry.RawOptions = stripped
			}
			if report != nil {
				report.addNote(skipNode(entry.Tag, objectType(entry.RawOptions), SourceClash, node.ReasonInvalid,
					fmt.Sprintf("dangling dialer-proxy %q was stripped", sanitizeSampleName(dangling))))
			}
			result = append(result, entry)
			continue
		}
		if dangling != "" && report != nil {
			// The chain resolved some hops and then hit a reference that names
			// nothing: the resolvable prefix is still built, and the cut is
			// reported so the lost tail is visible to the operator.
			report.addNote(skipNode(entry.Tag, objectType(entry.RawOptions), SourceClash, node.ReasonInvalid,
				fmt.Sprintf("dangling dialer-proxy %q was cut from the chain", sanitizeSampleName(dangling))))
		}
		main := make(map[string]any, len(object))
		for key, value := range object {
			main[key] = value
		}
		delete(main, "tag")
		main["detour"] = node.DepTag(0)
		encoded, ok := buildChainEnvelope(entry.Tag, main, clampChainDepth(deps))
		if !ok {
			result = append(result, entry)
			continue
		}
		result = append(result, ParsedNode{Tag: entry.Tag, RawOptions: encoded})
	}
	return result
}

// shadowTLSVersion maps the version a Clash shadow-tls plugin declares onto the
// sing-box shadowtls version. ok is false for a version sing-box cannot
// express, which must be refused instead of being guessed at.
//
// An empty value maps to 3, which is what a plugin without a version means in
// the protocol's own deployments: the shadow-tls line the Clash ecosystem ships
// is v3, and both kernels default to a different number when the field is
// absent (sing-box's shadowtls outbound falls back to 1, mihomo's client to 2),
// so following either default would silently downgrade a working v3 node.
func shadowTLSVersion(raw string) (uint64, bool) {
	switch strings.TrimSpace(raw) {
	case "":
		return 3, true
	case "1":
		return 1, true
	case "2":
		return 2, true
	case "3":
		return 3, true
	default:
		return 0, false
	}
}

// isShadowTLSPlugin reports whether a Clash proxy declares the shadow-tls
// plugin, in either spelling.
func isShadowTLSPlugin(proxy map[string]any) bool {
	plugin := canonicalSSPluginName(getString(proxy, "plugin"))
	return plugin == "shadow-tls" || plugin == "shadowtls"
}

// shadowTLSPluginOptions reads the password, camouflage host and version from
// either the plugin-opts string, the plugin-opts mapping or the flat
// shadow-tls-* keys.
func shadowTLSPluginOptions(proxy map[string]any) (password, host, version string) {
	options := splitSSPluginOptionsMap(getString(proxy, "plugin-opts", "plugin_opts"))
	if pluginOptions, ok := getMap(proxy, "plugin-opts", "plugin_opts"); ok {
		// Clash YAML/JSON normally carries plugin-opts as a mapping.
		for key, value := range pluginOptions {
			if text := strings.TrimSpace(firstNonEmptyValue(value)); text != "" {
				options[strings.ToLower(key)] = text
			}
		}
	}
	password = strings.TrimSpace(firstNonEmpty(options["password"], getString(proxy, "shadow-tls-password")))
	host = strings.TrimSpace(firstNonEmpty(options["host"], options["sni"], getString(proxy, "shadow-tls-sni")))
	version = strings.TrimSpace(firstNonEmpty(options["version"], getString(proxy, "shadow-tls-version")))
	return password, host, version
}

// shadowTLSRefusal explains why a Clash shadow-tls plugin cannot become a chain,
// or "" when the proxy does not use the plugin or can be converted. It is the
// report half of clashShadowTLSChain.
//
// A proxy that carries the plugin is refused rather than imported as a bare
// shadowsocks node: setSSPluginFromClash has already written plugin=shadow-tls
// into that outbound, and sing-box rejects it at dial time with "plugin not
// found: shadow-tls", so the node would import and never connect.
func shadowTLSRefusal(proxy map[string]any) string {
	if !isShadowTLSPlugin(proxy) {
		return ""
	}
	password, host, version := shadowTLSPluginOptions(proxy)
	if _, ok := shadowTLSVersion(version); !ok {
		return fmt.Sprintf("shadow-tls version %q is not supported (sing-box supports 1, 2 and 3)",
			sanitizeSampleName(version))
	}
	if password == "" || host == "" {
		return "shadow-tls plugin needs both a password and a host"
	}
	return ""
}

// clashShadowTLSChain rewrites a Clash shadowsocks proxy that carries the
// shadow-tls plugin into a chain node. ok is false when the proxy does
// not use the plugin or misses the required options; shadowTLSRefusal names
// the latter.
func clashShadowTLSChain(tag string, server string, port uint64, proxy map[string]any, outbound map[string]any) (json.RawMessage, bool) {
	if !isShadowTLSPlugin(proxy) {
		return nil, false
	}
	password, host, version := shadowTLSPluginOptions(proxy)
	//: a version sing-box cannot express (it supports 1, 2 and 3) is
	// refused instead of being guessed at.
	shadowTLS, ok := shadowTLSVersion(version)
	if !ok {
		return nil, false
	}
	// A v2/v3 hop needs a password and every hop needs a camouflage host;
	// without them sing-box builds a hop that can never handshake.
	if password == "" || host == "" {
		return nil, false
	}
	fingerprint := firstNonEmpty(
		getString(proxy, "client-fingerprint", "client_fingerprint", "fingerprint"),
		"chrome",
	)
	dep := map[string]any{
		"type":        "shadowtls",
		"tag":         node.DepTag(0),
		"server":      server,
		"server_port": port,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": host,
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": fingerprint,
			},
		},
	}
	if shadowTLS != 1 {
		//: version 1 does not carry a version or a password field.
		dep["version"] = shadowTLS
		dep["password"] = password
	}
	main := make(map[string]any, len(outbound))
	for key, value := range outbound {
		main[key] = value
	}
	delete(main, "tag")
	delete(main, "plugin")
	delete(main, "plugin_opts")
	main["detour"] = node.DepTag(0)
	return buildChainEnvelope(tag, main, []map[string]any{dep})
}

// splitSSPluginOptionsMap decodes a plugin option string into a key/value map.
func splitSSPluginOptionsMap(raw string) map[string]string {
	options := make(map[string]string)
	for _, part := range splitUnescaped(strings.TrimSpace(raw), ';') {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		options[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return options
}
