// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
)

// ServiceMonitorRepository persists service monitors and health events.
type ServiceMonitorRepository struct {
	db *sqlx.DB
}

func NewServiceMonitorRepository(db *sqlx.DB) *ServiceMonitorRepository {
	return &ServiceMonitorRepository{db: db}
}

// ─── Service Monitors ────────────────────────────────────────────────────────

func (r *ServiceMonitorRepository) Create(m *apps.ServiceMonitor) (*apps.ServiceMonitor, error) {
	query := `
		INSERT INTO service_monitors (
			node_id, user_id, service_name, check_type,
			http_url, http_expected_status,
			tcp_host, tcp_port,
			interval_seconds, failure_threshold, notify_telegram,
			enabled, status, auto_heal_config, auto_heal_state
		) VALUES (
			:node_id, :user_id, :service_name, :check_type,
			:http_url, :http_expected_status,
			:tcp_host, :tcp_port,
			:interval_seconds, :failure_threshold, :notify_telegram,
			:enabled, :status, :auto_heal_config, :auto_heal_state
		)
		RETURNING *`

	rows, err := r.db.NamedQuery(query, m)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, errors.New("create service monitor returned no rows")
	}
	var result apps.ServiceMonitor
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ServiceMonitorRepository) Update(m *apps.ServiceMonitor) (*apps.ServiceMonitor, error) {
	query := `
		UPDATE service_monitors SET
			service_name        = :service_name,
			check_type          = :check_type,
			http_url            = :http_url,
			http_expected_status = :http_expected_status,
			tcp_host            = :tcp_host,
			tcp_port            = :tcp_port,
			interval_seconds    = :interval_seconds,
			failure_threshold   = :failure_threshold,
			notify_telegram     = :notify_telegram,
			enabled             = :enabled,
			auto_heal_config    = :auto_heal_config,
			auto_heal_state     = :auto_heal_state,
			updated_at          = NOW()
		WHERE id = :id
		RETURNING *`

	rows, err := r.db.NamedQuery(query, m)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, apps.ErrServiceMonitorNotFound
	}
	var result apps.ServiceMonitor
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ServiceMonitorRepository) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM service_monitors WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apps.ErrServiceMonitorNotFound
	}
	return nil
}

func (r *ServiceMonitorRepository) FindByID(id string) (*apps.ServiceMonitor, error) {
	var m apps.ServiceMonitor
	err := r.db.Get(&m, `SELECT * FROM service_monitors WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apps.ErrServiceMonitorNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *ServiceMonitorRepository) ListByNodeID(nodeID string) ([]*apps.ServiceMonitor, error) {
	var monitors []*apps.ServiceMonitor
	err := r.db.Select(&monitors, `
		SELECT m.*,
		       COALESCE(e.message, '') AS last_message,
		       COALESCE(e.latency_ms, 0) AS last_latency_ms
		FROM service_monitors m
		LEFT JOIN LATERAL (
			SELECT message, latency_ms FROM service_health_events
			WHERE monitor_id = m.id
			ORDER BY checked_at DESC
			LIMIT 1
		) e ON TRUE
		WHERE m.node_id = $1
		ORDER BY m.created_at ASC`,
		nodeID,
	)
	if err != nil {
		return nil, err
	}
	return monitors, nil
}

func (r *ServiceMonitorRepository) UpdateStatus(id, status string, consecutiveFailures int) error {
	now := time.Now()
	_, err := r.db.Exec(`
		UPDATE service_monitors
		SET status = $1, consecutive_failures = $2, last_checked_at = $3, updated_at = NOW()
		WHERE id = $4`,
		status, consecutiveFailures, now, id,
	)
	return err
}

func (r *ServiceMonitorRepository) UpdateAutoHealState(id string, state json.RawMessage) error {
	_, err := r.db.Exec(`
		UPDATE service_monitors
		SET auto_heal_state = $1, updated_at = NOW()
		WHERE id = $2`,
		state, id,
	)
	return err
}

// ─── Health Events ───────────────────────────────────────────────────────────

func (r *ServiceMonitorRepository) InsertEvent(e *apps.ServiceHealthEvent) error {
	_, err := r.db.NamedExec(`
		INSERT INTO service_health_events (monitor_id, node_id, user_id, status, latency_ms, message, checked_at)
		VALUES (:monitor_id, :node_id, :user_id, :status, :latency_ms, :message, :checked_at)`,
		e,
	)
	return err
}

func (r *ServiceMonitorRepository) ListEventsByMonitorID(monitorID string, limit int) ([]*apps.ServiceHealthEvent, error) {
	var events []*apps.ServiceHealthEvent
	err := r.db.Select(&events, `
		SELECT * FROM service_health_events
		WHERE monitor_id = $1
		ORDER BY checked_at DESC
		LIMIT $2`,
		monitorID, limit,
	)
	if err != nil {
		return nil, err
	}
	return events, nil
}

// purgeEventsBatch bounds each DELETE so the first purge of a large table does not hold locks for long.
const purgeEventsBatch = 10000

func (r *ServiceMonitorRepository) PurgeEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Auto-heal events are few (one per action); one statement is enough.
	res, err := r.db.ExecContext(ctx, `DELETE FROM service_autoheal_events WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	total, _ := res.RowsAffected()
	for {
		res, err := r.db.ExecContext(ctx, `
			DELETE FROM service_health_events
			WHERE id IN (
				SELECT id FROM service_health_events
				WHERE checked_at < $1
				LIMIT $2
			)`,
			cutoff, purgeEventsBatch,
		)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < purgeEventsBatch {
			return total, nil
		}
	}
}

func (r *ServiceMonitorRepository) InsertAutoHealEvent(e *apps.AutoHealEvent) error {
	_, err := r.db.NamedExec(`
		INSERT INTO service_autoheal_events
			(monitor_id, node_id, action, attempt, max_attempts, error, check_message, downtime_seconds, created_at)
		VALUES
			(:monitor_id, :node_id, :action, :attempt, :max_attempts, :error, :check_message, :downtime_seconds, :created_at)`,
		e,
	)
	return err
}

func (r *ServiceMonitorRepository) ListAutoHealEvents(monitorID string, from time.Time, limit int) ([]*apps.AutoHealEvent, error) {
	var events []*apps.AutoHealEvent
	err := r.db.Select(&events, `
		SELECT * FROM service_autoheal_events
		WHERE monitor_id = $1 AND created_at >= $2
		ORDER BY created_at DESC
		LIMIT $3`,
		monitorID, from, limit,
	)
	return events, err
}

func (r *ServiceMonitorRepository) UptimeHistory24h(monitorID string) ([]*apps.UptimePoint, error) {
	// Generate 5-minute buckets for the last 24 hours and join with health events.
	// Each bucket shows 'up' if all checks passed, 'down' if any failed, or 'no_data' if there are none.
	var points []*apps.UptimePoint
	err := r.db.Select(&points, `
		WITH buckets AS (
			SELECT generate_series(
				NOW() - INTERVAL '24 hours',
				NOW(),
				INTERVAL '5 minutes'
			) AS bucket
		),
		buckets_truncated AS (
			SELECT DISTINCT
				date_trunc('minute', bucket) - (EXTRACT(MINUTE FROM bucket)::int % 5) * INTERVAL '1 minute' AS bucket
			FROM buckets
		),
		agg AS (
			SELECT
				date_trunc('minute', checked_at) - (EXTRACT(MINUTE FROM checked_at)::int % 5) * INTERVAL '1 minute' AS bucket,
				CASE WHEN bool_and(status = 'up') THEN 'up'
				     ELSE 'down'
				END AS status,
				count(*) FILTER (WHERE status = 'up') AS up,
				count(*) FILTER (WHERE status <> 'up') AS down
			FROM service_health_events
			WHERE monitor_id = $1
			  AND checked_at >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		)
		SELECT
			to_char(b.bucket AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS bucket,
			COALESCE(a.status, 'no_data') AS status,
			COALESCE(a.up, 0) AS up,
			COALESCE(a.down, 0) AS down
		FROM buckets_truncated b
		LEFT JOIN agg a ON a.bucket = b.bucket
		ORDER BY b.bucket ASC`,
		monitorID,
	)
	if err != nil {
		return nil, err
	}
	return points, nil
}
