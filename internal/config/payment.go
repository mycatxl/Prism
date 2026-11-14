package config

import "strings"

// SeedPaymentConfig imports deployment defaults once. Saved false/empty values
// remain authoritative on restart, including intentionally removed credentials.
func (c *RuntimeConfig) SeedPaymentConfig(env *EnvConfig) {
	if c.PaymentConfigInitialized {
		return
	}
	if env != nil {
		if c.LDCGatewayURL == "" {
			c.LDCGatewayURL = env.LDCGatewayURL
		}
		if c.LDCPID == "" {
			c.LDCPID = strings.TrimSpace(env.LDCPID)
		}
		if c.LDCKey == "" {
			c.LDCKey = strings.TrimSpace(env.LDCKey)
		}
		if c.LDCNotifyURL == "" {
			c.LDCNotifyURL = env.LDCNotifyURL
		}
		if c.LDCReturnURL == "" {
			c.LDCReturnURL = env.LDCReturnURL
		}
		c.LDCEnabled = c.LDCEnabled || (env.LDCPID != "" && env.LDCKey != "")
		if c.TronAddress == "" {
			c.TronAddress = strings.TrimSpace(env.USDTAddress)
		}
		if c.TronUSDTContract == "" {
			c.TronUSDTContract = env.USDTContract
		}
		if c.TronAPIKey == "" {
			c.TronAPIKey = env.TronGridAPIKey
		}
		c.TronDepositEnabled = c.TronDepositEnabled || env.USDTAddress != ""
	}
	if c.LDCGatewayURL == "" {
		c.LDCGatewayURL = "https://credit.linux.do/epay"
	}
	if c.LDCRate == 0 {
		c.LDCRate = 1
	}
	if c.TronUSDTContract == "" {
		c.TronUSDTContract = DefaultUSDTContract
	}
	if c.TronTRXRatio == 0 {
		c.TronTRXRatio = 1
	}
	if c.TronWindowMinutes == 0 {
		c.TronWindowMinutes = 15
	}
	c.PaymentConfigInitialized = true
}

// PublicView masks secrets without changing their database representation.
type RuntimeConfigView struct {
	*RuntimeConfig
	LDCKeySet                bool              `json:"ldc_key_set"`
	TronAPIKeySet            bool              `json:"tron_api_key_set"`
	AlertTelegramBotTokenSet bool              `json:"alert_telegram_bot_token_set"`
	PaymentURLChecks         []PaymentURLCheck `json:"payment_url_checks,omitempty"`
}

// PaymentURLCheck is an advisory result for a callback URL at save time. It
// contains safe classifications only, never raw URLs or transport diagnostics.
type PaymentURLCheck struct {
	Field      string `json:"field"`
	Status     string `json:"status"`
	Code       string `json:"code"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (c *RuntimeConfig) PublicView() *RuntimeConfigView {
	if c == nil {
		return nil
	}
	copy := *c
	copy.LDCKey, copy.TronAPIKey = "", ""
	copy.AlertTelegramBotToken = ""
	copy.NormalizeOperationalAlerts()
	return &RuntimeConfigView{RuntimeConfig: &copy, LDCKeySet: c.LDCKey != "", TronAPIKeySet: c.TronAPIKey != "", AlertTelegramBotTokenSet: c.AlertTelegramBotToken != ""}
}
