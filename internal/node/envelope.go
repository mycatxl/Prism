// Node document formats (WP06 §1).
//
// A node document has two shapes:
//
//   - form A: a plain sing-box outbound JSON object, byte-compatible with the
//     upstream Resin representation (hash unchanged);
//   - form B: an envelope object marked with "prism_node":1 that carries an
//     endpoint, a detour chain, or a mihomo proxy.
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

const (
	// EnvelopeMarker is the top-level key that turns an object into form B.
	EnvelopeMarker = "prism_node"
	// EnvelopeVersion is the only supported envelope version.
	EnvelopeVersion = 1

	// EngineSingbox and EngineMihomo are the supported runtime engine names.
	EngineSingbox = "singbox"
	EngineMihomo  = "mihomo"

	// Kinds used by the envelope "kind" field.
	KindEndpoint = "endpoint"
	KindChain    = "chain"
	KindProxy    = "proxy"

	// DepTagPrefix is the fixed tag prefix of chain dependencies.
	DepTagPrefix = "d"
)

// endpointTypeSet lists the sing-box endpoint types Prism classifies as
// endpoints. openvpn-server is not a node candidate.
var endpointTypeSet = map[string]bool{
	"wireguard":      true,
	"openvpn-client": true,
	"openconnect":    true,
	"tailscale":      true,
}

// EndpointTypes returns the endpoint types this build can create (WP06 §10).
// tailscale is deliberately absent: the binary has no with_tailscale tag, so
// such nodes are reported as ENGINE_NOT_BUILT.
func EndpointTypes() []string {
	return []string{"openconnect", "openvpn-client", "wireguard"}
}

// IsEndpointType reports whether typeName names a sing-box endpoint type.
func IsEndpointType(typeName string) bool {
	return endpointTypeSet[typeName]
}

// DocKind classifies a node document.
type DocKind int

const (
	// DocOutbound is a single sing-box outbound.
	DocOutbound DocKind = iota
	// DocEndpoint is a single sing-box endpoint.
	DocEndpoint
	// DocChain is a detour chain (deps + main).
	DocChain
	// DocProxy is a mihomo proxy envelope (WP07).
	DocProxy
)

// String returns the envelope kind name for a DocKind.
func (k DocKind) String() string {
	switch k {
	case DocOutbound:
		return "outbound"
	case DocEndpoint:
		return KindEndpoint
	case DocChain:
		return KindChain
	case DocProxy:
		return KindProxy
	}
	return "unknown"
}

// Envelope is the decoded form B document.
type Envelope struct {
	PrismNode int               `json:"prism_node"`
	Engine    string            `json:"engine"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name,omitempty"`
	Main      json.RawMessage   `json:"main,omitempty"`
	Deps      []json.RawMessage `json:"deps,omitempty"`
	Proxy     json.RawMessage   `json:"proxy,omitempty"`
}

// NodeDoc is a normalized node document ready for the runtime to build.
type NodeDoc struct {
	Kind   DocKind
	Engine string
	Name   string
	Type   string
	Chain  bool
	Main   json.RawMessage
	Deps   []json.RawMessage
	Hash   Hash
}

// SubProtocol returns the protocol name of the document's main object.
func (d NodeDoc) SubProtocol() string {
	return d.Type
}

var depTagPattern = regexp.MustCompile(`^d[0-9]+$`)

// IsEnvelope reports whether raw is a form B envelope object.
func IsEnvelope(raw []byte) bool {
	var header struct {
		Marker json.RawMessage `json:"prism_node"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return false
	}
	return len(header.Marker) != 0
}

// ParseEnvelope decodes raw as a form B envelope. ok is false when raw is not
// an envelope at all; err is non-nil when it is a malformed envelope.
func ParseEnvelope(raw []byte) (Envelope, bool, error) {
	var envelope Envelope
	if !IsEnvelope(raw) {
		return Envelope{}, false, nil
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Envelope{}, true, fmt.Errorf("invalid node envelope: %w", err)
	}
	if envelope.PrismNode != EnvelopeVersion {
		return Envelope{}, true, fmt.Errorf("INVALID:unsupported prism_node version %d", envelope.PrismNode)
	}
	switch envelope.Engine {
	case EngineSingbox, EngineMihomo:
	default:
		return Envelope{}, true, fmt.Errorf("INVALID:unknown engine %q", envelope.Engine)
	}
	return envelope, true, nil
}

type objectHeader struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// objectKind classifies a sing-box config object by its type name.
func objectKind(typeName string) DocKind {
	if endpointTypeSet[typeName] {
		return DocEndpoint
	}
	return DocOutbound
}

// ParseNodeDoc decodes any node document (form A or form B).
func ParseNodeDoc(raw []byte) (NodeDoc, error) {
	doc := NodeDoc{Kind: DocOutbound, Engine: EngineSingbox, Hash: HashFromRawOptions(raw)}
	trimmed := raw
	if len(trimmed) == 0 {
		return NodeDoc{}, errors.New("empty node document")
	}

	// A node document can come from untrusted subscription content. Refuse a
	// document that names a local file instead of carrying material before
	// anything reads it (see untrusted_refs.go).
	if err := RejectLocalFileRefs(trimmed); err != nil {
		return NodeDoc{}, err
	}

	envelope, isEnvelope, err := ParseEnvelope(trimmed)
	if err != nil {
		return NodeDoc{}, err
	}
	if !isEnvelope {
		var header objectHeader
		if err := json.Unmarshal(trimmed, &header); err != nil {
			return NodeDoc{}, fmt.Errorf("parse node document: %w", err)
		}
		if header.Type == "" {
			return NodeDoc{}, errors.New("INVALID:missing outbound type")
		}
		// §1.3: a form A wireguard object is a legacy WireGuard outbound and is
		// converted to an endpoint at build time; the hash stays untouched.
		if header.Type == "wireguard" {
			converted, err := WireGuardEndpointFromLegacyOutbound(trimmed)
			if err != nil {
				return NodeDoc{}, err
			}
			main, err := converted.MainObject("")
			if err != nil {
				return NodeDoc{}, err
			}
			doc.Kind = DocEndpoint
			doc.Type = header.Type
			doc.Main = main
			return doc, nil
		}
		doc.Kind = objectKind(header.Type)
		doc.Type = header.Type
		doc.Main = json.RawMessage(trimmed)
		return doc, nil
	}

	doc.Engine = envelope.Engine
	doc.Name = envelope.Name
	if len(envelope.Main) == 0 && len(envelope.Proxy) == 0 {
		return NodeDoc{}, errors.New("INVALID:envelope has no main or proxy object")
	}

	switch envelope.Engine {
	case EngineMihomo:
		if envelope.Kind != KindProxy {
			return NodeDoc{}, fmt.Errorf("INVALID:mihomo nodes must use kind %q", KindProxy)
		}
		doc.Kind = DocProxy
		doc.Main = envelope.Proxy
		var proxyHeader struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(envelope.Proxy, &proxyHeader); err != nil {
			return NodeDoc{}, fmt.Errorf("parse mihomo proxy: %w", err)
		}
		doc.Type = proxyHeader.Type
		return doc, nil
	case EngineSingbox:
	default:
		return NodeDoc{}, fmt.Errorf("INVALID:unknown engine %q", envelope.Engine)
	}

	switch envelope.Kind {
	case KindEndpoint:
		var header objectHeader
		if err := json.Unmarshal(envelope.Main, &header); err != nil {
			return NodeDoc{}, fmt.Errorf("parse endpoint: %w", err)
		}
		if !IsEndpointType(header.Type) {
			return NodeDoc{}, fmt.Errorf("INVALID:%q is not an endpoint type", header.Type)
		}
		doc.Kind = DocEndpoint
		doc.Type = header.Type
		doc.Main = envelope.Main
	case KindChain:
		if len(envelope.Deps) == 0 {
			return NodeDoc{}, errors.New("INVALID:chain node has no deps")
		}
		if len(envelope.Deps) > MaxChainDeps {
			return NodeDoc{}, fmt.Errorf("INVALID:chain depth %d exceeds %d", len(envelope.Deps), MaxChainDeps)
		}
		var header objectHeader
		if err := json.Unmarshal(envelope.Main, &header); err != nil {
			return NodeDoc{}, fmt.Errorf("parse chain main object: %w", err)
		}
		if header.Type == "" {
			return NodeDoc{}, errors.New("INVALID:chain main object has no type")
		}
		doc.Kind = DocChain
		doc.Chain = true
		doc.Type = header.Type
		doc.Main = envelope.Main
		doc.Deps = envelope.Deps
	case KindProxy:
		return NodeDoc{}, errors.New("INVALID:singbox engine cannot carry kind \"proxy\"")
	default:
		return NodeDoc{}, fmt.Errorf("INVALID:unknown envelope kind %q", envelope.Kind)
	}
	return doc, nil
}

// MaxChainDeps bounds the number of dependencies in a detour chain (§6, depth 3).
const MaxChainDeps = 3

// DepTag returns the fixed dependency tag for index i.
func DepTag(i int) string {
	return DepTagPrefix + strconv.Itoa(i)
}

// RewriteDepDetours returns a deep copy of raw where every "detour" value that
// references a chain dependency tag (d0, d1, ...) is rewritten to base+"/d<k>".
func RewriteDepDetours(raw json.RawMessage, base string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse chain object: %w", err)
	}
	rewriteDepDetoursValue(value, base)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode chain object: %w", err)
	}
	return encoded, nil
}

func rewriteDepDetoursValue(value any, base string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "detour" {
				if text, ok := child.(string); ok && depTagPattern.MatchString(text) {
					typed[key] = base + "/" + text
					continue
				}
			}
			rewriteDepDetoursValue(child, base)
		}
	case []any:
		for _, child := range typed {
			rewriteDepDetoursValue(child, base)
		}
	}
}

// TypeOfObject extracts the "type" field of a sing-box config object.
func TypeOfObject(raw json.RawMessage) (string, error) {
	var header objectHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", fmt.Errorf("parse object type: %w", err)
	}
	if header.Type == "" {
		return "", errors.New("INVALID:object has no type")
	}
	return header.Type, nil
}
