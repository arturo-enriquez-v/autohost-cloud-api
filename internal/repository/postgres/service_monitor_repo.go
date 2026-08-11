package postgres

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	sm "github.com/arturo/autohost-cloud-api/internal/domain/service_monitor"
)

// ServiceMonitorRepository persists service monitors and health events.
type ServiceMonitorRepository struct {
	db *sqlx.DB
}

func NewServiceMonitorRepository(db *sqlx.DB) *ServiceMonitorRepository {
	return &ServiceMonitorRepository{db: db}
}

// ─── Service Monitors ────────────────────────────────────────────────────────

func (r *ServiceMonitorRepository) Create(m *sm.ServiceMonitor) (*sm.ServiceMonitor, error) {
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
	var result sm.ServiceMonitor
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ServiceMonitorRepository) Update(m *sm.ServiceMonitor) (*sm.ServiceMonitor, error) {
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
		return nil, sm.ErrNotFound
	}
	var result sm.ServiceMonitor
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
		return sm.ErrNotFound
	}
	return nil
}

func (r *ServiceMonitorRepository) FindByID(id string) (*sm.ServiceMonitor, error) {
	var m sm.ServiceMonitor
	err := r.db.Get(&m, `SELECT * FROM service_monitors WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sm.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *ServiceMonitorRepository) ListByNodeID(nodeID string) ([]*sm.ServiceMonitor, error) {
	var monitors []*sm.ServiceMonitor
	err := r.db.Select(&monitors,
		`SELECT * FROM service_monitors WHERE node_id = $1 ORDER BY created_at ASC`,
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

// ─── Health Events ───────────────────────────────────────────────────────────

func (r *ServiceMonitorRepository) InsertEvent(e *sm.ServiceHealthEvent) error {
	_, err := r.db.NamedExec(`
		INSERT INTO service_health_events (monitor_id, node_id, user_id, status, latency_ms, message, checked_at)
		VALUES (:monitor_id, :node_id, :user_id, :status, :latency_ms, :message, :checked_at)`,
		e,
	)
	return err
}

func (r *ServiceMonitorRepository) ListEventsByMonitorID(monitorID string, limit int) ([]*sm.ServiceHealthEvent, error) {
	var events []*sm.ServiceHealthEvent
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

func (r *ServiceMonitorRepository) UptimeHistory24h(monitorID string) ([]*sm.UptimePoint, error) {
	// Generate 5-minute buckets for the last 24 hours and join with health events.
	// Each bucket shows 'up' if all checks passed, 'down' if any failed, or 'no_data' if there are none.
	var points []*sm.UptimePoint
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
				END AS status
			FROM service_health_events
			WHERE monitor_id = $1
			  AND checked_at >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		)
		SELECT
			to_char(b.bucket, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS bucket,
			COALESCE(a.status, 'no_data') AS status
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
