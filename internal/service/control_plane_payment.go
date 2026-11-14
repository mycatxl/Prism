package service

import (
	"math"
	"math/big"
	"strconv"
	"strings"

	"prism/internal/config"
	"prism/internal/ldc"
	"prism/internal/tron"
)

// Rates retain the existing numeric storage/API shape. Refuse a decimal whose
// value would change when that representation is serialized for exact credit
// arithmetic (for example 0.99999999999999999 becoming 1).
func exactPaymentRateJSON(raw []byte) bool {
	if len(raw) > 64 {
		return false
	}
	text := strings.TrimSpace(string(raw))
	rate, err := strconv.ParseFloat(text, 64)
	if err != nil || rate <= 0 || rate > 1_000_000 {
		return false
	}
	want, ok := new(big.Rat).SetString(text)
	if !ok {
		return false
	}
	stored, ok := new(big.Rat).SetString(strconv.FormatFloat(rate, 'g', -1, 64))
	return ok && want.Cmp(stored) == 0
}

func (s *ControlPlaneService) paymentConfig() *config.RuntimeConfig {
	c := config.NewDefaultRuntimeConfig()
	if s != nil {
		if s.RuntimeCfg != nil {
			if current := s.RuntimeCfg.Load(); current != nil {
				c = copyRuntimeConfig(current)
			}
		}
		c.SeedPaymentConfig(s.EnvCfg)
	} else {
		c.SeedPaymentConfig(nil)
	}
	return c
}

func paymentLDCClient(c *config.RuntimeConfig) *ldc.Client {
	return ldc.NewClient(ldc.Config{PID: c.LDCPID, Key: c.LDCKey, GatewayURL: c.LDCGatewayURL, NotifyURL: c.LDCNotifyURL, ReturnURL: c.LDCReturnURL})
}

func validatePaymentConfig(c *config.RuntimeConfig) *ServiceError {
	// Older tests/configurations can still contain the uninitialized zero value.
	if !c.PaymentConfigInitialized {
		c.SeedPaymentConfig(nil)
	}
	for _, v := range []struct {
		name string
		rate float64
	}{{"ldc_rate", c.LDCRate}, {"tron_trx_ratio", c.TronTRXRatio}} {
		if math.IsNaN(v.rate) || math.IsInf(v.rate, 0) || v.rate <= 0 || v.rate > 1_000_000 {
			return invalidArg(v.name + ": must be finite and between 0 (exclusive) and 1000000")
		}
	}
	if c.TronWindowMinutes < 1 || c.TronWindowMinutes > 1440 {
		return invalidArg("tron_window_minutes: must be between 1 and 1440")
	}
	for _, v := range []struct{ name, value string }{{"ldc_gateway_url", c.LDCGatewayURL}, {"ldc_notify_url", c.LDCNotifyURL}, {"ldc_return_url", c.LDCReturnURL}} {
		if v.value == "" {
			continue
		}
		if _, err := validatePaymentURL(v.name, v.value); err != nil {
			return err
		}
	}
	for _, v := range []struct{ name, value string }{{"tron_address", c.TronAddress}, {"tron_usdt_contract", c.TronUSDTContract}} {
		if v.value != "" && !tron.ValidAddress(v.value) {
			return invalidArg(v.name + ": invalid TRON address or checksum")
		}
	}
	for _, v := range []struct{ name, value string }{{"ldc_pid", c.LDCPID}, {"ldc_key", c.LDCKey}, {"tron_api_key", c.TronAPIKey}} {
		if len(v.value) > 512 || strings.ContainsAny(v.value, "\r\n\x00") || strings.TrimSpace(v.value) != v.value {
			return invalidArg(v.name + ": invalid credential")
		}
	}
	if strings.Contains(c.LDCPID, "&") {
		return invalidArg("ldc_pid: must not contain an EasyPay parameter separator")
	}
	if c.LDCEnabled && (c.LDCPID == "" || c.LDCKey == "" || c.LDCGatewayURL == "") {
		return invalidArg("ldc_enabled: gateway, merchant ID and secret are required")
	}
	if c.TronDepositEnabled && (c.TronAddress == "" || c.TronUSDTContract == "") {
		return invalidArg("tron_deposit_enabled: recipient and USDT contract are required")
	}
	return nil
}
