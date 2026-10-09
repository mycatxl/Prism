package export

import (
	"encoding/json"
	"strconv"

	"prism/internal/node"
)

// exportSingbox renders a sing-box configuration (WP11 §2.1).
//
// Node names are deduplicated with " #2", " #3" so every tag in the document is
// unique and every selector reference resolves. Chain dependencies are emitted
// with the tag "<name>/d<i>" (deps are output but never added to a group) and
// their detour references are rewritten to match: the subscription parser
// ignores the dep tag and only follows the reference, so the round trip keeps
// the node hash.
func exportSingbox(items []preparedItem, opt Options, report *Report) ([]byte, int) {
	selectorTag := opt.selectorTag()
	autoTag := opt.autoTag()

	var (
		names     = make([]string, 0, len(items))
		outbounds []json.RawMessage
		endpoints []json.RawMessage
	)
	for _, item := range items {
		switch {
		case item.Doc.Engine == node.EngineMihomo:
			skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
			continue
		case item.Doc.Kind == node.DocEndpoint:
			endpoint, err := singboxObject(item, item.Doc.Main)
			if err != nil {
				skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
				continue
			}
			endpoints = append(endpoints, endpoint)
		case item.Doc.Chain:
			main, deps, err := singboxChain(item)
			if err != nil {
				skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
				continue
			}
			outbounds = append(outbounds, main)
			outbounds = append(outbounds, deps...)
		default:
			outbound, err := singboxObject(item, item.Doc.Main)
			if err != nil {
				skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
				continue
			}
			outbounds = append(outbounds, outbound)
		}
		names = append(names, item.Name)
	}

	// Group tags must not collide with a node tag.
	selectorTag = uniqueTag(selectorTag, names)
	autoTag = uniqueTag(autoTag, names)

	body := struct {
		Outbounds []json.RawMessage `json:"outbounds"`
		Endpoints []json.RawMessage `json:"endpoints,omitempty"`
	}{
		Outbounds: []json.RawMessage{
			mustMarshal(map[string]any{
				"type":      "selector",
				"tag":       selectorTag,
				"outbounds": append([]string{autoTag}, names...),
			}),
			mustMarshal(map[string]any{
				"type":      "urltest",
				"tag":       autoTag,
				"outbounds": names,
				"url":       "https://www.gstatic.com/generate_204",
				"interval":  "5m",
			}),
		},
		Endpoints: endpoints,
	}
	body.Outbounds = append(body.Outbounds, outbounds...)

	encoded, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, 0
	}
	return append(encoded, '\n'), len(names)
}

// singboxObject renders one node config object with the node name as tag.
func singboxObject(item preparedItem, raw json.RawMessage) (json.RawMessage, error) {
	object, err := objectMap(raw)
	if err != nil {
		return nil, err
	}
	return encodeObject(withTag(object, item.Name))
}

// singboxChain renders a detour chain plus its dependencies.
func singboxChain(item preparedItem) (json.RawMessage, []json.RawMessage, error) {
	main, err := objectMap(item.Doc.Main)
	if err != nil {
		return nil, nil, err
	}
	main = withTag(main, item.Name)
	if detour, ok := main["detour"].(string); ok {
		main["detour"] = rewriteDepRef(item.Name, detour)
	}

	deps := make([]json.RawMessage, 0, len(item.Doc.Deps))
	for i, raw := range item.Doc.Deps {
		object, err := objectMap(raw)
		if err != nil {
			return nil, nil, err
		}
		object["tag"] = item.Name + "/" + node.DepTag(i)
		if detour, ok := object["detour"].(string); ok {
			object["detour"] = rewriteDepRef(item.Name, detour)
		}
		encoded, err := encodeObject(object)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, encoded)
	}

	encoded, err := encodeObject(main)
	if err != nil {
		return nil, nil, err
	}
	return encoded, deps, nil
}

// rewriteDepRef maps a document-local chain dependency reference ("d0", "d1",
// ...) onto its exported "<name>/d<i>" form. Anything else is left untouched.
func rewriteDepRef(name string, detour string) string {
	if len(detour) < 2 || detour[0] != 'd' {
		return detour
	}
	for i := 1; i < len(detour); i++ {
		if detour[i] < '0' || detour[i] > '9' {
			return detour
		}
	}
	return name + "/" + detour
}

// uniqueTag returns tag, or "tag #k" when it collides with a node name.
func uniqueTag(tag string, names []string) string {
	if !containsString(names, tag) {
		return tag
	}
	for k := 2; k <= maxNameVariants; k++ {
		candidate := tag + " #" + strconv.Itoa(k)
		if !containsString(names, candidate) {
			return candidate
		}
	}
	return tag + " #"
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// mustMarshal marshals a fixed-shape object; a failure is a programming error.
func mustMarshal(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("export: marshal group object: " + err.Error())
	}
	return json.RawMessage(encoded)
}
