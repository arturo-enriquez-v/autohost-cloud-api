// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/jmoiron/sqlx"
)

// NodeRepository implementa node.Repository usando PostgreSQL
type NodeRepository struct {
	db *sqlx.DB
}

// NewNodeRepository crea una nueva instancia del repositorio
func NewNodeRepository(db *sqlx.DB) *NodeRepository {
	return &NodeRepository{db: db}
}

// Register crea un nuevo nodo
func (r *NodeRepository) Register(n *node.Node) (*node.Node, error) {
	var model NodeModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO nodes (hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, is_local)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, project_id, is_local, last_seen_at, created_at, updated_at`,
		n.Hostname, n.IPLocal, n.IPVPN, n.OS, n.Arch, n.VersionAgent, n.OwnerID, n.IsLocal).Scan(
		&model.ID,
		&model.Hostname,
		&model.IPLocal,
		&model.IPVPN,
		&model.OS,
		&model.Arch,
		&model.VersionAgent,
		&model.OwnerID,
		&model.ProjectID,
		&model.IsLocal,
		&model.LastSeenAt,
		&model.CreatedAt,
		&model.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Actualizar el nodo con los datos devueltos
	n.ID = model.ID
	n.ProjectID = model.ProjectID
	n.IsLocal = model.IsLocal
	n.LastSeenAt = model.LastSeenAt
	n.CreatedAt = model.CreatedAt
	n.UpdatedAt = model.UpdatedAt

	return n, nil
}

func (r *NodeRepository) Update(node *node.Node) (*node.Node, error) {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes 
		SET hostname = $1, updated_at = now()
		WHERE id = $2`, node.Hostname, node.ID)
	if err != nil {
		return nil, err
	}

	return node, nil
}

func (r *NodeRepository) FindByID(id string) (*node.Node, error) {
	var model NodeModel

	err := r.db.Get(&model, `
        SELECT id, hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, project_id, is_local,
               last_seen_at, created_at, updated_at
        FROM nodes 
        WHERE id = $1`, id)

	if err == sql.ErrNoRows {
		return nil, node.ErrNodeNotFound
	}
	if err != nil {
		return nil, err
	}

	return &node.Node{
		ID:           model.ID,
		Hostname:     model.Hostname,
		IPLocal:      model.IPLocal,
		IPVPN:        model.IPVPN,
		OS:           model.OS,
		Arch:         model.Arch,
		VersionAgent: model.VersionAgent,
		OwnerID:      model.OwnerID,
		ProjectID:    model.ProjectID,
		IsLocal:      model.IsLocal,
		LastSeenAt:   model.LastSeenAt,
		CreatedAt:    model.CreatedAt,
		UpdatedAt:    model.UpdatedAt,
	}, nil
}

func (r *NodeRepository) FindLocalNode() (*node.Node, error) {
	var model NodeModel
	err := r.db.Get(&model, `
		SELECT id, hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, project_id, is_local,
		       last_seen_at, created_at, updated_at
		FROM nodes
		WHERE is_local = true
		ORDER BY created_at ASC
		LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &node.Node{
		ID:           model.ID,
		Hostname:     model.Hostname,
		IPLocal:      model.IPLocal,
		IPVPN:        model.IPVPN,
		OS:           model.OS,
		Arch:         model.Arch,
		VersionAgent: model.VersionAgent,
		OwnerID:      model.OwnerID,
		ProjectID:    model.ProjectID,
		IsLocal:      model.IsLocal,
		LastSeenAt:   model.LastSeenAt,
		CreatedAt:    model.CreatedAt,
		UpdatedAt:    model.UpdatedAt,
	}, nil
}

func (r *NodeRepository) ClaimUnownedNodes(ownerID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes
		SET owner_id = $1, updated_at = now()
		WHERE owner_id IS NULL`, ownerID)
	return err
}

// FindByOwnerID busca todos los nodos de un propietario
func (r *NodeRepository) FindByOwnerID(ownerID string) ([]*node.Node, error) {
	var models []NodeModel
	err := r.db.Select(&models, `
		SELECT id, hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, project_id, is_local,
		       last_seen_at, created_at, updated_at
		FROM nodes 
		WHERE owner_id = $1
		ORDER BY is_local DESC, created_at DESC`, ownerID)

	if err != nil {
		return nil, err
	}

	nodes := make([]*node.Node, len(models))
	for i, model := range models {
		nodes[i] = &node.Node{
			ID:           model.ID,
			Hostname:     model.Hostname,
			IPLocal:      model.IPLocal,
			IPVPN:        model.IPVPN,
			OS:           model.OS,
			Arch:         model.Arch,
			VersionAgent: model.VersionAgent,
			OwnerID:      model.OwnerID,
			ProjectID:    model.ProjectID,
			IsLocal:      model.IsLocal,
			LastSeenAt:   model.LastSeenAt,
			CreatedAt:    model.CreatedAt,
			UpdatedAt:    model.UpdatedAt,
		}
	}

	return nodes, nil
}

func (r *NodeRepository) UpdateLastSeen(nodeID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes 
		SET last_seen_at = now()
		WHERE id = $1`, nodeID)
	return err
}

func (r *NodeRepository) UpdateLastSeenAndIPs(nodeID string, ipLocal string, ipVPN string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes 
		SET last_seen_at = now(), 
		    ip_local = COALESCE(NULLIF($1, ''), ip_local),
		    ip_vpn = COALESCE(NULLIF($2, ''), ip_vpn),
		    updated_at = now()
		WHERE id = $3`, ipLocal, ipVPN, nodeID)
	return err
}

func (r *NodeRepository) UpdateVPNIP(nodeID string, ipVPN string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes 
		SET ip_vpn = NULLIF($1, ''), updated_at = now()
		WHERE id = $2`, ipVPN, nodeID)
	return err
}

func (r *NodeRepository) UpdateVersionAgent(nodeID, version string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes
		SET version_agent = $1, updated_at = now()
		WHERE id = $2`, version, nodeID)
	return err
}

// FindByOwnerIDWithMetrics busca todos los nodos de un propietario con sus últimas métricas
func (r *NodeRepository) FindByOwnerIDWithMetrics(ownerID string) ([]*node.NodeWithMetrics, error) {
	var results []*node.NodeWithMetrics

	query := `
		SELECT 
			n.id, n.hostname, n.ip_local, n.ip_vpn, n.os, n.arch, n.version_agent, 
			n.owner_id, n.project_id, n.is_local, n.last_seen_at, n.created_at, n.updated_at,
			m.cpu_usage_percent, m.memory_usage_percent, m.disk_usage_percent, m.cpu_temp_celsius, m.collected_at
		FROM nodes n
		LEFT JOIN LATERAL (
			SELECT cpu_usage_percent, memory_usage_percent, disk_usage_percent, cpu_temp_celsius, collected_at
			FROM node_metrics
			WHERE node_id = n.id
			ORDER BY collected_at DESC
			LIMIT 1
		) m ON true
		WHERE n.owner_id = $1
		ORDER BY n.is_local DESC, n.created_at DESC
	`

	rows, err := r.db.Queryx(query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var nwm node.NodeWithMetrics
		var cpuUsage, memUsage, diskUsage, cpuTemp sql.NullFloat64
		var collectedAt sql.NullTime

		err := rows.Scan(
			&nwm.ID, &nwm.Hostname, &nwm.IPLocal, &nwm.IPVPN, &nwm.OS, &nwm.Arch,
			&nwm.VersionAgent, &nwm.OwnerID, &nwm.ProjectID, &nwm.IsLocal, &nwm.LastSeenAt,
			&nwm.CreatedAt, &nwm.UpdatedAt,
			&cpuUsage, &memUsage, &diskUsage, &cpuTemp, &collectedAt,
		)
		if err != nil {
			return nil, err
		}

		// Si hay métricas, agregarlas
		if cpuUsage.Valid {
			nwm.LastMetric = &node.LastMetric{
				CPUUsagePercent:    cpuUsage.Float64,
				MemoryUsagePercent: memUsage.Float64,
				DiskUsagePercent:   diskUsage.Float64,
				CPUTempCelsius:     cpuTemp.Float64,
				CollectedAt:        collectedAt.Time,
			}
		}

		results = append(results, &nwm)
	}

	return results, nil
}

func (r *NodeRepository) Delete(id string) error {
	_, err := r.db.ExecContext(context.Background(), `
		DELETE FROM nodes
		WHERE id = $1`, id)
	return err
}

// AssignProject sets (or clears) the project_id of a node.
func (r *NodeRepository) AssignProject(nodeID string, projectID *string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE nodes SET project_id = $1, updated_at = NOW() WHERE id = $2`,
		projectID, nodeID)
	return err
}
