// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package plan

import "fmt"

// Service handles querying resource usage metrics for a user.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// GetUsage calculates current resource utilization figures for a user.
func (s *Service) GetUsage(userID string) (*Usage, error) {
	nodes, projects, commands, backupBytes, aiDiagCount, err := s.repo.GetUserUsageCounts(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get usage counts: %w", err)
	}

	return &Usage{
		NodesCount:             nodes,
		ProjectsCount:          projects,
		CustomCommandsCount:    commands,
		BackupStorageBytes:     backupBytes,
		AIDiagnosticsThisMonth: aiDiagCount,
	}, nil
}
