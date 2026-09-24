package export

import (
	"encoding/json"
	"errors"
	"fmt"

	"prism/internal/node"
)

// NodeDoc is one parsed node document in the form the exporters need.
//
// Form A (a plain sing-box outbound) keeps Raw == Main. Form B (an envelope,
// WP06 §1) splits into the main object plus its chain deps or the mihomo proxy
// object.
type NodeDoc struct {
	Kind   node.DocKind
	Engine string
	Type   string
	Chain  bool
	Main   json.RawMessage
	Deps   []json.RawMessage
	Proxy  json.RawMessage
}

// ParseNodeDoc decodes a form A or form B node document into a NodeDoc.
func ParseNodeDoc(raw []byte) (NodeDoc, error) {
	d, err := node.ParseNodeDoc(raw)
	if err != nil {
		return NodeDoc{}, err
	}
	doc := NodeDoc{
		Kind:   d.Kind,
		Engine: d.Engine,
		Type:   d.Type,
		Chain:  d.Chain,
		Main:   d.Main,
		Deps:   d.Deps,
	}
	if d.Kind == node.DocProxy {
		doc.Proxy = d.Main
		doc.Main = nil
	}
	if len(doc.Main) == 0 && len(doc.Proxy) == 0 {
		return NodeDoc{}, errors.New("INVALID:empty node document")
	}
	return doc, nil
}

// objectMap decodes a JSON object into a mutable map.
func objectMap(raw json.RawMessage) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("parse node object: %w", err)
	}
	return object, nil
}

// encodeObject marshals a node object.
func encodeObject(object map[string]any) (json.RawMessage, error) {
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode node object: %w", err)
	}
	return json.RawMessage(encoded), nil
}

// withTag returns a copy of the object with the display tag applied.
func withTag(object map[string]any, tag string) map[string]any {
	copied := make(map[string]any, len(object)+1)
	for key, value := range object {
		copied[key] = value
	}
	copied["tag"] = tag
	return copied
}

// withName returns a copy of the object with the display name applied.
func withName(object map[string]any, name string) map[string]any {
	copied := make(map[string]any, len(object)+1)
	for key, value := range object {
		copied[key] = value
	}
	copied["name"] = name
	return copied
}
