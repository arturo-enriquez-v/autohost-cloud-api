// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// AlertEventRepository implements node.Repository using PostgreSQL.
type AlertEventRepository struct {
	db *sqlx.DB
}

func NewAlertEventRepository(db *sqlx.DB) *AlertEventRepository {
	return &AlertEventRepository{db: db}
}

// Create inserts a new alert event and returns the persisted record.
func (r *AlertEventRepository) Create(event *node.AlertEvent) (*node.AlertEvent, error) {
	var result node.AlertEvent
	err := r.db.QueryRowxContext(context.Background(), `
		INSERT INTO alert_events (user_id, node_id, alert_type, message)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, node_id, alert_type, message, created_at`,
		event.UserID, event.NodeID, event.AlertType, event.Message,
	).StructScan(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// CountDistinctNodesWithRecentAlerts returns the number of distinct node IDs
// that fired at least one alert since `since` for the given user.
func (r *AlertEventRepository) CountDistinctNodesWithRecentAlerts(userID string, since time.Time) (int, error) {
	var count int
	err := r.db.QueryRowxContext(context.Background(), `
		SELECT COUNT(DISTINCT node_id)
		FROM alert_events
		WHERE user_id = $1 AND created_at >= $2`,
		userID, since,
	).Scan(&count)
	return count, err
}

// ListNodeAlertSummaryByUserID returns per-node alert count and distinct types
// for events fired since `since`.
func (r *AlertEventRepository) ListNodeAlertSummaryByUserID(userID string, since time.Time) ([]*node.NodeAlertSummary, error) {
	rows, err := r.db.QueryxContext(context.Background(), `
		SELECT node_id, COUNT(*) AS cnt, ARRAY_AGG(DISTINCT alert_type) AS types
		FROM alert_events
		WHERE user_id = $1 AND created_at >= $2
		GROUP BY node_id`,
		userID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*node.NodeAlertSummary
	for rows.Next() {
		var nodeID string
		var cnt int
		var rawTypes []string
		if err := rows.Scan(&nodeID, &cnt, pq.Array(&rawTypes)); err != nil {
			return nil, err
		}
		types := make([]node.AlertEventType, len(rawTypes))
		for i, t := range rawTypes {
			types[i] = node.AlertEventType(t)
		}
		result = append(result, &node.NodeAlertSummary{
			NodeID: nodeID,
			Count:  cnt,
			Types:  types,
		})
	}
	return result, nil
}

// ListByUserID returns alert events for a user ordered by most recent first.
func (r *AlertEventRepository) ListByUserID(userID string, limit, offset int) ([]*node.AlertEvent, error) {
	var events []*node.AlertEvent
	err := r.db.SelectContext(context.Background(), &events, `
		SELECT id, user_id, node_id, alert_type, message, created_at
		FROM alert_events
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []*node.AlertEvent{}
	}
	return events, nil
}

// ListFiltered returns alert events matching the filter plus the total matching count.
func (r *AlertEventRepository) ListFiltered(userID string, filter node.AlertFilter, limit, offset int) ([]*node.AlertEvent, int, error) {
	conds := []string{"user_id = $1"}
	args := []interface{}{userID}
	n := 2

	if filter.NodeID != "" {
		conds = append(conds, fmt.Sprintf("node_id = $%d", n))
		args = append(args, filter.NodeID)
		n++
	}
	if filter.From != nil {
		conds = append(conds, fmt.Sprintf("created_at >= $%d", n))
		args = append(args, *filter.From)
		n++
	}
	if filter.To != nil {
		conds = append(conds, fmt.Sprintf("created_at <= $%d", n))
		args = append(args, *filter.To)
		n++
	}

	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := r.db.QueryRowxContext(context.Background(),
		"SELECT COUNT(*) FROM alert_events "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataArgs := make([]interface{}, len(args)+2)
	copy(dataArgs, args)
	dataArgs[len(args)] = limit
	dataArgs[len(args)+1] = offset
	query := fmt.Sprintf(`
		SELECT id, user_id, node_id, alert_type, message, created_at
		FROM alert_events %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, n, n+1)

	var events []*node.AlertEvent
	if err := r.db.SelectContext(context.Background(), &events, query, dataArgs...); err != nil {
		return nil, 0, err
	}
	if events == nil {
		events = []*node.AlertEvent{}
	}
	return events, total, nil
}
