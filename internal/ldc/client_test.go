package ldc

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"reflect"
	"testing"
)

func TestQueryStringAndSign(t *testing.T) {
	params := map[string]string{
		"z":         "last",
		"a":         "first",
		"empty":     "",
		"sign":      "old-signature",
		"sign_type": "SHA256",
	}

	gotQuery := queryString(params)
	wantQuery := "a=first&z=last"
	if gotQuery != wantQuery {
		t.Fatalf("query string: got %q, want %q", gotQuery, wantQuery)
	}

	got := md5Hex(gotQuery + "secret")
	hash := md5.Sum([]byte("a=first&z=lastsecret"))
	want := hex.EncodeToString(hash[:])
	if got != want {
		t.Fatalf("signature: got %q, want %q", got, want)
	}
}

func TestBuildPaymentURL(t *testing.T) {
	client := NewClient(Config{
		PID:        "10001",
		Key:        "secret",
		GatewayURL: "https://payments.example/epay/",
		NotifyURL:  "https://merchant.example/notify",
		ReturnURL:  "https://merchant.example/return",
	})

	gotURL, err := client.BuildPaymentURL("ORDER-123", "Pro plan", 12.5)
	if err != nil {
		t.Fatalf("BuildPaymentURL() error = %v", err)
	}
	parsed, err := url.Parse(gotURL)
	if err != nil {
		t.Fatalf("parse payment URL: %v", err)
	}
	if got, want := parsed.Scheme+"://"+parsed.Host+parsed.Path, "https://payments.example/epay/pay/submit.php"; got != want {
		t.Fatalf("payment endpoint: got %q, want %q", got, want)
	}

	wantParams := url.Values{
		"pid":          {"10001"},
		"type":         {"epay"},
		"out_trade_no": {"ORDER-123"},
		"name":         {"Pro plan"},
		"money":        {"12.50"},
		"notify_url":   {"https://merchant.example/notify"},
		"return_url":   {"https://merchant.example/return"},
		"sign_type":    {"MD5"},
	}
	canonical := "money=12.50&name=Pro plan&notify_url=https://merchant.example/notify&out_trade_no=ORDER-123&pid=10001&return_url=https://merchant.example/return&type=epay"
	hash := md5.Sum([]byte(canonical + "secret"))
	wantParams.Set("sign", hex.EncodeToString(hash[:]))

	if got, want := parsed.Query(), wantParams; !reflect.DeepEqual(got, want) {
		t.Fatalf("query params: got %v, want %v", got, want)
	}
}

func TestNewClientDefaultGatewayURL(t *testing.T) {
	client := NewClient(Config{})
	got, err := client.BuildPaymentURL("order", "name", 1)
	if err != nil {
		t.Fatalf("BuildPaymentURL() error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse payment URL: %v", err)
	}
	if got, want := parsed.Scheme+"://"+parsed.Host+parsed.Path, "https://credit.linux.do/epay/pay/submit.php"; got != want {
		t.Fatalf("payment endpoint: got %q, want %q", got, want)
	}
}

func TestVerifyCallbackSuccess(t *testing.T) {
	client := NewClient(Config{Key: "secret"})
	params := map[string]string{
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
		"out_trade_no": "ORDER-123",
		"money":        "12.50",
		"sign_type":    "MD5",
	}
	params["sign"] = md5Hex(queryString(params) + "secret")

	valid, err := client.VerifyCallback(params)
	if err != nil {
		t.Fatalf("VerifyCallback() error = %v", err)
	}
	if !valid {
		t.Fatal("VerifyCallback() = false, want true")
	}
}

func TestVerifyCallbackInvalidSignature(t *testing.T) {
	client := NewClient(Config{Key: "secret"})
	params := map[string]string{
		"trade_status": "TRADE_SUCCESS",
		"sign":         "not-a-valid-signature",
	}

	valid, err := client.VerifyCallback(params)
	if err != nil {
		t.Fatalf("VerifyCallback() error = %v", err)
	}
	if valid {
		t.Fatal("VerifyCallback() = true, want false")
	}
}

func TestVerifyCallbackNonSuccessTradeStatus(t *testing.T) {
	client := NewClient(Config{Key: "secret"})
	params := map[string]string{
		"trade_status": "TRADE_CLOSED",
		"out_trade_no": "ORDER-123",
	}
	params["sign"] = md5Hex(queryString(params) + "secret")

	valid, err := client.VerifyCallback(params)
	if err != nil {
		t.Fatalf("VerifyCallback() error = %v", err)
	}
	if valid {
		t.Fatal("VerifyCallback() = true, want false")
	}
}

func TestEmptyAndMissingParameters(t *testing.T) {
	client := NewClient(Config{
		PID:       "10001",
		Key:       "secret",
		NotifyURL: "",
		ReturnURL: "",
	})

	gotURL, err := client.BuildPaymentURL("", "", 0)
	if err != nil {
		t.Fatalf("BuildPaymentURL() error = %v", err)
	}
	parsed, err := url.Parse(gotURL)
	if err != nil {
		t.Fatalf("parse payment URL: %v", err)
	}
	got := parsed.Query()
	for _, key := range []string{"pid", "type", "money", "sign", "sign_type"} {
		if got.Get(key) == "" {
			t.Errorf("query parameter %q is missing or empty", key)
		}
	}
	for _, key := range []string{"out_trade_no", "name", "notify_url", "return_url"} {
		if _, ok := got[key]; ok {
			t.Errorf("query parameter %q should be omitted, got %v", key, got[key])
		}
	}

	for name, params := range map[string]map[string]string{
		"nil params":     nil,
		"missing sign":   {"trade_status": "TRADE_SUCCESS"},
		"missing status": {"sign": md5Hex("secret")},
	} {
		t.Run(name, func(t *testing.T) {
			valid, err := client.VerifyCallback(params)
			if err != nil {
				t.Fatalf("VerifyCallback() error = %v", err)
			}
			if valid {
				t.Fatal("VerifyCallback() = true, want false")
			}
		})
	}
}
