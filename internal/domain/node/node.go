package node

import "time"

// Node representa un nodo/servidor registrado en el sistema
type Node struct {
	ID           string     `db:"id"`
	Hostname     string     `db:"hostname"`
	IPLocal      string     `db:"ip_local"`
	IPVPN        *string    `db:"ip_vpn"`
	OS           string     `db:"os"`
	Arch         string     `db:"arch"`
	VersionAgent string     `db:"version_agent"`
	OwnerID      *string    `db:"owner_id"`
	ProjectID    *string    `db:"project_id"`
	LastSeenAt   *time.Time `db:"last_seen_at"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

// Repository define las operaciones de persistencia para nodos
type Repository interface {
	Register(node *Node) (*Node, error)
	FindByID(id string) (*Node, error)
	Update(node *Node) (*Node, error)
	Delete(id string) error
	FindByOwnerID(ownerID string) ([]*Node, error)
	FindByOwnerIDWithMetrics(ownerID string) ([]*NodeWithMetrics, error)
	UpdateLastSeen(nodeID string) error
	UpdateLastSeenAndIPs(nodeID string, ipLocal string, ipVPN string) error
	UpdateVPNIP(nodeID string, ipVPN string) error
	UpdateVersionAgent(nodeID, version string) error
	AssignProject(nodeID string, projectID *string) error
}
