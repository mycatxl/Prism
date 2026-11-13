package nodeprivacy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

const MarkerHeader = "X-Prism-Privacy-Marker"

var IdentityHeaders = []string{"X-Forwarded-For", "Via", "Forwarded", "X-Real-IP", "Client-IP", "X-Client-IP", "True-Client-IP", "CF-Connecting-IP", "X-ProxyUser-IP"}

type EchoResponse struct {
	ObservedAddress string              `json:"observed_address"`
	Headers         map[string][]string `json:"headers"`
	Marker          string              `json:"marker"`
	MarkerSurvived  bool                `json:"marker_survived"`
}

func ValidateEchoURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path != "/check" {
		return fmt.Errorf("privacy echo requires an HTTPS URL without credentials or query")
	}
	return nil
}

// EchoHandler must terminate TLS directly. It deliberately does not trust
// proxy headers for RemoteAddr, read bodies, log requests, or reflect cookies.
func EchoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.TLS == nil || r.Method != http.MethodGet || r.URL.Path != "/check" || r.ContentLength != 0 || r.URL.RawQuery != "" {
		http.Error(w, "invalid check", http.StatusBadRequest)
		return
	}
	marker := r.Header.Get(MarkerHeader)
	b, err := hex.DecodeString(marker)
	if err != nil || len(b) != 16 {
		http.Error(w, "invalid marker", http.StatusBadRequest)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "invalid peer", 500)
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		http.Error(w, "invalid peer", 500)
		return
	}
	out := EchoResponse{ObservedAddress: ip.Unmap().String(), Headers: map[string][]string{}, Marker: marker, MarkerSurvived: true}
	for _, name := range IdentityHeaders {
		values := r.Header.Values(name)
		if len(strings.Join(values, "")) > 2048 {
			http.Error(w, "identity header too large", http.StatusRequestHeaderFieldsTooLarge)
			return
		}
		out.Headers[name] = values
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func NewMarker() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// EvaluateEcho compares only synthetic check traffic. Passing establishes this
// path/instant, not an assurance about arbitrary customer payloads or endpoints.
func EvaluateEcho(body []byte, marker string, exit netip.Addr) (bool, error) {
	var out EchoResponse
	if len(body) > 16*1024 {
		return false, fmt.Errorf("INVALID_RESPONSE")
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("INVALID_RESPONSE")
	}
	if out.Marker != marker || !out.MarkerSurvived || out.Headers == nil {
		return false, fmt.Errorf("CHALLENGE_MISMATCH")
	}
	observed, err := netip.ParseAddr(out.ObservedAddress)
	if err != nil || !exit.IsValid() {
		return false, fmt.Errorf("INVALID_RESPONSE")
	}
	if observed.Unmap() != exit.Unmap() {
		return true, nil
	}
	for _, name := range IdentityHeaders {
		values, ok := out.Headers[name]
		if !ok {
			return false, fmt.Errorf("HEADER_OBSERVATION_MISSING")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				continue
			}
			// A bare exit address is harmless; any additional identity or hop metadata
			// is conservatively rejected. We never send a real client identity here.
			ip, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil || ip.Unmap() != exit.Unmap() {
				return true, nil
			}
		}
	}
	return false, nil
}
