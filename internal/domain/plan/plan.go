// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package plan

// Usage captures current resource utilization for a user (informational only, no restrictions).
type Usage struct {
	NodesCount             int   `json:"nodes_count"`
	ProjectsCount          int   `json:"projects_count"`
	CustomCommandsCount    int   `json:"custom_commands_count"`
	BackupStorageBytes     int64 `json:"backup_storage_bytes"`
	AIDiagnosticsThisMonth int   `json:"ai_diagnostics_this_month"`
}

// Repository defines persistence operations for querying user resource usage.
type Repository interface {
	GetUserUsageCounts(userID string) (nodesCount, projectsCount, commandsCount int, backupStorageBytes int64, aiDiagnosticsThisMonth int, err error)
}
