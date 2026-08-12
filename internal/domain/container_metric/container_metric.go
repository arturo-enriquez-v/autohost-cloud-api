package containermetric

import "time"

type ContainerMetric struct {
	ID                 string    `db:"id" json:"id"`
	NodeID             string    `db:"node_id" json:"node_id"`
	ContainerID        string    `db:"container_id" json:"container_id"`
	ContainerName      string    `db:"container_name" json:"container_name"`
	CPUUsagePercent    float64   `db:"cpu_usage_percent" json:"cpu_usage_percent"`
	MemoryUsageBytes   int64     `db:"memory_usage_bytes" json:"memory_usage_bytes"`
	MemoryUsagePercent float64   `db:"memory_usage_percent" json:"memory_usage_percent"`
	Status             string    `db:"status" json:"status"`
	State              string    `db:"state" json:"state"`
	CollectedAt        time.Time `db:"collected_at" json:"collected_at"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
}

type CreateContainerMetricRequest struct {
	NodeID             string    `json:"node_id"`
	ContainerID        string    `json:"container_id"`
	ContainerName      string    `json:"container_name"`
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsageBytes   int64     `json:"memory_usage_bytes"`
	MemoryUsagePercent float64   `json:"memory_usage_percent"`
	Status             string    `json:"status"`
	State              string    `json:"state"`
	CollectedAt        time.Time `json:"collected_at"`
}

type ContainerMetricHistoryPoint struct {
	Bucket             time.Time `db:"bucket" json:"bucket"`
	CPUUsagePercent    float64   `db:"cpu_usage_percent" json:"cpu_usage_percent"`
	MemoryUsagePercent float64   `db:"memory_usage_percent" json:"memory_usage_percent"`
	MemoryUsageBytes   int64     `db:"memory_usage_bytes" json:"memory_usage_bytes"`
	State              string    `db:"state" json:"state"`
}

type ContainerHistoryParams struct {
	NodeID        string
	ContainerName string
	ContainerID   string
	From          time.Time
	To            time.Time
	Bucket        string
}

type Repository interface {
	StoreBatch(metrics []*CreateContainerMetricRequest) error
	GetHistory(params ContainerHistoryParams) ([]*ContainerMetricHistoryPoint, error)
}
