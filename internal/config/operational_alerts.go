package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultAlertDiskFreePercent = 10
	DefaultAlertCooldownMinutes = 15
)

// Older system_config rows decode new thresholds as zero.
func (c *RuntimeConfig) NormalizeOperationalAlerts() {
	if c.AlertDiskFreePercent == 0 {
		c.AlertDiskFreePercent = DefaultAlertDiskFreePercent
	}
	if c.AlertCooldownMinutes == 0 {
		c.AlertCooldownMinutes = DefaultAlertCooldownMinutes
	}
}

func (c *RuntimeConfig) ValidateOperationalAlerts() error {
	c.NormalizeOperationalAlerts()
	if c.AlertDiskFreePercent < 1 || c.AlertDiskFreePercent > 50 {
		return fmt.Errorf("alert_disk_free_percent: must be between 1 and 50")
	}
	if c.AlertCooldownMinutes < 1 || c.AlertCooldownMinutes > 1440 {
		return fmt.Errorf("alert_cooldown_minutes: must be between 1 and 1440")
	}
	if c.AlertTelegramBotToken != "" {
		id, secret, found := strings.Cut(c.AlertTelegramBotToken, ":")
		botID, err := strconv.ParseUint(id, 10, 64)
		if !found || err != nil || botID == 0 || len(secret) < 20 || len(secret) > 128 || !telegramIdentifier(secret) {
			return fmt.Errorf("alert_telegram_bot_token: invalid bot token")
		}
	}
	if c.AlertTelegramChatID != "" {
		id, err := strconv.ParseInt(c.AlertTelegramChatID, 10, 64)
		channel := strings.HasPrefix(c.AlertTelegramChatID, "@") && len(c.AlertTelegramChatID) >= 6 && len(c.AlertTelegramChatID) <= 33 && telegramIdentifier(c.AlertTelegramChatID[1:])
		if (err != nil || id == 0) && !channel {
			return fmt.Errorf("alert_telegram_chat_id: expected a nonzero chat ID or @channel username")
		}
	}
	if c.AlertTelegramEnabled && (c.AlertTelegramBotToken == "" || c.AlertTelegramChatID == "") {
		return fmt.Errorf("alert_telegram_enabled: bot token and chat ID are required")
	}
	return nil
}

func telegramIdentifier(value string) bool {
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
