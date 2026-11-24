// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
)

// MonitorStatsRepository aggregates service_health_events for charts.
type MonitorStatsRepository struct {
	db *sqlx.DB
}

func NewMonitorStatsRepository(db *sqlx.DB) *MonitorStatsRepository {
	return &MonitorStatsRepository{db: db}
}

// bucketOrigin aligns buckets to fixed boundaries (e.g. whole hours in UTC) whatever "now" is.
const bucketOrigin = "TIMESTAMPTZ '2000-01-01 00:00:00+00'"

func intervalParam(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

func (r *MonitorStatsRepository) StatsBuckets(monitorID string, from time.Time, bucket time.Duration) ([]*apps.MonitorStatsBucket, error) {
	var buckets []*apps.MonitorStatsBucket
	err := r.db.Select(&buckets, `
		WITH series AS (
			SELECT generate_series(date_bin($2::interval, $3::timestamptz, `+bucketOrigin+`), NOW(), $2::interval) AS bucket
		),
		agg AS (
			SELECT
				date_bin($2::interval, checked_at, `+bucketOrigin+`) AS bucket,
				count(*) FILTER (WHERE status = 'up') AS passed,
				count(*) FILTER (WHERE status <> 'up') AS failed,
				(avg(latency_ms) FILTER (WHERE status = 'up'))::float8 AS avg_latency_ms,
				(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status = 'up'))::float8 AS p95_latency_ms
			FROM service_health_events
			WHERE monitor_id = $1 AND checked_at >= $3
			GROUP BY 1
		)
		SELECT s.bucket,
		       COALESCE(a.passed, 0) AS passed,
		       COALESCE(a.failed, 0) AS failed,
		       a.avg_latency_ms,
		       a.p95_latency_ms
		FROM series s
		LEFT JOIN agg a ON a.bucket = s.bucket
		ORDER BY s.bucket ASC`,
		monitorID, intervalParam(bucket), from,
	)
	return buckets, err
}

func (r *MonitorStatsRepository) StatsSummary(monitorID string, from time.Time) (*apps.MonitorStatsSummary, error) {
	var s apps.MonitorStatsSummary
	err := r.db.Get(&s, `
		SELECT
			count(*) AS checks,
			count(*) FILTER (WHERE status = 'up') AS passed,
			count(*) FILTER (WHERE status <> 'up') AS failed,
			(avg(latency_ms) FILTER (WHERE status = 'up'))::float8 AS avg_latency_ms,
			(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status = 'up'))::float8 AS p95_latency_ms
		FROM service_health_events
		WHERE monitor_id = $1 AND checked_at >= $2`,
		monitorID, from,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Incidents groups consecutive failed checks ("gaps and islands"): each failed check is tagged with the
// number of passed checks before it, so a run of failures shares one tag.
func (r *MonitorStatsRepository) Incidents(monitorID string, from time.Time, threshold, limit int) ([]*apps.MonitorIncident, error) {
	var incidents []*apps.MonitorIncident
	err := r.db.Select(&incidents, `
		WITH events AS (
			SELECT checked_at, status, message,
			       count(*) FILTER (WHERE status = 'up') OVER (ORDER BY checked_at) AS run
			FROM service_health_events
			WHERE monitor_id = $1 AND checked_at >= $2
		),
		runs AS (
			SELECT run,
			       min(checked_at) AS started_at,
			       max(checked_at) AS last_failed_at,
			       count(*) AS failed_checks,
			       COALESCE((array_agg(message ORDER BY checked_at))[1], '') AS message
			FROM events
			WHERE status <> 'up'
			GROUP BY run
			HAVING count(*) >= $3
		)
		SELECT r.started_at, r.last_failed_at, r.failed_checks, r.message,
		       (SELECT min(e.checked_at) FROM service_health_events e
		        WHERE e.monitor_id = $1 AND e.checked_at > r.last_failed_at AND e.status = 'up') AS resolved_at
		FROM runs r
		ORDER BY r.started_at DESC
		LIMIT $4`,
		monitorID, from, threshold, limit,
	)
	return incidents, err
}
