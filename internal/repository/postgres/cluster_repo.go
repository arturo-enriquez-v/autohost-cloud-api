package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/cluster"
	"github.com/jmoiron/sqlx"
)

type ClusterRepository struct {
	db *sqlx.DB
}

func NewClusterRepository(db *sqlx.DB) *ClusterRepository {
	return &ClusterRepository{db: db}
}

func (r *ClusterRepository) CreateCluster(ctx context.Context, c *cluster.Cluster) (*cluster.Cluster, error) {
	var out cluster.Cluster
	query := `
		INSERT INTO clusters (user_id, name, status, overlay_network)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, name, status, overlay_network, created_at, updated_at`
	err := r.db.QueryRowContext(ctx, query, c.UserID, c.Name, c.Status, c.OverlayNetwork).
		Scan(&out.ID, &out.UserID, &out.Name, &out.Status, &out.OverlayNetwork, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ClusterRepository) GetClusterByID(ctx context.Context, id string) (*cluster.Cluster, error) {
	var c cluster.Cluster
	query := `SELECT id, user_id, name, status, overlay_network, created_at, updated_at FROM clusters WHERE id = $1`
	err := r.db.GetContext(ctx, &c, query, id)
	if err == sql.ErrNoRows {
		return nil, cluster.ErrClusterNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ClusterRepository) ListClustersByUserID(ctx context.Context, userID string) ([]*cluster.Cluster, error) {
	var list []*cluster.Cluster
	query := `SELECT id, user_id, name, status, overlay_network, created_at, updated_at FROM clusters WHERE user_id = $1 ORDER BY created_at DESC`
	err := r.db.SelectContext(ctx, &list, query, userID)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *ClusterRepository) DeleteCluster(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM clusters WHERE id = $1`, id)
	return err
}

func (r *ClusterRepository) AddClusterNode(ctx context.Context, cn *cluster.ClusterNode) (*cluster.ClusterNode, error) {
	var out cluster.ClusterNode
	query := `
		INSERT INTO cluster_nodes (cluster_id, node_id, role, swarm_node_id, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cluster_id, node_id)
		DO UPDATE SET role = EXCLUDED.role, swarm_node_id = EXCLUDED.swarm_node_id, status = EXCLUDED.status
		RETURNING id, cluster_id, node_id, role, swarm_node_id, status, joined_at`
	err := r.db.QueryRowContext(ctx, query, cn.ClusterID, cn.NodeID, cn.Role, cn.SwarmNodeID, cn.Status).
		Scan(&out.ID, &out.ClusterID, &out.NodeID, &out.Role, &out.SwarmNodeID, &out.Status, &out.JoinedAt)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ClusterRepository) GetClusterNodes(ctx context.Context, clusterID string) ([]*cluster.ClusterNode, error) {
	query := `
		SELECT 
			cn.id, cn.cluster_id, cn.node_id, cn.role, cn.swarm_node_id, cn.status, cn.joined_at,
			n.hostname as node_hostname, n.ip_local as node_ip, n.ip_vpn as node_ip_vpn
		FROM cluster_nodes cn
		JOIN nodes n ON cn.node_id = n.id
		WHERE cn.cluster_id = $1
		ORDER BY cn.role DESC, cn.joined_at ASC`
	var list []*cluster.ClusterNode
	err := r.db.SelectContext(ctx, &list, query, clusterID)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *ClusterRepository) RemoveClusterNode(ctx context.Context, clusterID, nodeID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM cluster_nodes WHERE cluster_id = $1 AND node_id = $2`, clusterID, nodeID)
	return err
}

func (r *ClusterRepository) CreateClusterService(ctx context.Context, s *cluster.ClusterService) (*cluster.ClusterService, error) {
	envJSON, err := json.Marshal(s.EnvVars)
	if err != nil {
		envJSON = []byte("{}")
	}

	var out cluster.ClusterService
	query := `
		INSERT INTO cluster_services (cluster_id, name, image, ports, env_vars, replicas, swarm_service_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, cluster_id, name, image, ports, env_vars, replicas, swarm_service_id, status, created_at, updated_at`
	err = r.db.QueryRowContext(ctx, query, s.ClusterID, s.Name, s.Image, s.Ports, envJSON, s.Replicas, s.SwarmServiceID, s.Status).
		Scan(&out.ID, &out.ClusterID, &out.Name, &out.Image, &out.Ports, &out.EnvVarsRaw, &out.Replicas, &out.SwarmServiceID, &out.Status, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(out.EnvVarsRaw, &out.EnvVars)
	return &out, nil
}

func (r *ClusterRepository) GetClusterServiceByID(ctx context.Context, id string) (*cluster.ClusterService, error) {
	var s cluster.ClusterService
	query := `SELECT id, cluster_id, name, image, ports, env_vars, replicas, swarm_service_id, status, created_at, updated_at FROM cluster_services WHERE id = $1`
	err := r.db.GetContext(ctx, &s, query, id)
	if err == sql.ErrNoRows {
		return nil, cluster.ErrServiceNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(s.EnvVarsRaw, &s.EnvVars)
	return &s, nil
}

func (r *ClusterRepository) ListClusterServices(ctx context.Context, clusterID string) ([]*cluster.ClusterService, error) {
	var list []*cluster.ClusterService
	query := `SELECT id, cluster_id, name, image, ports, env_vars, replicas, swarm_service_id, status, created_at, updated_at FROM cluster_services WHERE cluster_id = $1 ORDER BY created_at ASC`
	err := r.db.SelectContext(ctx, &list, query, clusterID)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		_ = json.Unmarshal(s.EnvVarsRaw, &s.EnvVars)
	}
	return list, nil
}

func (r *ClusterRepository) UpdateClusterServiceReplicas(ctx context.Context, serviceID string, replicas int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE cluster_services SET replicas = $1, updated_at = NOW() WHERE id = $2`, replicas, serviceID)
	return err
}

func (r *ClusterRepository) DeleteClusterService(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM cluster_services WHERE id = $1`, id)
	return err
}

func (r *ClusterRepository) UpsertAutoscalePolicy(ctx context.Context, p *cluster.AutoscalePolicy) (*cluster.AutoscalePolicy, error) {
	var out cluster.AutoscalePolicy
	query := `
		INSERT INTO cluster_service_autoscaling (
			cluster_service_id, enabled, metric_type, scale_up_threshold, scale_down_threshold,
			scale_up_duration_sec, scale_down_duration_sec, min_replicas, max_replicas, cooldown_seconds
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (cluster_service_id)
		DO UPDATE SET
			enabled = EXCLUDED.enabled,
			metric_type = EXCLUDED.metric_type,
			scale_up_threshold = EXCLUDED.scale_up_threshold,
			scale_down_threshold = EXCLUDED.scale_down_threshold,
			scale_up_duration_sec = EXCLUDED.scale_up_duration_sec,
			scale_down_duration_sec = EXCLUDED.scale_down_duration_sec,
			min_replicas = EXCLUDED.min_replicas,
			max_replicas = EXCLUDED.max_replicas,
			cooldown_seconds = EXCLUDED.cooldown_seconds,
			updated_at = NOW()
		RETURNING id, cluster_service_id, enabled, metric_type, scale_up_threshold, scale_down_threshold,
		          scale_up_duration_sec, scale_down_duration_sec, min_replicas, max_replicas,
		          cooldown_seconds, last_scaled_at, created_at, updated_at`
	err := r.db.QueryRowContext(ctx, query,
		p.ClusterServiceID, p.Enabled, p.MetricType, p.ScaleUpThreshold, p.ScaleDownThreshold,
		p.ScaleUpDurationSec, p.ScaleDownDurationSec, p.MinReplicas, p.MaxReplicas, p.CooldownSeconds,
	).Scan(
		&out.ID, &out.ClusterServiceID, &out.Enabled, &out.MetricType, &out.ScaleUpThreshold, &out.ScaleDownThreshold,
		&out.ScaleUpDurationSec, &out.ScaleDownDurationSec, &out.MinReplicas, &out.MaxReplicas,
		&out.CooldownSeconds, &out.LastScaledAt, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ClusterRepository) GetAutoscalePolicyByServiceID(ctx context.Context, serviceID string) (*cluster.AutoscalePolicy, error) {
	var p cluster.AutoscalePolicy
	query := `
		SELECT id, cluster_service_id, enabled, metric_type, scale_up_threshold, scale_down_threshold,
		       scale_up_duration_sec, scale_down_duration_sec, min_replicas, max_replicas,
		       cooldown_seconds, last_scaled_at, created_at, updated_at
		FROM cluster_service_autoscaling WHERE cluster_service_id = $1`
	err := r.db.GetContext(ctx, &p, query, serviceID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

type autoscalePolicyRow struct {
	ID                   string     `db:"id"`
	ClusterServiceID     string     `db:"cluster_service_id"`
	Enabled              bool       `db:"enabled"`
	MetricType           string     `db:"metric_type"`
	ScaleUpThreshold     float64    `db:"scale_up_threshold"`
	ScaleDownThreshold   float64    `db:"scale_down_threshold"`
	ScaleUpDurationSec   int        `db:"scale_up_duration_sec"`
	ScaleDownDurationSec int        `db:"scale_down_duration_sec"`
	MinReplicas          int        `db:"min_replicas"`
	MaxReplicas          int        `db:"max_replicas"`
	CooldownSeconds      int        `db:"cooldown_seconds"`
	LastScaledAt         *time.Time `db:"last_scaled_at"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`

	ServiceID            string     `db:"service_id"`
	ServiceName          string     `db:"service_name"`
	CurrentReplicas      int        `db:"current_replicas"`
	ClusterID            string     `db:"cluster_id"`
	ManagerNodeID        string     `db:"manager_node_id"`
}

func (r *ClusterRepository) ListAllActiveAutoscalePolicies(ctx context.Context) ([]*cluster.AutoscalePolicyWithContext, error) {
	query := `
		SELECT 
			csa.id, csa.cluster_service_id, csa.enabled, csa.metric_type,
			csa.scale_up_threshold, csa.scale_down_threshold,
			csa.scale_up_duration_sec, csa.scale_down_duration_sec,
			csa.min_replicas, csa.max_replicas, csa.cooldown_seconds,
			csa.last_scaled_at, csa.created_at, csa.updated_at,
			cs.id as service_id, cs.name as service_name, cs.replicas as current_replicas,
			cs.cluster_id as cluster_id,
			cn.node_id as manager_node_id
		FROM cluster_service_autoscaling csa
		JOIN cluster_services cs ON csa.cluster_service_id = cs.id
		JOIN cluster_nodes cn ON cs.cluster_id = cn.cluster_id AND cn.role = 'manager'
		WHERE csa.enabled = true`

	var rows []autoscalePolicyRow
	err := r.db.SelectContext(ctx, &rows, query)
	if err != nil {
		return nil, err
	}

	var out []*cluster.AutoscalePolicyWithContext
	for _, row := range rows {
		out = append(out, &cluster.AutoscalePolicyWithContext{
			Policy: cluster.AutoscalePolicy{
				ID:                   row.ID,
				ClusterServiceID:     row.ClusterServiceID,
				Enabled:              row.Enabled,
				MetricType:           row.MetricType,
				ScaleUpThreshold:     row.ScaleUpThreshold,
				ScaleDownThreshold:   row.ScaleDownThreshold,
				ScaleUpDurationSec:   row.ScaleUpDurationSec,
				ScaleDownDurationSec: row.ScaleDownDurationSec,
				MinReplicas:          row.MinReplicas,
				MaxReplicas:          row.MaxReplicas,
				CooldownSeconds:      row.CooldownSeconds,
				LastScaledAt:         row.LastScaledAt,
				CreatedAt:            row.CreatedAt,
				UpdatedAt:            row.UpdatedAt,
			},
			ServiceID:       row.ServiceID,
			ServiceName:     row.ServiceName,
			CurrentReplicas: row.CurrentReplicas,
			ClusterID:       row.ClusterID,
			ManagerNodeID:   row.ManagerNodeID,
		})
	}
	return out, nil
}

func (r *ClusterRepository) UpdateLastScaledAt(ctx context.Context, policyID string, t time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE cluster_service_autoscaling SET last_scaled_at = $1, updated_at = NOW() WHERE id = $2`, t, policyID)
	return err
}

func (r *ClusterRepository) GetServiceAverageMetric(ctx context.Context, serviceName string, windowDuration time.Duration) (float64, float64, int, error) {
	since := time.Now().Add(-windowDuration)
	query := `
		SELECT 
			COALESCE(AVG(cpu_usage_percent), 0.0) as avg_cpu,
			COALESCE(AVG(memory_usage_percent), 0.0) as avg_ram,
			COUNT(id) as sample_count
		FROM container_metrics
		WHERE (container_name = $1 OR container_name LIKE $2)
		  AND (state = 'running' OR state = '' OR memory_usage_percent > 0.0)
		  AND collected_at >= $3`

	var res struct {
		AvgCPU      float64 `db:"avg_cpu"`
		AvgRAM      float64 `db:"avg_ram"`
		SampleCount int     `db:"sample_count"`
	}
	err := r.db.GetContext(ctx, &res, query, serviceName, serviceName+".%", since)
	if err != nil {
		return 0, 0, 0, err
	}
	return res.AvgCPU, res.AvgRAM, res.SampleCount, nil
}

