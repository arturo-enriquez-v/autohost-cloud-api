// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"errors"
	"time"
)

// ErrNodeAlertNotFound is returned when no alert config exists for the given node.
var ErrNodeAlertNotFound = errors.New("node alert config not found")

// NodeAlert stores the alert configuration for a specific node.
type NodeAlert struct {
	ID               string    `db:"id"                 json:"id"`
	NodeID           string    `db:"node_id"            json:"node_id"`
	TelegramBotID    *string   `db:"telegram_bot_id"    json:"telegram_bot_id"`
	TelegramBotToken string    `db:"telegram_bot_token" json:"telegram_bot_token"`
	TelegramChatID   string    `db:"telegram_chat_id"   json:"telegram_chat_id"`
	CPUThreshold     *float32  `db:"cpu_threshold"      json:"cpu_threshold"`
	RAMThreshold     *float32  `db:"ram_threshold"      json:"ram_threshold"`
	DiskThreshold    *float32  `db:"disk_threshold"     json:"disk_threshold"`
	AlertOnOffline   bool      `db:"alert_on_offline"   json:"alert_on_offline"`
	CreatedAt        time.Time `db:"created_at"         json:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"         json:"updated_at"`
}

type NodeAlertRepository interface {
	Upsert(alert *NodeAlert) (*NodeAlert, error)
	FindByNodeID(nodeID string) (*NodeAlert, error)
	FindAllByOwnerID(ownerID string) ([]*NodeAlert, error)
}

type NodeAlertService struct {
	repo NodeAlertRepository
}

func NewNodeAlertService(repo NodeAlertRepository) *NodeAlertService {
	return &NodeAlertService{repo: repo}
}

func (s *NodeAlertService) Upsert(alert *NodeAlert) (*NodeAlert, error) {
	return s.repo.Upsert(alert)
}

func (s *NodeAlertService) FindByNodeID(nodeID string) (*NodeAlert, error) {
	return s.repo.FindByNodeID(nodeID)
}

func (s *NodeAlertService) FindAllByOwnerID(ownerID string) ([]*NodeAlert, error) {
	return s.repo.FindAllByOwnerID(ownerID)
}
