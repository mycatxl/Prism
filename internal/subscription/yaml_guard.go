package subscription

import (
	"fmt"
	"strings"
)

// YAML resource guard (WP06 §9 hardening).
//
// gopkg.in/yaml.v3 (v3.0.1, see go.mod) bounds *syntax* recursion on its own —
// its scanner refuses more than 10000 nested flow collections and more than
// 10000 indentation levels ("exceeded max depth of 10000"), and its decoder
// refuses excessive aliasing. What it does not bound is the *cost* of decoding
// a single huge mapping: its duplicate-key check compares every new key against
// the keys already decoded, so one mapping with n keys costs O(n²). Measured on
// this tree (a body that satisfies looksLikeClashYAML, decoded exactly like
// parseClashYAMLSubscription does):
//
//	1000 keys   ~0.01 s        50000 keys    4.6 s
//	10000 keys  ~0.15 s        100000 keys  24.0 s
//	20000 keys  ~0.7 s         200000 keys 131.0 s
//
// and the same curve applies to the same body written as one flow mapping
// (a single line), or with the huge mapping built out of alias references: all
// three shapes are one mapping with n keys. A 2.3 MiB body therefore burns two
// minutes of CPU per parse attempt, and MaxSubscriptionBytes (32 MiB) would
// allow hours. The 10000-deep case upstream still allows also costs a 10000
// frame recursion and a 10000 node tree.
//
// MaxYAMLNestingDepth and MaxYAMLMappingKeys are the parser-side bounds that
// make that refusal cheap, explicit and reportable: the scan below walks the
// body once, stops at the first breach, and the parse then fails with a
// SkippedNode reason instead of a silent CPU burn or a fatal stack overflow.
// Both sit behind MaxSubscriptionBytes, which stays the outer gate.
const (
	// MaxYAMLNestingDepth caps the nesting depth of one YAML body.
	//
	// The deepest real shape needs ~10 levels (root mapping → proxies sequence
	// → node mapping → `ws-opts`/`grpc-opts`/`tls`/`plugin-opts` mapping →
	// `headers`/`reality-opts` mapping → scalar). 100 keeps an order of
	// magnitude of headroom, so a real Clash/sing-box document cannot be
	// refused, while a nesting bomb needs order 10^5 levels to be dangerous.
	//
	// 10 was rejected although it "looks like enough": a provider wrapper plus
	// nested `dial`/`detour` blocks plus this scan's deliberately conservative
	// counting (see scanYAMLLimits) can add up, and refusing a real
	// subscription is worse than leaving headroom.
	MaxYAMLNestingDepth = 100

	// MaxYAMLMappingKeys caps the number of keys of any single mapping.
	//
	// 20000 bounds the decoder's O(n²) duplicate-key work at well under a
	// second (see the table above) while staying far above real content: a
	// Clash node mapping carries 5-30 keys, and the largest legitimate inline
	// mappings of a client configuration (`hosts:`, `nameserver-policy`) carry
	// hundreds. A generated "one mapping with a million keys" body is not a
	// configuration, it is a slowdown primitive.
	MaxYAMLMappingKeys = 20000
)

// yamlScanLimits are the bounds one pre-decode scan enforces.
type yamlScanLimits struct {
	MaxDepth int
	MaxKeys  int
}

// yamlScanResult is the outcome of the pre-decode scan. Depth and MappingKeys
// are capped at their limit plus one, so a breach is always visible as a number
// just above the limit.
type yamlScanResult struct {
	Depth       int
	MappingKeys int
	// DepthBreach reports that Depth exceeded limits.MaxDepth.
	DepthBreach bool
	// MappingKeyBreach reports that MappingKeys exceeded limits.MaxKeys.
	MappingKeyBreach bool
}

// scanYAMLLimits walks a YAML body once and reports its nesting depth and the
// size of its largest mapping. It allocates nothing per level of input nesting
// beyond two small stacks and stops at the first breach, so hostile input is
// refused after a handful of bytes per level: a body of millions of `[`, or a
// mapping with millions of keys, is refused after a few hundred bytes.
//
// Depth is the *syntax* depth: one level per increase in block indentation, one
// per compact sequence marker (the `- ` in `- name: x`), plus the number of open
// flow collections (`[`, `{`) on the current line. Keys are counted per mapping
// — for block mappings per sibling group at one column, for flow mappings per
// comma-separated entry. The estimate is deliberately conservative in the safe
// direction: a line whose indentation merely grows, the continuation line of a
// multi-line plain scalar, or a flow entry that turns out to be a scalar still
// counts. That can only refuse a body slightly early, never let a huge one
// through, and both limits are an order of magnitude above anything real.
//
// Out of scope: alias-driven *graph* expansion (`&a`/`*a` chains) builds a deep
// decoded value out of shallow syntax; yaml.v3's alias budget bounds it, and
// the expensive shapes measured above are all large mappings, which the key
// limit catches.
func scanYAMLLimits(text string, limits yamlScanLimits) yamlScanResult {
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = MaxYAMLNestingDepth
	}
	if limits.MaxKeys <= 0 {
		limits.MaxKeys = MaxYAMLMappingKeys
	}
	result := yamlScanResult{}

	// indents is the block-structure stack: one entry per nesting level of the
	// current line, holding that level's indentation.
	indents := make([]int, 0, 32)
	// mapFrames tracks the block mappings that are still open, so keys of one
	// mapping are counted together and a new sequence item starts a new one.
	mapFrames := make([]yamlBlockMapping, 0, 16)
	// flowFrames tracks the open flow collections, for both the depth of the
	// current line and the key count of flow mappings.
	flowFrames := make([]yamlFlowCollection, 0, 16)
	// quote holds the quote character of a quoted scalar that spans lines: its
	// content is not structure, and its indentation opens no block level.
	var quote byte
	// blockScalarIndent is the indentation of the line that opened a `|`/`>`
	// scalar, or -1 when none is open. Block scalar content is a plain string
	// to yaml.v3 — it can never recurse and never holds a key.
	blockScalarIndent := -1

	recordDepth := func(depth int) bool {
		if depth > result.Depth {
			result.Depth = depth
			if depth > limits.MaxDepth {
				result.Depth = limits.MaxDepth + 1
				result.DepthBreach = true
				return false
			}
		}
		return true
	}
	recordKeys := func(keys int) bool {
		if keys > result.MappingKeys {
			result.MappingKeys = keys
			if keys > limits.MaxKeys {
				result.MappingKeys = limits.MaxKeys + 1
				result.MappingKeyBreach = true
				return false
			}
		}
		return true
	}
	// countKey records one key of the frame below it and reports whether the
	// scan may continue.
	countKey := func(frameIndex int) bool {
		mapFrames[frameIndex].Keys++
		return recordKeys(mapFrames[frameIndex].Keys)
	}

	for offset := 0; ; {
		lineEnd := strings.IndexByte(text[offset:], '\n')
		line := text[offset:]
		lastLine := true
		if lineEnd >= 0 {
			line = text[offset : offset+lineEnd]
			lastLine = false
		}
		nextOffset := offset + lineEnd + 1

		line = strings.TrimRight(line, " \t\r")
		indent := 0
		for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
			indent++
		}
		content := line[indent:]

		blank := content == "" || content[0] == '#'
		inBlockScalar := blockScalarIndent >= 0 && indent > blockScalarIndent
		if !blank && !inBlockScalar && blockScalarIndent >= 0 {
			// Indentation is back at or left of the opener: the scalar ended.
			blockScalarIndent = -1
		}
		if blank || inBlockScalar {
			// Blank line, comment, or literal/folded scalar content: none of
			// them changes the structure of the document.
			if lastLine {
				return result
			}
			offset = nextOffset
			continue
		}

		continuing := len(flowFrames) > 0 || quote != 0
		scanFrom := 0
		if !continuing {
			for len(indents) > 0 && indents[len(indents)-1] >= indent {
				indents = indents[:len(indents)-1]
			}
			indents = append(indents, indent)
			if !recordDepth(len(indents)) {
				return result
			}

			// Compact nested sequences (`- - - value`) add one level each and
			// move the effective column of the content to their right.
			markers := 0
			for scanFrom+1 < len(content) &&
				content[scanFrom] == '-' &&
				(content[scanFrom+1] == ' ' || content[scanFrom+1] == '\t') {
				markers++
				scanFrom += 2
				if !recordDepth(len(indents) + markers) {
					return result
				}
			}
			column := indent + 2*markers

			isKey := yamlLineLooksLikeMappingKey(content[scanFrom:])
			if markers > 0 {
				// A sequence entry starts a new item, so the item's own
				// mapping is a new one even at the same column as the
				// previous item's keys.
				for len(mapFrames) > 0 && mapFrames[len(mapFrames)-1].Indent >= column {
					mapFrames = mapFrames[:len(mapFrames)-1]
				}
				mapFrames = append(mapFrames, yamlBlockMapping{Indent: column})
			} else {
				for len(mapFrames) > 0 && mapFrames[len(mapFrames)-1].Indent > column {
					mapFrames = mapFrames[:len(mapFrames)-1]
				}
				if len(mapFrames) == 0 || mapFrames[len(mapFrames)-1].Indent < column {
					mapFrames = append(mapFrames, yamlBlockMapping{Indent: column})
				}
			}
			if isKey {
				if !countKey(len(mapFrames) - 1) {
					return result
				}
			}
		}

		baseDepth := len(indents)
		if baseDepth == 0 {
			baseDepth = 1
		}
		for pos := scanFrom; pos < len(content); pos++ {
			c := content[pos]
			if quote != 0 {
				if c == '\\' && quote == '"' {
					// `\"` inside a double-quoted scalar is not its end.
					pos++
					continue
				}
				if c == quote {
					quote = 0
				}
				continue
			}
			switch c {
			case '\'':
				quote = '\''
			case '"':
				quote = '"'
			case '#':
				if pos == 0 || content[pos-1] == ' ' || content[pos-1] == '\t' {
					// Comment: the rest of the line carries no structure.
					pos = len(content)
					continue
				}
			case '[', '{':
				flowFrames = append(flowFrames, yamlFlowCollection{IsMapping: c == '{', Keys: 1})
				if !recordDepth(baseDepth + len(flowFrames)) {
					return result
				}
			case ']', '}':
				if len(flowFrames) > 0 {
					flowFrames = flowFrames[:len(flowFrames)-1]
				}
			case ',':
				// In a flow mapping each entry is a key/value pair, so a comma
				// is one more key of that mapping.
				if len(flowFrames) > 0 && flowFrames[len(flowFrames)-1].IsMapping {
					flowFrames[len(flowFrames)-1].Keys++
					if !recordKeys(flowFrames[len(flowFrames)-1].Keys) {
						return result
					}
				}
			}
		}

		if quote == 0 && len(flowFrames) == 0 && yamlLineOpensBlockScalar(content) {
			blockScalarIndent = indent
		}

		if lastLine {
			return result
		}
		offset = nextOffset
	}
}

// yamlBlockMapping is one open block mapping of the scan: the column of its
// keys and how many keys were seen so far.
type yamlBlockMapping struct {
	Indent int
	Keys   int
}

// yamlFlowCollection is one open flow collection of the scan.
type yamlFlowCollection struct {
	IsMapping bool
	Keys      int
}

// yamlLineLooksLikeMappingKey reports whether a content line (already stripped
// of indentation and of compact sequence markers) is a `key:` entry, which is
// the shape that costs yaml.v3's decoder its quadratic duplicate-key check.
func yamlLineLooksLikeMappingKey(content string) bool {
	if content == "" {
		return false
	}
	// An alias, anchor, tag or explicit key indicator is not a plain `key:`.
	switch content[0] {
	case '?', '*', '&', '!', '{', '[', '|', '>':
		return false
	}
	var quote byte
	for i := 0; i < len(content); i++ {
		c := content[i]
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			// A quoted key still ends with `:`; the quoted part is skipped so a
			// colon inside the key never counts.
			quote = c
		case '#':
			if i == 0 || content[i-1] == ' ' || content[i-1] == '\t' {
				return false
			}
		case ':':
			return i+1 >= len(content) || content[i+1] == ' ' || content[i+1] == '\t'
		}
	}
	return false
}

// yamlLineOpensBlockScalar reports whether a content line ends by opening a
// literal (`|`) or folded (`>`) block scalar, with an optional chomping or
// indentation modifier and an optional trailing comment.
func yamlLineOpensBlockScalar(content string) bool {
	trimmed := strings.TrimRight(content, " \t")
	if idx := strings.IndexByte(trimmed, '#'); idx >= 0 {
		trimmed = strings.TrimRight(trimmed[:idx], " \t")
	}
	end := len(trimmed)
	for end > 0 {
		switch trimmed[end-1] {
		case '+', '-', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			end--
			continue
		default:
		}
		break
	}
	if end == 0 {
		return false
	}
	last := trimmed[end-1]
	if last != '|' && last != '>' {
		return false
	}
	// The indicator must be its own token: `key: |` or a bare `|`, but not the
	// tail of a plain scalar such as `key: a|`.
	if end == 1 {
		return true
	}
	return trimmed[end-2] == ' ' || trimmed[end-2] == '\t'
}

// describeYAMLLimitBreach renders the report reason and the human-readable
// detail of a breached limit. The detail names the bound and the symptom; it
// never contains subscription content.
func describeYAMLLimitBreach(result yamlScanResult) (string, string) {
	if result.DepthBreach {
		return ReasonDepthExceeded, fmt.Sprintf(
			"clash yaml nesting depth exceeds %d; refusing to decode the body",
			MaxYAMLNestingDepth,
		)
	}
	return ReasonComplexityExceeded, fmt.Sprintf(
		"a clash yaml mapping holds more than %d keys, which costs the yaml decoder quadratic time; refusing to decode the body",
		MaxYAMLMappingKeys,
	)
}
