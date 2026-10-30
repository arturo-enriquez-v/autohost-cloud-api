// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"github.com/jmoiron/sqlx"
)

// PlanRepository implements plan.Repository using PostgreSQL for resource usage queries.
type PlanRepository struct {
	db *sqlx.DB
}

func NewPlanRepository(db *sqlx.DB) *PlanRepository {
	return &PlanRepository{db: db}
}

func (r *PlanRepository) GetUserUsageCounts(userID string) (nodesCount, projectsCount, commandsCount int, backupStorageBytes int64, aiDiagnosticsThisMonth int, err error) {
	_ = r.db.Get(&nodesCount, `SELECT COUNT(*) FROM nodes WHERE owner_id = $1`, userID)
	_ = r.db.Get(&projectsCount, `SELECT COUNT(*) FROM projects WHERE user_id = $1`, userID)
	_ = r.db.Get(&commandsCount, `SELECT COUNT(*) FROM node_commands WHERE user_id = $1`, userID)
	_ = r.db.Get(&backupStorageBytes, `SELECT COALESCE(SUM(file_size_bytes), 0) FROM backup_records WHERE user_id = $1 AND status = 'completed'`, userID)
	_ = r.db.Get(&aiDiagnosticsThisMonth, `SELECT COUNT(*) FROM log_diagnostics WHERE user_id = $1 AND created_at >= date_trunc('month', now())`, userID)
	return nodesCount, projectsCount, commandsCount, backupStorageBytes, aiDiagnosticsThisMonth, nil
}
