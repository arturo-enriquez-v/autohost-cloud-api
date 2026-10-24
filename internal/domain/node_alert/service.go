// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package nodealert

import "errors"

// ErrNotFound is returned when no alert config exists for the given node.
var ErrNotFound = errors.New("node alert config not found")

// Service wraps the repository with business logic.
type Service struct {
	repo Repository
}

// NewService creates a new alert config service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Upsert creates or updates the alert configuration for a node.
func (s *Service) Upsert(alert *NodeAlert) (*NodeAlert, error) {
	return s.repo.Upsert(alert)
}

// FindByNodeID returns the alert config for a node, or ErrNotFound.
func (s *Service) FindByNodeID(nodeID string) (*NodeAlert, error) {
	return s.repo.FindByNodeID(nodeID)
}

// FindAllByOwnerID returns all alert configs for nodes owned by ownerID.
func (s *Service) FindAllByOwnerID(ownerID string) ([]*NodeAlert, error) {
	return s.repo.FindAllByOwnerID(ownerID)
}
