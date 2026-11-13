package ldc

import "testing"

func TestPaymentSigningRejectsAmbiguousParameterBoundaries(t *testing.T) {
	for _, cfg := range []Config{
		{Key: "test-secret", ReturnURL: "https://checkout.test&trade_status=TRADE_SUCCESS&tx=x/return"},
		{Key: "test-secret", NotifyURL: "https://checkout.test/notify?a=1&b=2"},
		{Key: "test-secret", PID: "merchant&out_trade_no=other"},
	} {
		if signed, err := NewClient(cfg).BuildPaymentURLCents("order", "deposit", 100); err == nil || signed != "" {
			t.Fatal("signed a value containing an ambiguous parameter separator")
		}
	}
}

func TestCallbackRejectsAmbiguousParameterBoundaries(t *testing.T) {
	client := NewClient(Config{Key: "test-secret"})
	for _, params := range []map[string]string{
		{"out_trade_no": "order", "trade_status": "TRADE_SUCCESS", "notify_url": "https://checkout.test&a=1"},
		{"out_trade_no": "order", "trade_status": "TRADE_SUCCESS", "tx&extra": "value"},
		{"out_trade_no": "order", "trade_status": "TRADE_SUCCESS", "tx=extra": "value"},
	} {
		params["sign"] = md5Hex(queryString(params) + "test-secret")
		if valid, err := client.VerifyCallback(params); err != nil || valid {
			t.Fatalf("ambiguous callback accepted: valid=%v err=%v", valid, err)
		}
	}
}

func TestCheckoutSignatureCannotBeReinterpretedAsPaidCallback(t *testing.T) {
	checkout := map[string]string{
		"pid": "merchant", "type": "epay", "out_trade_no": "round-two-order", "money": "1.00", "name": "deposit",
		"notify_url": "https://checkout.test&trade_status=TRADE_SUCCESS&tx=x/notify",
		"return_url": "https://checkout.test&trade_status=TRADE_SUCCESS&tx=x/return",
	}
	callback := make(map[string]string)
	for key, value := range checkout {
		callback[key] = value
	}
	callback["return_url"] = "https://checkout.test"
	callback["trade_status"] = "TRADE_SUCCESS"
	callback["tx"] = "x/return"
	if queryString(checkout) != queryString(callback) {
		t.Fatal("fixture does not reproduce the ambiguous EasyPay boundary")
	}
	// This is a checkout signature, not a provider's successful payment attestation.
	callback["sign"] = md5Hex(queryString(checkout) + "test-secret")
	valid, err := NewClient(Config{Key: "test-secret"}).VerifyCallback(callback)
	if err != nil || valid {
		t.Fatalf("checkout signature accepted as payment: %v, %v", valid, err)
	}
}
