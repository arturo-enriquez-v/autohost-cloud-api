// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// telegramAPIBase is the Telegram Bot API base URL.
const telegramAPIBase = "https://api.telegram.org"

var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}

// SendTelegramMessage sends a plain-text message via the Telegram Bot API.
// botToken is the token from @BotFather; chatID is the target chat or user ID.
func SendTelegramMessage(botToken, chatID, message string) error {
	if botToken == "" || chatID == "" {
		return fmt.Errorf("telegram: bot token and chat ID must not be empty")
	}

	payload := map[string]string{
		"chat_id":    chatID,
		"text":       message,
		"parse_mode": "HTML",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal payload: %w", err)
	}

	// Normalize: strip leading "bot" prefix if the user stored it with one,
	// since the URL already adds "/bot" before the token.
	token := strings.TrimPrefix(botToken, "bot")

	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
	resp, err := telegramHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: unexpected status %d", resp.StatusCode)
	}
	return nil
}
