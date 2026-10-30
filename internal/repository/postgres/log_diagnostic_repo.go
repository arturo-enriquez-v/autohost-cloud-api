// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/jmoiron/sqlx"
)

// LogDiagnosticRepository implements node.Repository with PostgreSQL.
type LogDiagnosticRepository struct {
	db *sqlx.DB
}

// NewLogDiagnosticRepository creates a new repository backed by sqlx.
func NewLogDiagnosticRepository(db *sqlx.DB) *LogDiagnosticRepository {
	return &LogDiagnosticRepository{db: db}
}

func (r *LogDiagnosticRepository) Create(ctx context.Context, d *node.LogDiagnostic) (*node.LogDiagnostic, error) {
	var result node.LogDiagnostic
	query := `
		INSERT INTO log_diagnostics (user_id, node_id, request_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, node_id, request_id, status, diagnostic_json, sources, system_health, created_at, approved_at`

	err := r.db.QueryRowxContext(ctx, query,
		d.UserID, d.NodeID, d.RequestID, d.Status, d.CreatedAt,
	).StructScan(&result)
	if err != nil {
		return nil, fmt.Errorf("failed to create log diagnostic: %w", err)
	}
	return &result, nil
}

func (r *LogDiagnosticRepository) GetByID(ctx context.Context, userID, id string) (*node.LogDiagnostic, error) {
	var d node.LogDiagnostic
	query := `
		SELECT id, user_id, node_id, request_id, status, diagnostic_json, sources, system_health, created_at, approved_at
		FROM log_diagnostics
		WHERE id = $1 AND user_id = $2`

	err := r.db.GetContext(ctx, &d, query, id, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("diagnostic not found")
		}
		return nil, fmt.Errorf("failed to get log diagnostic: %w", err)
	}
	return &d, nil
}

func (r *LogDiagnosticRepository) GetByRequestID(ctx context.Context, requestID string) (*node.LogDiagnostic, error) {
	var d node.LogDiagnostic
	query := `
		SELECT id, user_id, node_id, request_id, status, diagnostic_json, sources, system_health, created_at, approved_at
		FROM log_diagnostics
		WHERE request_id = $1`

	err := r.db.GetContext(ctx, &d, query, requestID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("diagnostic not found for request_id %s", requestID)
		}
		return nil, fmt.Errorf("failed to get log diagnostic by request_id: %w", err)
	}
	return &d, nil
}

func (r *LogDiagnosticRepository) UpdateStatus(ctx context.Context, id, status string) error {
	query := `UPDATE log_diagnostics SET status = $1 WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, status, id)
	return err
}

func (r *LogDiagnosticRepository) UpdatePreview(ctx context.Context, requestID string, diagnosticJSON, sources node.JSONRawMessage, systemHealth string) error {
	query := `
		UPDATE log_diagnostics
		SET status = 'preview', diagnostic_json = $1, sources = $2, system_health = $3
		WHERE request_id = $4 AND status = 'pending'`

	result, err := r.db.ExecContext(ctx, query, diagnosticJSON, sources, systemHealth, requestID)
	if err != nil {
		return fmt.Errorf("failed to update preview: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no pending diagnostic found for request_id %s", requestID)
	}
	return nil
}

func (r *LogDiagnosticRepository) Approve(ctx context.Context, userID, id string) error {
	query := `
		UPDATE log_diagnostics
		SET status = 'approved', approved_at = $1
		WHERE id = $2 AND user_id = $3 AND status = 'preview'`

	result, err := r.db.ExecContext(ctx, query, time.Now().UTC(), id, userID)
	if err != nil {
		return fmt.Errorf("failed to approve diagnostic: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no preview diagnostic found to approve")
	}
	return nil
}

func (r *LogDiagnosticRepository) Reject(ctx context.Context, userID, id string) error {
	query := `
		UPDATE log_diagnostics
		SET status = 'rejected'
		WHERE id = $1 AND user_id = $2 AND status = 'preview'`

	result, err := r.db.ExecContext(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("failed to reject diagnostic: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no preview diagnostic found to reject")
	}
	return nil
}

func (r *LogDiagnosticRepository) List(ctx context.Context, userID string, filter node.ListFilter, limit, offset int) ([]*node.LogDiagnostic, int, error) {
	var conditions []string
	var args []interface{}
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("user_id = $%d", argIdx))
	args = append(args, userID)
	argIdx++

	if filter.NodeID != nil {
		conditions = append(conditions, fmt.Sprintf("node_id = $%d", argIdx))
		args = append(args, *filter.NodeID)
		argIdx++
	}
	if filter.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *filter.Status)
		argIdx++
	}
	if filter.SystemHealth != nil {
		conditions = append(conditions, fmt.Sprintf("system_health = $%d", argIdx))
		args = append(args, *filter.SystemHealth)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM log_diagnostics WHERE %s", where)
	var total int
	if err := r.db.GetContext(ctx, &total, countQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("failed to count diagnostics: %w", err)
	}

	// Fetch page
	dataQuery := fmt.Sprintf(`
		SELECT id, user_id, node_id, request_id, status, diagnostic_json, sources, system_health, created_at, approved_at
		FROM log_diagnostics
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	args = append(args, limit, offset)

	var diagnostics []*node.LogDiagnostic
	if err := r.db.SelectContext(ctx, &diagnostics, dataQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("failed to list diagnostics: %w", err)
	}

	return diagnostics, total, nil
}

func (r *LogDiagnosticRepository) Delete(ctx context.Context, userID, id string) error {
	query := `DELETE FROM log_diagnostics WHERE id = $1 AND user_id = $2`
	result, err := r.db.ExecContext(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("failed to delete diagnostic: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("diagnostic not found")
	}
	return nil
}

// CleanupExpired removes:
// - pending/rejected diagnostics older than 48 hours
// - approved diagnostics older than 30 days
func (r *LogDiagnosticRepository) CleanupExpired(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM log_diagnostics
		WHERE (status IN ('pending', 'rejected') AND created_at < NOW() - INTERVAL '2 days')
		   OR (status = 'approved' AND created_at < NOW() - INTERVAL '30 days')`

	result, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired diagnostics: %w", err)
	}
	return result.RowsAffected()
}
