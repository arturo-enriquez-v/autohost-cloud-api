// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import "fmt"

// Usage captures current resource utilization for a user (informational only, no restrictions).
type Usage struct {
	NodesCount             int   `json:"nodes_count"`
	ProjectsCount          int   `json:"projects_count"`
	CustomCommandsCount    int   `json:"custom_commands_count"`
	BackupStorageBytes     int64 `json:"backup_storage_bytes"`
	AIDiagnosticsThisMonth int   `json:"ai_diagnostics_this_month"`
}

// PlanRepository defines persistence operations for querying user resource usage.
type PlanRepository interface {
	GetUserUsageCounts(userID string) (nodesCount, projectsCount, commandsCount int, backupStorageBytes int64, aiDiagnosticsThisMonth int, err error)
}

// PlanService handles querying resource usage metrics for a user.
type PlanService struct {
	repo PlanRepository
}

func NewPlanService(repo PlanRepository) *PlanService {
	return &PlanService{repo: repo}
}

// GetUsage calculates current resource utilization figures for a user.
func (s *PlanService) GetUsage(userID string) (*Usage, error) {
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
