// Package main: multi-channel notification dispatch.
//
// Sends alerts and operational messages to external channels: Slack, Discord,
// Telegram, and email. Each channel is configured once and then used by the
// alerting system or directly via the notify_send tool.
//
// This closes the gap between "alert triggered" and "human informed" —
// without notification channels, alerts only fire to a generic webhook.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ---- Types ----

type ChannelType string

const (
	ChannelSlack   ChannelType = "slack"
	ChannelDiscord ChannelType = "discord"
	ChannelTelegram ChannelType = "telegram"
	ChannelEmail   ChannelType = "email"
)

type NotificationChannel struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Type      ChannelType `json:"type"`
	WebhookURL string     `json:"webhook_url,omitempty"` // Slack/Discord
	BotToken   string     `json:"bot_token,omitempty"`   // Telegram
	ChatID     string     `json:"chat_id,omitempty"`     // Telegram
	EmailTo    string     `json:"email_to,omitempty"`    // Email
	EmailFrom  string     `json:"email_from,omitempty"`
	SMTPHost   string     `json:"smtp_host,omitempty"`
	SMTPPort   int         `json:"smtp_port,omitempty"`
	Enabled    bool        `json:"enabled"`
	CreatedAt  time.Time   `json:"created_at"`
}

type NotificationMessage struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Level   string `json:"level"` // info, warning, critical
}

type SendResult struct {
	ChannelID string `json:"channel_id"`
	Status    string `json:"status"` // sent, failed
	Error     string `json:"error,omitempty"`
	SentAt    time.Time `json:"sent_at"`
}

// ---- Manager ----

var notifyMgr *NotificationManager

type NotificationManager struct {
	mu       sync.Mutex
	channels map[string]*NotificationChannel
}

func newNotificationManager() *NotificationManager {
	nm := &NotificationManager{
		channels: make(map[string]*NotificationChannel),
	}
	nm.loadFromEnv()
	return nm
}

// loadFromEnv auto-configures channels from environment variables.
func (nm *NotificationManager) loadFromEnv() {
	// Slack
	if hook := os.Getenv("CUBE_SLACK_WEBHOOK"); hook != "" {
		ch := &NotificationChannel{
			ID:         "slack-default",
			Name:       "Slack (env)",
			Type:       ChannelSlack,
			WebhookURL: hook,
			Enabled:    true,
			CreatedAt:  time.Now().UTC(),
		}
		nm.channels[ch.ID] = ch
	}
	// Discord
	if hook := os.Getenv("CUBE_DISCORD_WEBHOOK"); hook != "" {
		ch := &NotificationChannel{
			ID:         "discord-default",
			Name:       "Discord (env)",
			Type:       ChannelDiscord,
			WebhookURL: hook,
			Enabled:    true,
			CreatedAt:  time.Now().UTC(),
		}
		nm.channels[ch.ID] = ch
	}
	// Telegram
	if token := os.Getenv("CUBE_TELEGRAM_TOKEN"); token != "" {
		chatID := os.Getenv("CUBE_TELEGRAM_CHAT_ID")
		if chatID != "" {
			ch := &NotificationChannel{
				ID:       "telegram-default",
				Name:     "Telegram (env)",
				Type:     ChannelTelegram,
				BotToken: token,
				ChatID:   chatID,
				Enabled:  true,
				CreatedAt: time.Now().UTC(),
			}
			nm.channels[ch.ID] = ch
		}
	}
}

// ---- Operations ----

func (nm *NotificationManager) AddChannel(ch *NotificationChannel) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if ch.Name == "" {
		return fmt.Errorf("channel name is required")
	}
	if err := validateChannelType(ch.Type); err != nil {
		return err
	}
	if ch.ID == "" {
		ch.ID = generateID("ch")
	}
	if ch.Type == ChannelSlack || ch.Type == ChannelDiscord {
		if ch.WebhookURL == "" {
			return fmt.Errorf("webhook_url is required for %s channels", ch.Type)
		}
		if err := validateWebhookURL(ch.WebhookURL); err != nil {
			return fmt.Errorf("invalid webhook URL: %w", err)
		}
	}
	if ch.Type == ChannelTelegram {
		if ch.BotToken == "" || ch.ChatID == "" {
			return fmt.Errorf("bot_token and chat_id are required for telegram channels")
		}
	}
	if ch.Type == ChannelEmail {
		if ch.EmailTo == "" || ch.SMTPHost == "" {
			return fmt.Errorf("email_to and smtp_host are required for email channels")
		}
	}

	ch.CreatedAt = time.Now().UTC()
	nm.channels[ch.ID] = ch
	return nil
}

func (nm *NotificationManager) ListChannels() []NotificationChannel {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	result := make([]NotificationChannel, 0, len(nm.channels))
	for _, ch := range nm.channels {
		// Don't expose secrets in list
		safe := *ch
		safe.BotToken = ""
		result = append(result, safe)
	}
	return result
}

func (nm *NotificationManager) RemoveChannel(id string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if _, ok := nm.channels[id]; !ok {
		return fmt.Errorf("channel '%s' not found", id)
	}
	delete(nm.channels, id)
	return nil
}

func (nm *NotificationManager) Send(channelID string, msg NotificationMessage) (*SendResult, error) {
	nm.mu.Lock()
	ch, ok := nm.channels[channelID]
	nm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("channel '%s' not found", channelID)
	}
	if !ch.Enabled {
		return nil, fmt.Errorf("channel '%s' is disabled", channelID)
	}

	result := &SendResult{
		ChannelID: channelID,
		SentAt:    time.Now().UTC(),
	}

	var err error
	switch ch.Type {
	case ChannelSlack, ChannelDiscord:
		err = nm.sendWebhook(ch, msg)
	case ChannelTelegram:
		err = nm.sendTelegram(ch, msg)
	case ChannelEmail:
		err = nm.sendEmail(ch, msg)
	default:
		err = fmt.Errorf("unsupported channel type: %s", ch.Type)
	}

	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	} else {
		result.Status = "sent"
	}
	return result, nil
}

// ---- Dispatchers ----

func (nm *NotificationManager) sendWebhook(ch *NotificationChannel, msg NotificationMessage) error {
	payload := map[string]interface{}{
		"text": fmt.Sprintf("*%s*\n%s", msg.Title, msg.Body),
	}
	if ch.Type == ChannelDiscord {
		payload = map[string]interface{}{
			"content": fmt.Sprintf("**%s**\n%s", msg.Title, msg.Body),
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(ch.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (nm *NotificationManager) sendTelegram(ch *NotificationChannel, msg NotificationMessage) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", ch.BotToken)

	payload := map[string]interface{}{
		"chat_id": ch.ChatID,
		"text":    fmt.Sprintf("*%s*\n%s", msg.Title, msg.Body),
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return fmt.Errorf("telegram API returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (nm *NotificationManager) sendEmail(ch *NotificationChannel, msg NotificationMessage) error {
	// Email sending requires SMTP — we build the message but delegate to system mailer
	// or an external SMTP relay. This is a lightweight implementation.
	subject := fmt.Sprintf("[Cube] %s", msg.Title)
	body := fmt.Sprintf("Subject: %s\n\n%s", subject, msg.Body)

	_ = body // Would use net/smtp.SendMail here
	return fmt.Errorf("email sending requires SMTP configuration (host=%s port=%d) — not yet implemented", ch.SMTPHost, ch.SMTPPort)
}

// ---- Helpers ----

func validateChannelType(t ChannelType) error {
	switch t {
	case ChannelSlack, ChannelDiscord, ChannelTelegram, ChannelEmail:
		return nil
	}
	return fmt.Errorf("invalid channel type: %s (must be slack, discord, telegram, or email)", t)
}

// urlExists is a helper to validate URLs have proper format.
func urlExists(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme != "" && parsed.Host != ""
}

var _ = urlExists // used in future tests
var _ = strings.TrimSpace
