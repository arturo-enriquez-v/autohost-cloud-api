// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"errors"
	"time"
)

var (
	ErrNodeNotFound    = errors.New("node not found")
	ErrInvalidNodeData = errors.New("invalid node data")
	ErrUnauthorized    = errors.New("unauthorized")
)

// Node representa un nodo/servidor registrado en el sistema
type Node struct {
	ID           string     `db:"id" json:"id"`
	Hostname     string     `db:"hostname" json:"hostname"`
	IPLocal      string     `db:"ip_local" json:"ip_local"`
	IPVPN        *string    `db:"ip_vpn" json:"ip_vpn"`
	OS           string     `db:"os" json:"os"`
	Arch         string     `db:"arch" json:"arch"`
	VersionAgent string     `db:"version_agent" json:"version_agent"`
	OwnerID      *string    `db:"owner_id" json:"owner_id"`
	ProjectID    *string    `db:"project_id" json:"project_id"`
	IsLocal      bool       `db:"is_local" json:"is_local"`
	LastSeenAt   *time.Time `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
}

// NodeWithMetrics representa un nodo con su última métrica
type NodeWithMetrics struct {
	ID           string      `json:"id"`
	Hostname     string      `json:"hostname"`
	IPLocal      string      `json:"ip_local"`
	IPVPN        *string     `json:"ip_vpn"`
	OS           string      `json:"os"`
	Arch         string      `json:"arch"`
	VersionAgent string      `json:"version_agent"`
	OwnerID      *string     `json:"owner_id"`
	ProjectID    *string     `json:"project_id"`
	IsLocal      bool        `json:"is_local"`
	LastSeenAt   *time.Time  `json:"last_seen_at"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	LastMetric   *LastMetric `json:"last_metric,omitempty"`
}

// LastMetric representa la última métrica de un nodo
type LastMetric struct {
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsagePercent float64   `json:"memory_usage_percent"`
	DiskUsagePercent   float64   `json:"disk_usage_percent"`
	CPUTempCelsius     float64   `json:"cpu_temp_celsius,omitempty"`
	CollectedAt        time.Time `json:"collected_at"`
}

// NodeRepository define las operaciones de persistencia para nodos
type NodeRepository interface {
	Register(node *Node) (*Node, error)
	FindByID(id string) (*Node, error)
	FindLocalNode() (*Node, error)
	ClaimUnownedNodes(ownerID string) error
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

// NodeService gestiona la lógica de negocio para nodos
type NodeService struct {
	repo NodeRepository
}

func NewNodeService(repo NodeRepository) *NodeService {
	return &NodeService{repo: repo}
}

// Backwards-compatible aliases for internal/core/node package consumers
type Repository = NodeRepository
type Service = NodeService

func NewService(repo NodeRepository) *NodeService {
	return NewNodeService(repo)
}

func (s *NodeService) Register(node *Node) (*Node, error) {
	if node.Hostname == "" {
		return nil, ErrInvalidNodeData
	}
	return s.repo.Register(node)
}

func (s *NodeService) UpdateLastSeen(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateLastSeen(nodeID)
}

func (s *NodeService) UpdateLastSeenAndIPs(nodeID string, ipLocal string, ipVPN string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateLastSeenAndIPs(nodeID, ipLocal, ipVPN)
}

func (s *NodeService) UpdateVPNIP(nodeID string, ipVPN string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateVPNIP(nodeID, ipVPN)
}

func (s *NodeService) UpdateVersionAgent(nodeID, version string) error {
	if nodeID == "" || version == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateVersionAgent(nodeID, version)
}

func (s *NodeService) GetByOwner(ownerID string) ([]*Node, error) {
	return s.repo.FindByOwnerID(ownerID)
}

func (s *NodeService) GetByOwnerWithMetrics(ownerID string) ([]*NodeWithMetrics, error) {
	return s.repo.FindByOwnerIDWithMetrics(ownerID)
}

func (s *NodeService) GetByID(id string) (*Node, error) {
	return s.repo.FindByID(id)
}

func (s *NodeService) Update(id, hostname string) (*Node, error) {
	n, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, ErrNodeNotFound
	}

	if hostname != "" {
		n.Hostname = hostname
	}

	return s.repo.Update(n)
}

func (s *NodeService) Delete(id string) error {
	if id == "" {
		return ErrInvalidNodeData
	}
	_, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	return s.repo.Delete(id)
}

func (s *NodeService) AssignToProject(nodeID, projectID string) error {
	if nodeID == "" || projectID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.AssignProject(nodeID, &projectID)
}

func (s *NodeService) RemoveFromProject(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.AssignProject(nodeID, nil)
}

func (s *NodeService) FindLocalNode() (*Node, error) {
	return s.repo.FindLocalNode()
}

func (s *NodeService) ClaimUnownedNodes(ownerID string) error {
	if ownerID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.ClaimUnownedNodes(ownerID)
}
