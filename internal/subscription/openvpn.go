package subscription

import (
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/node"
)

// .ovpn profiles and the Prism OpenVPN bundle format (WP06 §4).
//
// Two inputs are recognised here:
//
//	raw .ovpn text   — one endpoint node;
//	{"prism_openvpn_bundle":1,"profiles":[{"name","ovpn","username","password"}]}
//	                 — one endpoint node per profile.
//
// A profile that cannot be represented is reported with the reason code the
// .ovpn parser produced, never dropped silently.

const (
	// maxOpenVPNBundleProfiles bounds one bundle document.
	maxOpenVPNBundleProfiles = 256
	// openVPNBundleVersion is the only supported bundle version.
	openVPNBundleVersion = 1
)

type openVPNBundleProfile struct {
	Name     string `json:"name"`
	OVPN     string `json:"ovpn"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type openVPNBundle struct {
	Version  int                    `json:"prism_openvpn_bundle"`
	Profiles []openVPNBundleProfile `json:"profiles"`
}

// parseOpenVPNSubscription recognises a raw .ovpn profile (WP06 §4.1).
func parseOpenVPNSubscription(text string, report *parseReport) ([]ParsedNode, bool, error) {
	if !node.LooksLikeOpenVPN(text) {
		return nil, false, nil
	}
	profiles := []openVPNBundleProfile{{OVPN: text}}
	// A batch payload may also arrive as a base64-wrapped bundle document; the
	// caller retries with the decoded body, so nothing extra is needed here.
	nodes := appendOpenVPNProfiles(profiles, report)
	return nodes, true, nil
}

// parseOpenVPNBundleJSON recognises the prism-openvpn-bundle format.
func parseOpenVPNBundleJSON(obj map[string]json.RawMessage, report *parseReport) ([]ParsedNode, bool, error) {
	if _, ok := obj["prism_openvpn_bundle"]; !ok {
		return nil, false, nil
	}
	var bundle openVPNBundle
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, true, fmt.Errorf("subscription: encode openvpn bundle: %w", err)
	}
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		return nil, true, fmt.Errorf("subscription: unmarshal openvpn bundle: %w", err)
	}
	if bundle.Version != openVPNBundleVersion {
		return nil, true, fmt.Errorf("subscription: unsupported prism_openvpn_bundle version %d", bundle.Version)
	}
	if len(bundle.Profiles) > maxOpenVPNBundleProfiles {
		return nil, true, fmt.Errorf("subscription: openvpn bundle has %d profiles, limit is %d", len(bundle.Profiles), maxOpenVPNBundleProfiles)
	}
	return appendOpenVPNProfiles(bundle.Profiles, report), true, nil
}

// appendOpenVPNProfiles converts each profile, reporting the ones it refuses.
func appendOpenVPNProfiles(profiles []openVPNBundleProfile, report *parseReport) []ParsedNode {
	nodes := make([]ParsedNode, 0, len(profiles))
	for index, profile := range profiles {
		text := profile.OVPN
		if strings.TrimSpace(text) == "" {
			report.addSkip(SkippedNode{
				Name:   strings.TrimSpace(profile.Name),
				Type:   "openvpn-client",
				Source: SourceOpenVPN,
				Reason: node.ReasonInvalid,
				Detail: fmt.Sprintf("profile %d is empty", index),
			})
			continue
		}
		parsed, err := node.ParseOpenVPNProfile(text, profile.Name, profile.Username, profile.Password)
		if err != nil {
			skip := SkippedNode{
				Name:   strings.TrimSpace(profile.Name),
				Type:   "openvpn-client",
				Source: SourceOpenVPN,
				Reason: node.ReasonInvalid,
				Detail: err.Error(),
			}
			var parseErr *node.OpenVPNParseError
			if asOpenVPNParseError(err, &parseErr) {
				skip.Reason = parseErr.Reason
				skip.Detail = parseErr.Detail
			}
			report.addSkip(skip)
			continue
		}
		encoded, err := parsed.EnvelopeObject(parsed.Name)
		if err != nil {
			report.addSkip(SkippedNode{
				Name:   parsed.Name,
				Type:   "openvpn-client",
				Source: SourceOpenVPN,
				Reason: node.ReasonInvalid,
				Detail: err.Error(),
			})
			continue
		}
		nodes = append(nodes, ParsedNode{
			Tag:        parsed.Name,
			RawOptions: encoded,
			Detail:     parsed.Detail(),
		})
	}
	return nodes
}

// asOpenVPNParseError unwraps a *node.OpenVPNParseError without importing errors
// into every call site.
func asOpenVPNParseError(err error, target **node.OpenVPNParseError) bool {
	parseErr, ok := err.(*node.OpenVPNParseError)
	if !ok {
		return false
	}
	*target = parseErr
	return true
}
