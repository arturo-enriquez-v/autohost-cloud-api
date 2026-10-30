// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

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

// NodeAlertSummary holds aggregated alert info for a single node.
type NodeAlertSummary struct {
	NodeID string
	Count  int
	Types  []AlertEventType
}

type AlertEventRepository interface {
	Create(event *AlertEvent) (*AlertEvent, error)
	ListByUserID(userID string, limit, offset int) ([]*AlertEvent, error)
	ListFiltered(userID string, filter AlertFilter, limit, offset int) ([]*AlertEvent, int, error)
	CountDistinctNodesWithRecentAlerts(userID string, since time.Time) (int, error)
	ListNodeAlertSummaryByUserID(userID string, since time.Time) ([]*NodeAlertSummary, error)
}

type AlertEventService struct {
	repo AlertEventRepository
}

func NewAlertEventService(repo AlertEventRepository) *AlertEventService {
	return &AlertEventService{repo: repo}
}

func (s *AlertEventService) Create(event *AlertEvent) (*AlertEvent, error) {
	return s.repo.Create(event)
}

func (s *AlertEventService) ListByUserID(userID string, limit, offset int) ([]*AlertEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListByUserID(userID, limit, offset)
}

func (s *AlertEventService) CountDistinctNodesWithRecentAlerts(userID string, since time.Time) (int, error) {
	return s.repo.CountDistinctNodesWithRecentAlerts(userID, since)
}

func (s *AlertEventService) ListNodeAlertSummaryByUserID(userID string, since time.Time) ([]*NodeAlertSummary, error) {
	return s.repo.ListNodeAlertSummaryByUserID(userID, since)
}

func (s *AlertEventService) ListFiltered(userID string, filter AlertFilter, limit, offset int) ([]*AlertEvent, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	return s.repo.ListFiltered(userID, filter, limit, offset)
}
