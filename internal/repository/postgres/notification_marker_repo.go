// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/notification"
	"github.com/jmoiron/sqlx"
)

type NotificationMarkerRepo struct {
	db *sqlx.DB
}

func NewNotificationMarkerRepository(db *sqlx.DB) *NotificationMarkerRepo {
	return &NotificationMarkerRepo{db: db}
}

func (r *NotificationMarkerRepo) GetSummary(ctx context.Context, userID string) (*notification.NotificationSummary, error) {
	// 1. Get marker timestamps or fallback to user created_at
	var markers struct {
		LastSeenAlerts time.Time `db:"last_seen_alerts"`
		LastSeenLogs   time.Time `db:"last_seen_logs"`
	}

	markerQuery := `
		SELECT
			COALESCE(m.last_seen_alerts_at, u.created_at, NOW()) AS last_seen_alerts,
			COALESCE(m.last_seen_logs_at, u.created_at, NOW())   AS last_seen_logs
		FROM users u
		LEFT JOIN user_notification_markers m ON m.user_id = u.id
		WHERE u.id = $1
	`
	if err := r.db.GetContext(ctx, &markers, markerQuery, userID); err != nil {
		if err == sql.ErrNoRows {
			now := time.Now().UTC()
			markers.LastSeenAlerts = now
			markers.LastSeenLogs = now
		} else {
			return nil, fmt.Errorf("get notification markers: %w", err)
		}
	}

	// 2. Count unread alerts
	var unreadAlerts int
	alertsCountQuery := `
		SELECT COUNT(*)
		FROM alert_events
		WHERE user_id = $1 AND created_at > $2
	`
	if err := r.db.GetContext(ctx, &unreadAlerts, alertsCountQuery, userID, markers.LastSeenAlerts); err != nil {
		return nil, fmt.Errorf("count unread alerts: %w", err)
	}

	// 3. Count unread logs
	var unreadLogs int
	logsCountQuery := `
		SELECT COUNT(*)
		FROM external_logs
		WHERE user_id = $1 AND created_at > $2
	`
	if err := r.db.GetContext(ctx, &unreadLogs, logsCountQuery, userID, markers.LastSeenLogs); err != nil {
		return nil, fmt.Errorf("count unread logs: %w", err)
	}

	// 4. Fetch recent alerts (last 10)
	type alertRow struct {
		ID        string    `db:"id"`
		NodeID    string    `db:"node_id"`
		NodeName  string    `db:"node_name"`
		AlertType string    `db:"alert_type"`
		Message   string    `db:"message"`
		CreatedAt time.Time `db:"created_at"`
	}
	var alertRows []alertRow
	recentAlertsQuery := `
		SELECT
			a.id,
			a.node_id,
			COALESCE(n.name, n.hostname, '') AS node_name,
			a.alert_type,
			a.message,
			a.created_at
		FROM alert_events a
		LEFT JOIN nodes n ON n.id = a.node_id
		WHERE a.user_id = $1
		ORDER BY a.created_at DESC
		LIMIT 10
	`
	if err := r.db.SelectContext(ctx, &alertRows, recentAlertsQuery, userID); err != nil {
		return nil, fmt.Errorf("select recent alerts: %w", err)
	}

	recentAlerts := make([]*notification.NotificationAlertItem, len(alertRows))
	for i, a := range alertRows {
		recentAlerts[i] = &notification.NotificationAlertItem{
			ID:        a.ID,
			NodeID:    a.NodeID,
			NodeName:  a.NodeName,
			AlertType: a.AlertType,
			Message:   a.Message,
			CreatedAt: a.CreatedAt,
		}
	}

	// 5. Fetch recent logs (last 10)
	type logRow struct {
		ID        string    `db:"id"`
		NodeID    *string   `db:"node_id"`
		NodeName  string    `db:"node_name"`
		Title     string    `db:"title"`
		Level     string    `db:"level"`
		Message   *string   `db:"message"`
		CreatedAt time.Time `db:"created_at"`
	}
	var logRows []logRow
	recentLogsQuery := `
		SELECT
			l.id,
			l.node_id,
			COALESCE(n.name, n.hostname, '') AS node_name,
			l.title,
			l.level,
			l.message,
			l.created_at
		FROM external_logs l
		LEFT JOIN nodes n ON n.id = l.node_id
		WHERE l.user_id = $1
		ORDER BY l.created_at DESC
		LIMIT 10
	`
	if err := r.db.SelectContext(ctx, &logRows, recentLogsQuery, userID); err != nil {
		return nil, fmt.Errorf("select recent logs: %w", err)
	}

	recentLogs := make([]*notification.NotificationLogItem, len(logRows))
	for i, l := range logRows {
		recentLogs[i] = &notification.NotificationLogItem{
			ID:        l.ID,
			NodeID:    l.NodeID,
			NodeName:  l.NodeName,
			Title:     l.Title,
			Level:     l.Level,
			Message:   l.Message,
			CreatedAt: l.CreatedAt,
		}
	}

	return &notification.NotificationSummary{
		UnreadAlertsCount: unreadAlerts,
		UnreadLogsCount:   unreadLogs,
		TotalUnread:       unreadAlerts + unreadLogs,
		LastSeenAlertsAt:  markers.LastSeenAlerts,
		LastSeenLogsAt:    markers.LastSeenLogs,
		RecentAlerts:      recentAlerts,
		RecentLogs:        recentLogs,
	}, nil
}

func (r *NotificationMarkerRepo) Acknowledge(ctx context.Context, userID, scope string) error {
	query := `
		INSERT INTO user_notification_markers (user_id, last_seen_alerts_at, last_seen_logs_at, updated_at)
		VALUES (
			$1,
			CASE WHEN $2 IN ('alerts', 'all') THEN NOW() ELSE COALESCE((SELECT created_at FROM users WHERE id = $1), NOW()) END,
			CASE WHEN $2 IN ('logs', 'all') THEN NOW() ELSE COALESCE((SELECT created_at FROM users WHERE id = $1), NOW()) END,
			NOW()
		)
		ON CONFLICT (user_id) DO UPDATE
		SET last_seen_alerts_at = CASE WHEN $2 IN ('alerts', 'all') THEN NOW() ELSE user_notification_markers.last_seen_alerts_at END,
		    last_seen_logs_at   = CASE WHEN $2 IN ('logs', 'all') THEN NOW() ELSE user_notification_markers.last_seen_logs_at END,
		    updated_at          = NOW();
	`
	_, err := r.db.ExecContext(ctx, query, userID, scope)
	if err != nil {
		return fmt.Errorf("acknowledge notifications: %w", err)
	}
	return nil
}
