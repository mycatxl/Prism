package api

import (
	"net/http"

	"prism/internal/buildinfo"
	"prism/internal/node"
)

// systemCapabilitiesResponse is the GET /api/v1/system/capabilities body
// (WP06 §10).
type systemCapabilitiesResponse struct {
	Engines          []node.EngineCapability `json:"engines"`
	BuildTags        []string                `json:"build_tags"`
	ShareLinkSchemes []string                `json:"share_link_schemes"`
	FileFormats      []string                `json:"file_formats"`
}

// HandleSystemCapabilities returns a handler for
// GET /api/v1/system/capabilities.
func HandleSystemCapabilities() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, systemCapabilitiesResponse{
			Engines:          node.EngineCapabilities(),
			BuildTags:        buildinfo.TagList(),
			ShareLinkSchemes: node.ShareLinkSchemes(),
			FileFormats:      node.FileFormats(),
		})
	}
}
