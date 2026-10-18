// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package containersetting

import (
	"context"
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// GetByNodeID returns the container settings for a node, or a default empty configuration if none exists.
func (s *Service) GetByNodeID(ctx context.Context, nodeID string) (*ContainerSetting, error) {
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
func (s *Service) Save(ctx context.Context, nodeID string, sortOrder []string, locks map[string]bool) (*ContainerSetting, error) {
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
func (s *Service) IsContainerLocked(ctx context.Context, nodeID, containerName string) bool {
	locked, err := s.repo.IsContainerLocked(ctx, nodeID, containerName)
	if err != nil {
		return false
	}
	return locked
}
