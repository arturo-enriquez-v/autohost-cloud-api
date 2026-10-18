// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package alertevent

import "time"

// AlertFilter narrows the events returned by ListFiltered.
type AlertFilter struct {
	NodeID string     // if non-empty, only events for this node
	From   *time.Time // if non-nil, events at or after this time
	To     *time.Time // if non-nil, events at or before this time
}

// AlertEventType identifies what triggered the alert.
type AlertEventType string

const (
	AlertEventTypeOffline AlertEventType = "offline"
	AlertEventTypeCPU     AlertEventType = "cpu"
	AlertEventTypeRAM     AlertEventType = "ram"
	AlertEventTypeDisk    AlertEventType = "disk"
)

// AlertEvent is a persisted record of a fired alert notification.
type AlertEvent struct {
	ID        string         `db:"id"         json:"id"`
	UserID    *string        `db:"user_id"    json:"user_id"`
	NodeID    string         `db:"node_id"    json:"node_id"`
	AlertType AlertEventType `db:"alert_type" json:"alert_type"`
	Message   string         `db:"message"    json:"message"`
	CreatedAt time.Time      `db:"created_at" json:"created_at"`
}

// Repository defines the persistence interface for alert events.
type Repository interface {
	Create(event *AlertEvent) (*AlertEvent, error)
	ListByUserID(userID string, limit, offset int) ([]*AlertEvent, error)
	// ListFiltered returns events matching the filter plus the total matching count.
	ListFiltered(userID string, filter AlertFilter, limit, offset int) ([]*AlertEvent, int, error)
	// CountDistinctNodesWithRecentAlerts returns the number of distinct nodes
	// that fired at least one alert event since `since`.
	CountDistinctNodesWithRecentAlerts(userID string, since time.Time) (int, error)
	// ListNodeAlertSummaryByUserID returns per-node alert count and distinct
	// alert types for events fired since `since`.
	ListNodeAlertSummaryByUserID(userID string, since time.Time) ([]*NodeAlertSummary, error)
}

// NodeAlertSummary holds aggregated alert info for a single node.
type NodeAlertSummary struct {
	NodeID string
	Count  int
	Types  []AlertEventType
}
