// Package ldc implements the Linux.do Credit EasyPay integration.
package ldc

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const defaultGatewayURL = "https://credit.linux.do/epay"

// Config contains the credentials and callback URLs used by the EasyPay
// gateway.
type Config struct {
	PID        string
	Key        string
	GatewayURL string
	NotifyURL  string
	ReturnURL  string
}

// Client builds EasyPay payment requests and verifies gateway callbacks.
type Client struct {
	pid        string
	key        string
	gatewayURL string
	notifyURL  string
	returnURL  string
}

// NewClient returns a client configured with cfg. An empty GatewayURL uses the
// Linux.do Credit EasyPay endpoint.
func NewClient(cfg Config) *Client {
	gatewayURL := cfg.GatewayURL
	if gatewayURL == "" {
		gatewayURL = defaultGatewayURL
	}

	return &Client{
		pid:        cfg.PID,
		key:        cfg.Key,
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		notifyURL:  cfg.NotifyURL,
		returnURL:  cfg.ReturnURL,
	}
}

// BuildPaymentURL builds a URL that sends one payment to EasyPay.
func (c *Client) BuildPaymentURL(outTradeNo string, name string, money float64) (string, error) {
	return c.buildPaymentURL(outTradeNo, name, fmt.Sprintf("%.2f", money))
}

// BuildPaymentURLCents formats a validated amount without floating arithmetic.
func (c *Client) BuildPaymentURLCents(outTradeNo, name string, cents int64) (string, error) {
	return c.buildPaymentURL(outTradeNo, name, fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

func (c *Client) buildPaymentURL(outTradeNo, name, money string) (string, error) {
	params := map[string]string{
		"pid":          c.pid,
		"type":         "epay",
		"out_trade_no": outTradeNo,
		"name":         name,
		"money":        money,
		"notify_url":   c.notifyURL,
		"return_url":   c.returnURL,
	}
	if !unambiguousParameters(params) {
		return "", fmt.Errorf("LDC payment: ambiguous parameter separator")
	}

	query := queryString(params)
	sign := md5Hex(query + c.key)

	values := url.Values{}
	for key, value := range params {
		if value != "" {
			values.Set(key, value)
		}
	}
	values.Set("sign", sign)
	values.Set("sign_type", "MD5")

	return c.gatewayURL + "/pay/submit.php?" + values.Encode(), nil
}

// VerifyCallback verifies an EasyPay callback signature and its payment
// status.
func (c *Client) VerifyCallback(params map[string]string) (bool, error) {
	if !unambiguousParameters(params) {
		return false, nil
	}
	sign := params["sign"]
	expected := md5Hex(queryString(params) + c.key)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sign)) != 1 {
		return false, nil
	}
	if params["trade_status"] != "TRADE_SUCCESS" {
		return false, nil
	}
	return true, nil
}

// EasyPay signs decoded values joined with literal '&' and '=' separators.
// Reject ambiguous boundaries on both sides so a checkout signature cannot be
// reinterpreted as a callback with additional signed fields.
func unambiguousParameters(params map[string]string) bool {
	for key, value := range params {
		if strings.ContainsAny(key, "&=") || strings.Contains(value, "&") {
			return false
		}
	}
	return true
}

// queryString returns the EasyPay canonical parameter string. Empty values,
// the supplied signature, and its type are excluded before ASCII key sorting.
func queryString(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key == "sign" || key == "sign_type" || value == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
