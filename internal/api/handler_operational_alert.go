package api

import (
	"errors"
	"net/http"

	"prism/internal/alerting"
	"prism/internal/service"
)

// Test notifications require the administrator's browser session and CSRF,
// just like saving the external notification credentials.
func HandleTestOperationalAlert(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := adminActor(w, r)
		if !ok || !requireSessionActor(w, actor) {
			return
		}
		if cp == nil || cp.AlertNotifier == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "notification sender is unavailable")
			return
		}
		if err := cp.AlertNotifier.SendTest(r.Context()); err != nil {
			if errors.Is(err, alerting.ErrDisabled) {
				writeInvalidArgument(w, "save a complete enabled Telegram channel before testing")
			} else {
				WriteError(w, http.StatusBadGateway, "NOTIFICATION_FAILED", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]bool{"delivered": true})
	}
}
