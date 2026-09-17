package notification

import (
	"context"
	"time"
)

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

// MarkerRepository defines methods to track user read markers and notification summaries.
type MarkerRepository interface {
	GetSummary(ctx context.Context, userID string) (*NotificationSummary, error)
	Acknowledge(ctx context.Context, userID, scope string) error
}
