package node

import "time"

// NodeWithMetrics representa un nodo con su última métrica
type NodeWithMetrics struct {
	// Datos del nodo
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname"`
	IPLocal      string     `json:"ip_local"`
	IPVPN        *string    `json:"ip_vpn"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	VersionAgent string     `json:"version_agent"`
	OwnerID      *string    `json:"owner_id"`
	ProjectID    *string    `json:"project_id"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`

	// Última métrica (puede ser nil si no hay métricas)
	LastMetric *LastMetric `json:"last_metric,omitempty"`
}

// LastMetric representa la última métrica de un nodo
type LastMetric struct {
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsagePercent float64   `json:"memory_usage_percent"`
	DiskUsagePercent   float64   `json:"disk_usage_percent"`
	CollectedAt        time.Time `json:"collected_at"`
}
