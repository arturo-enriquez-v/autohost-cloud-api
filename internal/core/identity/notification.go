// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	PatternUserRegistered = "notifications:user_registered"
	PatternSystemAlert    = "notifications:system_alert"
)

type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityWarning  Severity = "WARNING"
	SeverityCritical Severity = "CRITICAL"
	SeveritySuccess  Severity = "SUCCESS"
)

// SystemAlert represents the payload sent to NestJS notifications:system_alert.
type SystemAlert struct {
	Recipient    string            `json:"recipient"`
	Severity     string            `json:"severity"`
	Service      string            `json:"service"`
	Title        string            `json:"title"`
	Timestamp    string            `json:"timestamp"`
	MarkdownLogs string            `json:"markdownLogs"`
	Metadata     map[string]string `json:"metadata"`
}

// UserRegisteredData represents the data in notifications:user_registered.
type UserRegisteredData struct {
	Recipient string `json:"recipient"`
	UserName  string `json:"userName"`
	LoginURL  string `json:"loginUrl,omitempty"`
}

type redisMessage struct {
	Pattern string `json:"pattern"`
	Data    any    `json:"data"`
}

// NotificationAlertItem represents an alert event enriched with node details for the notification bell.
type NotificationAlertItem struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	NodeName  string    `json:"node_name,omitempty"`
	AlertType string    `json:"alert_type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// NotificationLogItem represents an external log entry enriched with node details for the notification bell.
type NotificationLogItem struct {
	ID        string    `json:"id"`
	NodeID    *string   `json:"node_id,omitempty"`
	NodeName  string    `json:"node_name,omitempty"`
	Title     string    `json:"title"`
	Level     string    `json:"level"`
	Message   *string   `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NotificationSummary aggregates unread counts and recent events for global notifications and badges.
type NotificationSummary struct {
	UnreadAlertsCount int                      `json:"unread_alerts_count"`
	UnreadLogsCount   int                      `json:"unread_logs_count"`
	TotalUnread       int                      `json:"total_unread"`
	LastSeenAlertsAt  time.Time                `json:"last_seen_alerts_at"`
	LastSeenLogsAt    time.Time                `json:"last_seen_logs_at"`
	RecentAlerts      []*NotificationAlertItem `json:"recent_alerts"`
	RecentLogs        []*NotificationLogItem   `json:"recent_logs"`
}

// NotificationMarkerRepository defines methods to track user read markers and notification summaries.
type NotificationMarkerRepository interface {
	GetSummary(ctx context.Context, userID string) (*NotificationSummary, error)
	Acknowledge(ctx context.Context, userID, scope string) error
}

// NotificationService handles publishing notification events to Redis.
type NotificationService struct {
	redis *redis.Client
}

func NewNotificationService(redis *redis.Client) *NotificationService {
	return &NotificationService{redis: redis}
}

// SendSystemAlert publishes a system alert to the Redis notifications:system_alert topic.
func (s *NotificationService) SendSystemAlert(ctx context.Context, alert SystemAlert) error {
	if s == nil || s.redis == nil {
		return nil
	}

	if alert.Timestamp == "" {
		alert.Timestamp = time.Now().Format("2006-01-02 15:04:05")
	}

	msg := redisMessage{
		Pattern: PatternSystemAlert,
		Data:    alert,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal system alert: %w", err)
	}

	go func() {
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.redis.Publish(pubCtx, PatternSystemAlert, payload).Err(); err != nil {
			log.Printf("[ERROR] notification: publish %s to redis: %v", PatternSystemAlert, err)
		} else {
			log.Printf("[INFO] notification: published %s for service=%s recipient=%s severity=%s",
				PatternSystemAlert, alert.Service, alert.Recipient, alert.Severity)
		}
	}()

	return nil
}

// PublishUserRegistered publishes a welcome notification when a new user registers.
func (s *NotificationService) PublishUserRegistered(recipient, userName, loginURL string) {
	if s == nil || s.redis == nil {
		return
	}

	if loginURL == "" {
		frontendURL := os.Getenv("FRONTEND_URL")
		if frontendURL == "" {
			frontendURL = "http://localhost:3000"
		}
		loginURL = strings.TrimRight(frontendURL, "/") + "/login"
	}

	if strings.TrimSpace(userName) == "" {
		parts := strings.Split(recipient, "@")
		if len(parts) > 0 {
			userName = parts[0]
		}
	}

	msg := redisMessage{
		Pattern: PatternUserRegistered,
		Data: UserRegisteredData{
			Recipient: recipient,
			UserName:  userName,
			LoginURL:  loginURL,
		},
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ERROR] notification: failed to marshal user_registered event: %v", err)
		return
	}

	go func() {
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.redis.Publish(pubCtx, PatternUserRegistered, payload).Err(); err != nil {
			log.Printf("[ERROR] notification: failed to publish %s: %v", PatternUserRegistered, err)
		} else {
			log.Printf("[INFO] notification: published %s for %s", PatternUserRegistered, recipient)
		}
	}()
}
