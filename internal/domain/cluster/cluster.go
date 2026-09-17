package cluster

import (
	"context"
	"errors"
	"time"
)

var (
	ErrClusterNotFound    = errors.New("cluster not found")
	ErrServiceNotFound    = errors.New("cluster service not found")
	ErrInvalidClusterData = errors.New("invalid cluster data")
	ErrManagerRequired    = errors.New("at least one manager node is required")
	ErrNodeNotFound       = errors.New("node not found")
	ErrNodeNoVPNIP        = errors.New("node does not have an active VPN IP")
)

type ClusterRole string

const (
	RoleManager ClusterRole = "manager"
	RoleWorker  ClusterRole = "worker"
)

// Cluster represents a Docker Swarm cluster across AutoHost nodes.
type Cluster struct {
	ID             string    `db:"id"              json:"id"`
	UserID         string    `db:"user_id"         json:"user_id"`
	Name           string    `db:"name"            json:"name"`
	Status         string    `db:"status"          json:"status"`
	OverlayNetwork string    `db:"overlay_network" json:"overlay_network"`
	CreatedAt      time.Time `db:"created_at"      json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"      json:"updated_at"`
}

// ClusterNode associates a registered AutoHost node to a Swarm cluster.
type ClusterNode struct {
	ID          string      `db:"id"            json:"id"`
	ClusterID   string      `db:"cluster_id"    json:"cluster_id"`
	NodeID      string      `db:"node_id"       json:"node_id"`
	Role        ClusterRole `db:"role"          json:"role"`
	SwarmNodeID string      `db:"swarm_node_id" json:"swarm_node_id"`
	Status      string      `db:"status"        json:"status"`
	JoinedAt    time.Time   `db:"joined_at"     json:"joined_at"`

	// Joined fields from nodes table
	NodeHostname string  `db:"node_hostname" json:"node_hostname,omitempty"`
	NodeIP       string  `db:"node_ip"       json:"node_ip,omitempty"`
	NodeIPVPN    *string `db:"node_ip_vpn"   json:"node_ip_vpn,omitempty"`
}

// ClusterService represents a stateless service deployed to the Swarm.
type ClusterService struct {
	ID              string            `db:"id"               json:"id"`
	ClusterID       string            `db:"cluster_id"       json:"cluster_id"`
	Name            string            `db:"name"             json:"name"`
	Image           string            `db:"image"            json:"image"`
	Ports           string            `db:"ports"            json:"ports"`
	EnvVars         map[string]string `json:"env_vars"`
	EnvVarsRaw      []byte            `db:"env_vars"         json:"-"`
	Replicas        int               `db:"replicas"         json:"replicas"`
	SwarmServiceID  string            `db:"swarm_service_id" json:"swarm_service_id"`
	Status          string            `db:"status"           json:"status"`
	CreatedAt       time.Time         `db:"created_at"       json:"created_at"`
	UpdatedAt       time.Time         `db:"updated_at"       json:"updated_at"`
	AutoscalePolicy *AutoscalePolicy  `json:"autoscaling,omitempty"`
}

// AutoscalePolicy defines the threshold and cooldown parameters for service autoscaling.
type AutoscalePolicy struct {
	ID                   string     `db:"id"                      json:"id"`
	ClusterServiceID     string     `db:"cluster_service_id"      json:"cluster_service_id"`
	Enabled              bool       `db:"enabled"                 json:"enabled"`
	MetricType           string     `db:"metric_type"             json:"metric_type"` // "cpu" or "ram"
	ScaleUpThreshold     float64    `db:"scale_up_threshold"      json:"scale_up_threshold"`
	ScaleDownThreshold   float64    `db:"scale_down_threshold"    json:"scale_down_threshold"`
	ScaleUpDurationSec   int        `db:"scale_up_duration_sec"   json:"scale_up_duration_sec"`
	ScaleDownDurationSec int        `db:"scale_down_duration_sec" json:"scale_down_duration_sec"`
	MinReplicas          int        `db:"min_replicas"            json:"min_replicas"`
	MaxReplicas          int        `db:"max_replicas"            json:"max_replicas"`
	CooldownSeconds      int        `db:"cooldown_seconds"        json:"cooldown_seconds"`
	LastScaledAt         *time.Time `db:"last_scaled_at"          json:"last_scaled_at,omitempty"`
	CreatedAt            time.Time  `db:"created_at"              json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"              json:"updated_at"`
}

// AutoscalePolicyWithContext provides context for the background autoscaling scheduler.
type AutoscalePolicyWithContext struct {
	Policy         AutoscalePolicy
	ServiceID      string
	ServiceName    string
	CurrentReplicas int
	ClusterID      string
	ManagerNodeID  string
}

// ClusterWithDetails aggregates a cluster, its assigned nodes, and deployed services.
type ClusterWithDetails struct {
	Cluster  *Cluster          `json:"cluster"`
	Nodes    []*ClusterNode    `json:"nodes"`
	Services []*ClusterService `json:"services"`
}

// Repository defines persistence for clusters, nodes, services, and autoscaling.
type Repository interface {
	CreateCluster(ctx context.Context, c *Cluster) (*Cluster, error)
	GetClusterByID(ctx context.Context, id string) (*Cluster, error)
	ListClustersByUserID(ctx context.Context, userID string) ([]*Cluster, error)
	DeleteCluster(ctx context.Context, id string) error

	AddClusterNode(ctx context.Context, cn *ClusterNode) (*ClusterNode, error)
	GetClusterNodes(ctx context.Context, clusterID string) ([]*ClusterNode, error)
	RemoveClusterNode(ctx context.Context, clusterID, nodeID string) error

	CreateClusterService(ctx context.Context, s *ClusterService) (*ClusterService, error)
	GetClusterServiceByID(ctx context.Context, id string) (*ClusterService, error)
	ListClusterServices(ctx context.Context, clusterID string) ([]*ClusterService, error)
	UpdateClusterServiceReplicas(ctx context.Context, serviceID string, replicas int) error
	DeleteClusterService(ctx context.Context, id string) error

	UpsertAutoscalePolicy(ctx context.Context, p *AutoscalePolicy) (*AutoscalePolicy, error)
	GetAutoscalePolicyByServiceID(ctx context.Context, serviceID string) (*AutoscalePolicy, error)
	ListAllActiveAutoscalePolicies(ctx context.Context) ([]*AutoscalePolicyWithContext, error)
	UpdateLastScaledAt(ctx context.Context, policyID string, t time.Time) error
	GetServiceAverageMetric(ctx context.Context, serviceName string, windowDuration time.Duration) (avgCPU, avgRAM float64, sampleCount int, err error)
}
