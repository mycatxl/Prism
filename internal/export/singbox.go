package export

import (
	"encoding/json"
	"strconv"

	"prism/internal/node"
)

// exportSingbox renders a sing-box configuration.
//
// Every tag in the document is unique, and the exporter - not the node names -
// is what makes that true. All tags of the document are claimed from one taken
// set (claimTag): the tag of every node, the "<name>/d<i>" tag of every chain
// dependency and the selector/auto group tags. Node tags are the rendered names
// (RenderNames), so they are reserved before any dependency tag is claimed: a
// rendered name may contain "/" (names.go) and can therefore repeat the
// dependency tag of another node, and sing-box refuses the *whole*
// configuration on a duplicate outbound/endpoint tag, which used to make one
// colliding name take the entire export down.
//
// A dependency tag that had to be renamed is recorded as canonical -> emitted
// (depTags) and every reference to it is rewritten through that table, so a
// chain keeps pointing at the object sing-box resolves instead of at the node
// that owns the canonical spelling. Deps are output but never added to a group;
// the subscription parser ignores the dep tag and only follows the reference, so
// the round trip keeps the node hash.
func exportSingbox(items []preparedItem, opt Options, report *Report) ([]byte, int) {
	selectorTag := opt.selectorTag()
	autoTag := opt.autoTag()

	// Phase 1: decode every item and reserve its node tag. taken ends up holding
	// every tag the document carries.
	taken := make(map[string]struct{}, 2*len(items)+2)
	planned := make([]singboxNode, 0, len(items))
	for _, item := range items {
		if item.Doc.Engine == node.EngineMihomo {
			skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
			continue
		}
		entry, err := decodeSingboxNode(item)
		if err != nil {
			skip[struct{}](report, item.Name, NotRepresentable(FormatSingbox))
			continue
		}
		taken[item.Name] = struct{}{}
		planned = append(planned, entry)
	}

	// Phase 2: claim the dependency tags, rewrite the references through the
	// canonical -> emitted table and encode the objects.
	depTags := make(map[string]string)
	var (
		names     = make([]string, 0, len(planned))
		outbounds []json.RawMessage
		endpoints []json.RawMessage
	)
	for _, entry := range planned {
		if entry.chain {
			for i, dep := range entry.deps {
				canonical := entry.name + "/" + node.DepTag(i)
				emitted := claimTag(canonical, taken)
				depTags[canonical] = emitted
				dep["tag"] = emitted
			}
		}
		if detour, ok := entry.main["detour"].(string); ok {
			entry.main["detour"] = rewriteDepRef(depTags, entry.name, detour)
		}

		main, err := encodeObject(withTag(entry.main, entry.name))
		if err != nil {
			return nil, 0
		}
		if entry.endpoint {
			endpoints = append(endpoints, main)
		} else {
			outbounds = append(outbounds, main)
		}
		for _, dep := range entry.deps {
			if detour, ok := dep["detour"].(string); ok {
				dep["detour"] = rewriteDepRef(depTags, entry.name, detour)
			}
			encoded, err := encodeObject(dep)
			if err != nil {
				return nil, 0
			}
			outbounds = append(outbounds, encoded)
		}
		names = append(names, entry.name)
	}

	// Group tags must not collide with any other tag of the document.
	selectorTag = claimTag(selectorTag, taken)
	autoTag = claimTag(autoTag, taken)

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

// singboxNode is one item of the document after the representability check:
// the decoded config objects, with the node tag and the dependency tags not yet
// applied.
type singboxNode struct {
	name     string
	endpoint bool
	chain    bool
	main     map[string]any
	deps     []map[string]any
}

// decodeSingboxNode decodes one item into the objects the document will carry.
// An error means the item is not representable as a sing-box object.
func decodeSingboxNode(item preparedItem) (singboxNode, error) {
	main, err := objectMap(item.Doc.Main)
	if err != nil {
		return singboxNode{}, err
	}
	entry := singboxNode{
		name:     item.Name,
		endpoint: item.Doc.Kind == node.DocEndpoint,
		main:     main,
	}
	if !item.Doc.Chain {
		return entry, nil
	}
	entry.chain = true
	entry.deps = make([]map[string]any, 0, len(item.Doc.Deps))
	for _, raw := range item.Doc.Deps {
		dep, err := objectMap(raw)
		if err != nil {
			return singboxNode{}, err
		}
		entry.deps = append(entry.deps, dep)
	}
	return entry, nil
}

// rewriteDepRef maps a document-local chain dependency reference ("d0", "d1",
// ...) of the node called name onto the tag the document actually carries:
// depTags holds the canonical "<name>/d<i>" -> emitted tag mapping of every
// dependency, so a dependency tag that had to be deduplicated is still
// referenced correctly. A reference that names no dependency of this node - the
// table holds no entry for it - is left untouched, because the canonical
// spelling may well be the tag of an unrelated node of the same document.
func rewriteDepRef(depTags map[string]string, name string, detour string) string {
	canonical := canonicalDepTag(name, detour)
	if canonical == "" {
		return detour
	}
	if emitted, ok := depTags[canonical]; ok {
		return emitted
	}
	return detour
}

// canonicalDepTag returns the "<name>/d<i>" tag that a chain dependency
// reference denotes, or "" when detour is not a dependency reference at all.
func canonicalDepTag(name string, detour string) string {
	if len(detour) < 2 || detour[0] != 'd' {
		return ""
	}
	for i := 1; i < len(detour); i++ {
		if detour[i] < '0' || detour[i] > '9' {
			return ""
		}
	}
	return name + "/" + detour
}

// claimTag registers tag in taken and returns it, or the first free "tag #k"
// (k = 2..maxNameVariants) when the tag is already taken. The variant rule is
// the one RenderNames applies to node names, but the candidate set is every tag
// of the document, so a dependency tag can never shadow a node name and a group
// tag can never shadow either.
func claimTag(tag string, taken map[string]struct{}) string {
	if _, exists := taken[tag]; !exists {
		taken[tag] = struct{}{}
		return tag
	}
	for k := 2; k <= maxNameVariants; k++ {
		candidate := tag + " #" + strconv.Itoa(k)
		if _, exists := taken[candidate]; !exists {
			taken[candidate] = struct{}{}
			return candidate
		}
	}
	// The whole " #k" budget is taken. " #" is not a " #k" variant, so it is the
	// last resort of the bounded search; a document that also carries it is
	// pathological, so the search continues by growing the separator until a
	// free tag is found. Every iteration lengthens the candidate, so the loop
	// ends: the document may never carry a duplicate tag, whatever the node
	// names are.
	for candidate := tag + " #"; ; candidate += "#" {
		if _, exists := taken[candidate]; !exists {
			taken[candidate] = struct{}{}
			return candidate
		}
	}
}

// uniqueTag returns tag, or "tag #k" when it collides with a node name. It is
// the mihomo group-tag rule, which deduplicates against the proxy names
// only; the sing-box document claims every tag from one taken set (claimTag).
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
