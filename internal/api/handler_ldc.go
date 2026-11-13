package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"prism/internal/service"
)

// HandleMeLDCInfo implements GET /api/v1/me/ldc/info.
func HandleMeLDCInfo(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, ok := meActor(w, r); !ok {
			return
		}
		WriteJSON(w, http.StatusOK, cp.SelfLDCInfo())
	}
}

// HandleMeLDCPay implements POST /api/v1/me/ldc/pay.
func HandleMeLDCPay(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		var in struct {
			Points json.Number `json:"points"`
		}
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if r.Host == "" || strings.ContainsAny(r.Host, "&=/?#\\") {
			writeServiceError(w, invalidArgumentError("LDC payment: invalid host"))
			return
		}
		origin := url.URL{Scheme: "https", Host: r.Host}
		host := r.Host
		if hostname, _, err := net.SplitHostPort(host); err == nil {
			host = hostname
		}
		if ip := net.ParseIP(host); r.TLS == nil && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
			origin.Scheme = "http"
		}
		redirectURL, outTradeNo, err := cp.CreateLDCDepositDecimal(actor.UserID, string(in.Points), time.Now().UnixNano(), origin.String())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{
			"redirect_url": redirectURL,
			"out_trade_no": outTradeNo,
		})
	}
}

// HandlePublicLDCCallback implements GET/POST /api/v1/public/ldc/notify.
// EasyPay sends form-encoded POST callbacks, while GET is accepted for
// providers that use a query-string notification.
func HandlePublicLDCCallback(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := ldcCallbackParams(r)
		if err != nil {
			writeLDCCallbackError(w, invalidArgumentError(err.Error()))
			return
		}
		if err := cp.HandleLDCCallback(params, time.Now().UnixNano()); err != nil {
			writeLDCCallbackError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	}
}

func ldcCallbackParams(r *http.Request) (map[string]string, error) {
	if r == nil {
		return nil, errors.New("LDC callback: missing request")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return nil, errors.New("LDC callback: method must be GET or POST")
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			return nil, fmt.Errorf("LDC callback: invalid form: %w", err)
		}
	}
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.Form
	}
	params := make(map[string]string, len(values))
	for key, items := range values {
		if len(items) != 1 {
			return nil, errors.New("LDC callback: duplicate parameter")
		}
		if strings.TrimSpace(key) != key || key == "" {
			return nil, errors.New("LDC callback: invalid parameter name")
		}
		params[key] = items[0]
	}
	return params, nil
}

func writeLDCCallbackError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) && svcErr.Code == "INTERNAL" {
		status = http.StatusInternalServerError
	}
	// The provider only needs a non-success response to retry. Do not echo the
	// signed callback fields, which can contain credentials or gateway data.
	http.Error(w, "error", status)
}
