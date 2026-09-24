package api

import (
	"net/http"

	"prism/internal/service"
)

// HandleExplainPlatformNode returns a handler for
// GET /api/v1/platforms/{id}/nodes/{hash}/explain.
func HandleExplainPlatformNode(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		hash := PathParam(r, "hash")
		if len(hash) > 128 {
			writeInvalidArgument(w, "node_hash: invalid length")
			return
		}
		rules, err := cp.ExplainPlatformNode(platformID, hash)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, rules)
	}
}
