// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"context"
	"time"
)

// ContainerSetting holds display order and lock status for containers on a node.
type ContainerSetting struct {
	ID               string          `db:"id" json:"id"`
	NodeID           string          `db:"node_id" json:"node_id"`
	SortOrder        []string        `db:"sort_order" json:"sort_order"`
	LockedContainers map[string]bool `db:"locked_containers" json:"locked_containers"`
	CreatedAt        time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time       `db:"updated_at" json:"updated_at"`
}

// ContainerSettingRepository defines persistence operations for container settings.
type ContainerSettingRepository interface {
	GetByNodeID(ctx context.Context, nodeID string) (*ContainerSetting, error)
	Upsert(ctx context.Context, setting *ContainerSetting) (*ContainerSetting, error)
	IsContainerLocked(ctx context.Context, nodeID, containerName string) (bool, error)
}

type ContainerSettingService struct {
	repo ContainerSettingRepository
}

func NewContainerSettingService(repo ContainerSettingRepository) *ContainerSettingService {
	return &ContainerSettingService{repo: repo}
}

// GetByNodeID returns the container settings for a node, or a default empty configuration if none exists.
func (s *ContainerSettingService) GetByNodeID(ctx context.Context, nodeID string) (*ContainerSetting, error) {
	setting, err := s.repo.GetByNodeID(ctx, nodeID)
	if err != nil {
		return &ContainerSetting{
			NodeID:           nodeID,
			SortOrder:        []string{},
			LockedContainers: make(map[string]bool),
		}, nil
	}
	if setting.SortOrder == nil {
		setting.SortOrder = []string{}
	}
	if setting.LockedContainers == nil {
		setting.LockedContainers = make(map[string]bool)
	}
	return setting, nil
}

// Save creates or updates the container settings for a node.
func (s *ContainerSettingService) Save(ctx context.Context, nodeID string, sortOrder []string, locks map[string]bool) (*ContainerSetting, error) {
	if sortOrder == nil {
		sortOrder = []string{}
	}
	if locks == nil {
		locks = make(map[string]bool)
	}
	setting := &ContainerSetting{
		NodeID:           nodeID,
		SortOrder:        sortOrder,
		LockedContainers: locks,
	}
	return s.repo.Upsert(ctx, setting)
}

// IsContainerLocked checks if actions on the specified container are locked.
func (s *ContainerSettingService) IsContainerLocked(ctx context.Context, nodeID, containerName string) bool {
	locked, err := s.repo.IsContainerLocked(ctx, nodeID, containerName)
	if err != nil {
		return false
	}
	return locked
}
