// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"fmt"
	"time"

	containermetric "github.com/arturo/autohost-cloud-api/internal/domain/container_metric"
	"github.com/jmoiron/sqlx"
)

type ContainerMetricRepo struct {
	DB *sqlx.DB
}

func NewContainerMetricRepository(db *sqlx.DB) *ContainerMetricRepo {
	return &ContainerMetricRepo{DB: db}
}

func (r *ContainerMetricRepo) StoreBatch(metrics []*containermetric.CreateContainerMetricRequest) error {
	if len(metrics) == 0 {
		return nil
	}

	tx, err := r.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Preparex(`
		INSERT INTO container_metrics (
			node_id, container_id, container_name, cpu_usage_percent,
			memory_usage_bytes, memory_usage_percent, status, state, collected_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range metrics {
		_, err := stmt.Exec(
			m.NodeID, m.ContainerID, m.ContainerName, m.CPUUsagePercent,
			m.MemoryUsageBytes, m.MemoryUsagePercent, m.Status, m.State, m.CollectedAt,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

var allowedContainerBuckets = map[string]bool{
	"minute": true,
	"hour":   true,
	"day":    true,
}

func (r *ContainerMetricRepo) GetHistory(params containermetric.ContainerHistoryParams) ([]*containermetric.ContainerMetricHistoryPoint, error) {
	bucket := params.Bucket
	if !allowedContainerBuckets[bucket] {
		bucket = "hour"
	}

	from := params.From
	if from.IsZero() {
		from = time.Now().Add(-7 * 24 * time.Hour)
	}

	to := params.To
	if to.IsZero() {
		to = time.Now()
	}

	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', collected_at) AS bucket,
			COALESCE(AVG(cpu_usage_percent), 0)    AS cpu_usage_percent,
			COALESCE(AVG(memory_usage_percent), 0) AS memory_usage_percent,
			COALESCE(CAST(AVG(memory_usage_bytes) AS BIGINT), 0) AS memory_usage_bytes,
			COALESCE(MAX(state), 'unknown')        AS state
		FROM container_metrics
		WHERE node_id = $1
		  AND (container_name = $2 OR container_id = $3)
		  AND collected_at >= $4
		  AND collected_at <= $5
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucket)

	nameOrID := params.ContainerName
	if nameOrID == "" {
		nameOrID = params.ContainerID
	}
	idOrName := params.ContainerID
	if idOrName == "" {
		idOrName = params.ContainerName
	}

	var points []*containermetric.ContainerMetricHistoryPoint
	if err := r.DB.Select(&points, query, params.NodeID, nameOrID, idOrName, from, to); err != nil {
		return nil, err
	}
	return points, nil
}
