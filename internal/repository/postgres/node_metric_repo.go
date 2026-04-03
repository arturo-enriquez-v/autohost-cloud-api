package postgres

import (
	"fmt"

	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	"github.com/jmoiron/sqlx"
)

type NodeMetricRepo struct {
	DB *sqlx.DB
}

func NewNodeMetricRepository(db *sqlx.DB) *NodeMetricRepo {
	return &NodeMetricRepo{DB: db}
}

func (r *NodeMetricRepo) StoreNodeMetric(req *nodemetric.CreateNodeMetricRequest) (*nodemetric.NodeMetric, error) {
	var metric nodemetric.NodeMetric
	err := r.DB.QueryRowx(`
		INSERT INTO node_metrics (
			node_id, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
			memory_available_bytes, memory_usage_percent, disk_total_bytes, 
			disk_used_bytes, disk_available_bytes, disk_usage_percent, collected_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, node_id, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
		          memory_available_bytes, memory_usage_percent, disk_total_bytes, disk_used_bytes,
		          disk_available_bytes, disk_usage_percent, collected_at, created_at
	`, req.NodeID, req.CPUUsagePercent, req.MemoryTotalBytes,
		req.MemoryUsedBytes, req.MemoryAvailableBytes, req.MemoryUsagePercent,
		req.DiskTotalBytes, req.DiskUsedBytes, req.DiskAvailableBytes,
		req.DiskUsagePercent, req.CollectedAt).StructScan(&metric)
	if err != nil {
		return nil, err
	}

	return &metric, nil
}

// allowedBuckets is the whitelist for date_trunc granularity values to prevent SQL injection.
var allowedBuckets = map[string]bool{
	"minute": true,
	"hour":   true,
	"day":    true,
}

func (r *NodeMetricRepo) GetHistoryByNodeID(params nodemetric.HistoryParams) ([]*nodemetric.NodeMetricHistoryPoint, error) {
	bucket := params.Bucket
	if !allowedBuckets[bucket] {
		bucket = "hour"
	}

	query := fmt.Sprintf(`
		SELECT
			date_trunc('%s', collected_at) AS bucket,
			AVG(cpu_usage_percent)         AS cpu_usage_percent,
			AVG(memory_usage_percent)      AS memory_usage_percent,
			AVG(disk_usage_percent)        AS disk_usage_percent
		FROM node_metrics
		WHERE node_id = $1
		  AND collected_at >= $2
		  AND collected_at <= $3
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucket)

	var points []*nodemetric.NodeMetricHistoryPoint
	if err := r.DB.Select(&points, query, params.NodeID, params.From, params.To); err != nil {
		return nil, err
	}
	return points, nil
}

// func (r *NodeMetricRepo) GetLatestMetricByNodeID(nodeID string) (*nodemetric.NodeMetric, error) {
// 	var model nodemetric.NodeMetric
// 	err := r.DB.Get(&model, `
// 		SELECT id, node_id, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
// 		       memory_usage_percent, disk_total_bytes, disk_used_bytes,
// 		       disk_available_bytes, disk_usage_percent, collected_at, created_at
// 		FROM node_metrics
// 		WHERE node_id = $1
// 		ORDER BY collected_at DESC
// 		LIMIT 1
// 	`, nodeID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &model, nil
// }

// func (r *NodeMetricRepo) GetMetricsByNodeID(nodeID string, limit, offset int) ([]*nodemetric.NodeMetric, error) {
// 	var models []*nodemetric.NodeMetric
// 	err := r.DB.Select(&models, `
// 		SELECT id, node_id, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
// 		       memory_usage_percent, disk_total_bytes, disk_used_bytes,
// 		       disk_available_bytes, disk_usage_percent, collected_at, created_at
// 		FROM node_metrics
// 		WHERE node_id = $1
// 		ORDER BY collected_at DESC
// 		LIMIT $2 OFFSET $3
// 	`, nodeID, limit, offset)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return models, nil
// }
