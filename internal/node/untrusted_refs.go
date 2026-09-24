// Untrusted local file references in node documents.
//
// A node document can be produced from untrusted subscription content:
//
//   - a sing-box JSON outbound is copied verbatim, so tls.certificate_path,
//     tls.client_certificate_path, tls.client_key_path, tls.key_path,
//     tls.crl_path, static_key_path and private_key_path survive untouched;
//   - a Clash/Surge `ca:` and a share link's `ca=` are mapped onto
//     tls.certificate_path.
//
// sing-box then *reads* the named file while it builds the outbound, and when
// the read or the certificate parse fails it puts the file's content into the
// error string ("failed to parse certificate:\n\n" + content). That error is
// stored in NodeEntry.LastError and returned by the node APIs, so a
// subscription could make Prism read any local file and echo it back through
// the API. A path like /dev/zero or a FIFO also makes the read unbounded.
//
// The .ovpn parser already refuses file references and demands inline material
// (see rejectOpenVPNFilePath in openvpn.go). This file is the same rule for
// every other format: a document that names a file instead of carrying
// material is refused before it reaches the engine, with a reason code and a
// detail string that name the offending field only.
//
// The check runs in ParseNodeDoc, which every consumer of a node document uses
// (the outbound builder, the export path and the subscription parse report), so
// no format can bypass it. Inline PEM material keeps working.
package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// LocalFileRefError reports a node document that references a local file. It
// mirrors OpenVPNParseError: Reason is one of the parse-report reason codes and
// Detail names the offending field, never the file path and never file content.
type LocalFileRefError struct {
	Reason string
	Detail string
}

// Error implements error.
func (e *LocalFileRefError) Error() string {
	return e.Reason + ": " + e.Detail
}

const (
	// localFileRefDetailSuffix is the user-visible explanation. It matches the
	// wording the .ovpn parser uses for the same class of reference.
	localFileRefDetailSuffix = ": file path is not supported; inline the material"
	// localFileRefKeySuffix is the option-name pattern sing-box uses for every
	// option whose value is a path it opens: certificate_path, key_path,
	// client_key_path, crl_path, static_key_path, private_key_path,
	// credential_path, wrapper_path, protect_path, ...
	localFileRefKeySuffix = "_path"
	// pemMarker identifies inline material, which is accepted.
	pemMarker = "-----BEGIN"
	// maxLocalFileRefPathRunes bounds the field path echoed in Detail. The field
	// names come from untrusted input, so the detail stays short and printable.
	maxLocalFileRefPathRunes = 64
	// maxLocalFileRefDepth bounds the scan. Every real option lives far above
	// it; deeper nesting is not part of any sing-box schema, so nothing that
	// could be read hides below.
	maxLocalFileRefDepth = 64
)

// RejectLocalFileRefs reports whether raw (a node document) names a local file
// in a path-valued option. It returns nil for a document that carries inline
// material, for an empty document and for input that is not a JSON object (the
// document parser reports that case).
func RejectLocalFileRefs(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return scanLocalFileRefs(decoded, "", 0)
}

// LocalFileRefReason extracts the reason code and detail of a refusal returned
// by RejectLocalFileRefs. ok is false for any other error, so a caller can turn
// the refusal into a parse-report entry without matching strings.
func LocalFileRefReason(err error) (reason string, detail string, ok bool) {
	var refErr *LocalFileRefError
	if !errors.As(err, &refErr) {
		return "", "", false
	}
	return refErr.Reason, refErr.Detail, true
}

func scanLocalFileRefs(value any, path string, depth int) error {
	if depth > maxLocalFileRefDepth {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		// Sorted so that a document with several references always reports the
		// same field.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fieldPath := joinLocalFileRefPath(path, key)
			if isLocalFileRefKey(key) && namesLocalFile(typed[key]) {
				return &LocalFileRefError{
					Reason: ReasonUnsupportedFeature,
					Detail: fieldPath + localFileRefDetailSuffix,
				}
			}
			if err := scanLocalFileRefs(typed[key], fieldPath, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := scanLocalFileRefs(child, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// isLocalFileRefKey reports whether a JSON key names a file sing-box would open.
func isLocalFileRefKey(key string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(key)), localFileRefKeySuffix)
}

// namesLocalFile reports whether a path-valued option holds a file reference
// rather than inline material. An empty value names nothing, and inline PEM
// (which is what the parser produces for `ca-str` / `certificate`) is accepted.
func namesLocalFile(value any) bool {
	switch typed := value.(type) {
	case string:
		return isLocalFileRefValue(typed)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if ok && isLocalFileRefValue(text) {
				return true
			}
		}
	}
	return false
}

func isLocalFileRefValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return !strings.Contains(trimmed, pemMarker)
}

// joinLocalFileRefPath appends one sanitised key to a JSON field path so that
// Detail cannot reflect arbitrary attacker text back through the API.
func joinLocalFileRefPath(path string, key string) string {
	cleaned := sanitizeLocalFileRefKey(key)
	if path == "" {
		return cleaned
	}
	if cleaned == "" {
		return path
	}
	return path + "." + cleaned
}

func sanitizeLocalFileRefKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
		if builder.Len() >= maxLocalFileRefPathRunes {
			break
		}
	}
	return builder.String()
}
