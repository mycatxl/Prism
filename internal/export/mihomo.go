package export

import (
	"bytes"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"prism/internal/node"
)

// exportMihomo renders a mihomo (Clash Meta) configuration (WP11 §2.2).
//
// mihomo is not a Prism runtime kernel (decision D-1); this is pure
// serialisation and adds no dependency.
func exportMihomo(items []preparedItem, opt Options, report *Report) ([]byte, int) {
	selectorTag := opt.selectorTag()
	autoTag := opt.autoTag()

	var (
		proxies = make([]any, 0, len(items))
		names   = make([]string, 0, len(items))
	)
	for _, item := range items {
		proxy, reason, err := clashProxy(item)
		if err != nil {
			skip[struct{}](report, item.Name, reason)
			continue
		}
		proxies = append(proxies, withName(proxy, item.Name))
		names = append(names, item.Name)
	}

	selectorTag = uniqueTag(selectorTag, names)
	autoTag = uniqueTag(autoTag, names)

	autoMembers := make([]any, 0, len(names))
	for _, name := range names {
		autoMembers = append(autoMembers, name)
	}
	selectorMembers := append([]any{autoTag}, autoMembers...)

	root := []yamlEntry{
		{key: "proxies", value: proxies},
		{key: "proxy-groups", value: []any{
			map[string]any{
				"name":    selectorTag,
				"type":    "select",
				"proxies": selectorMembers,
			},
			map[string]any{
				"name":     autoTag,
				"type":     "url-test",
				"url":      "https://www.gstatic.com/generate_204",
				"interval": 300,
				"proxies":  autoMembers,
			},
		}},
		{key: "rules", value: []any{"MATCH," + selectorTag}},
	}

	body, err := marshalYAMLMap(root)
	if err != nil {
		return nil, 0
	}
	return body, len(names)
}

// clashProxy converts one node document into a mihomo proxy map. The returned
// reason is the report reason to use when err is non-nil.
func clashProxy(item preparedItem) (map[string]any, string, error) {
	// A mihomo node is already a Clash proxy map: output it verbatim.
	if item.Doc.Engine == node.EngineMihomo {
		object, err := objectMap(item.Doc.Proxy)
		if err != nil {
			return nil, NotRepresentable(FormatMihomo), fmt.Errorf("parse mihomo proxy: %w", err)
		}
		return object, "", nil
	}
	if item.Doc.Chain {
		proxy, ok := clashChainProxy(item)
		if !ok {
			return nil, ReasonNotRepresentableMihomoChain, fmt.Errorf("chain is not representable")
		}
		return proxy, "", nil
	}
	object, err := objectMap(item.Doc.Main)
	if err != nil {
		return nil, NotRepresentable(FormatMihomo), fmt.Errorf("parse sing-box object: %w", err)
	}
	proxy, ok := clashProxyFromSingbox(object)
	if !ok {
		return nil, NotRepresentable(FormatMihomo), fmt.Errorf("type %q is not representable", item.Doc.Type)
	}
	return proxy, "", nil
}

// yamlEntry is one ordered top-level YAML entry.
type yamlEntry struct {
	key   string
	value any
}

// marshalYAMLMap renders an ordered top-level mapping; nested map keys are
// sorted so the output is deterministic.
func marshalYAMLMap(entries []yamlEntry) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, entry := range entries {
		value, err := yamlValue(entry.value)
		if err != nil {
			return nil, err
		}
		if value == nil {
			value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.key},
			value,
		)
	}

	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return buffer.Bytes(), nil
}

// yamlValue converts a plain Go value into a deterministic yaml.Node.
func yamlValue(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: typed}, nil
	case bool:
		text := "false"
		if typed {
			text = "true"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", typed)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", typed)}, nil
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: fmt.Sprintf("%v", typed)}, nil
	case []string:
		sequence := &yaml.Node{Kind: yaml.SequenceNode}
		for _, item := range typed {
			node, err := yamlValue(item)
			if err != nil {
				return nil, err
			}
			sequence.Content = append(sequence.Content, node)
		}
		return sequence, nil
	case []int:
		sequence := &yaml.Node{Kind: yaml.SequenceNode}
		for _, item := range typed {
			node, err := yamlValue(item)
			if err != nil {
				return nil, err
			}
			sequence.Content = append(sequence.Content, node)
		}
		return sequence, nil
	case []any:
		sequence := &yaml.Node{Kind: yaml.SequenceNode}
		for _, item := range typed {
			node, err := yamlValue(item)
			if err != nil {
				return nil, err
			}
			sequence.Content = append(sequence.Content, node)
		}
		return sequence, nil
	case map[string]any:
		mapping := &yaml.Node{Kind: yaml.MappingNode}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			node, err := yamlValue(typed[key])
			if err != nil {
				return nil, err
			}
			mapping.Content = append(mapping.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				node,
			)
		}
		return mapping, nil
	default:
		return nil, fmt.Errorf("unsupported yaml value type %T", value)
	}
}
