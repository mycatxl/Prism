package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/idna"

	"prism/internal/addrpolicy"
	"prism/internal/config"
	"prism/internal/netutil"
)

func validatePaymentURL(field, raw string) (*url.URL, *ServiceError) {
	invalid := func() (*url.URL, *ServiceError) {
		return nil, invalidArg(field + ": must be an absolute HTTPS URL with a valid host and port, without credentials, query or fragment")
	}
	if len(raw) > 2048 || strings.Contains(raw, "#") || strings.IndexFunc(raw, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return invalid()
	}
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return invalid()
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if ip.Zone() != "" {
			return invalid()
		}
	} else {
		host, err := idna.Lookup.ToASCII(u.Hostname())
		host = strings.TrimSuffix(host, ".")
		if err != nil || len(host) == 0 || len(host) > 253 {
			return invalid()
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return invalid()
			}
			for _, c := range strings.ToLower(label) {
				if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
					return invalid()
				}
			}
		}
	}
	if field != "ldc_gateway_url" && strings.Contains(u.Path, "&") {
		return nil, invalidArg(field + ": must not contain an EasyPay parameter separator")
	}
	return u, nil
}

type PaymentURLChecker struct {
	// Injection points for local tests. Production uses verifying HTTPS, no
	// environment proxies, and an IP guard immediately before every connection.
	LookupIP func(context.Context, string) ([]netip.Addr, error)
	Client   *http.Client
}

func (s *ControlPlaneService) CheckPaymentURLs(ctx context.Context, patchJSON []byte, cfg *config.RuntimeConfig) []config.PaymentURLCheck {
	var patch map[string]json.RawMessage
	if cfg == nil || json.Unmarshal(patchJSON, &patch) != nil {
		return nil
	}
	_, toggled := patch["ldc_enabled"]
	checkBoth := toggled && cfg.LDCEnabled
	checker := &PaymentURLChecker{}
	if s != nil && s.PaymentURLChecker != nil {
		checker = s.PaymentURLChecker
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var checks []config.PaymentURLCheck
	for _, entry := range []struct{ field, value string }{{"ldc_notify_url", cfg.LDCNotifyURL}, {"ldc_return_url", cfg.LDCReturnURL}} {
		if _, changed := patch[entry.field]; !changed && !checkBoth {
			continue
		}
		checks = append(checks, checker.check(ctx, entry.field, entry.value))
	}
	return checks
}

func (c *PaymentURLChecker) check(ctx context.Context, field, raw string) config.PaymentURLCheck {
	result := config.PaymentURLCheck{Field: field, Status: "warning"}
	if raw == "" {
		result.Code = "derived"
		return result
	}
	u, err := validatePaymentURL(field, raw)
	if err != nil {
		result.Code = "invalid_url"
		return result
	}
	host, _ := idna.Lookup.ToASCII(u.Hostname())
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		ips = []netip.Addr{ip}
	} else {
		if addrpolicy.HostIsForbiddenLexically(host) || strings.HasSuffix(host, ".invalid") {
			result.Code = "non_public"
			return result
		}
		lookup := c.LookupIP
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		var lookupErr error
		ips, lookupErr = lookup(ctx, host)
		if lookupErr != nil || len(ips) == 0 {
			result.Code = "dns_failed"
			return result
		}
	}
	for _, ip := range ips {
		if addrpolicy.AddrIsForbidden(ip.Unmap()) {
			result.Code = "non_public"
			return result
		}
	}
	client := c.Client
	if client == nil {
		transport := netutil.GuardedTransport()
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodHead, raw, nil)
	if requestErr != nil {
		result.Code = "invalid_url"
		return result
	}
	req.Header.Set("User-Agent", "Prism-Callback-Check")
	response, requestErr := copy.Do(req)
	if requestErr != nil {
		result.Code = "connection_failed"
		return result
	}
	response.Body.Close()
	result.HTTPStatus = response.StatusCode
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if field == "ldc_notify_url" && u.Path != "/api/v1/public/ldc/notify" {
			result.Code = "unexpected_path"
		} else {
			result.Status, result.Code = "reachable", "reachable"
		}
	case response.StatusCode >= 300 && response.StatusCode < 400:
		result.Code = "redirect"
	case response.StatusCode == 404:
		result.Code = "not_found"
	case response.StatusCode == 401 || response.StatusCode == 403:
		result.Code = "access_denied"
	case response.StatusCode == 400 || response.StatusCode == 405:
		result.Code = "method_or_signature"
	default:
		result.Code = "http_error"
	}
	return result
}
