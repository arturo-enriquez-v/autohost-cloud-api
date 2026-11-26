// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"time"

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

const nodeSelectColumns = `
	n.id, n.hostname, COALESCE(n.ip_local::text, '') as ip_local, n.ip_vpn, n.os, n.arch, n.version_agent,
	n.owner_id, n.project_id, n.is_local, n.last_seen_at, n.created_at, n.updated_at,
	(v.id IS NOT NULL) AS is_vm, v.id AS vm_id, v.host_node_id AS host_node_id, hn.hostname AS host_hostname`

const nodeVMJoinClause = `
	LEFT JOIN LATERAL (
		SELECT id, host_node_id
		FROM vms
		WHERE node_id = n.id
		ORDER BY created_at DESC
		LIMIT 1
	) v ON true
	LEFT JOIN nodes hn ON hn.id = v.host_node_id`

func modelToNode(model NodeModel) *node.Node {
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
		IsVM:         model.IsVM,
		VMID:         model.VMID,
		HostNodeID:   model.HostNodeID,
		HostHostname: model.HostHostname,
	}
}

// Register crea un nuevo nodo
func (r *NodeRepository) Register(n *node.Node) (*node.Node, error) {
	var model NodeModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO nodes (hostname, ip_local, ip_vpn, os, arch, version_agent, owner_id, is_local)
		VALUES ($1, NULLIF($2, '')::inet, NULLIF($3, '')::inet, $4, $5, $6, $7, $8)
		ON CONFLICT (owner_id, hostname) DO UPDATE
		SET ip_local = COALESCE(EXCLUDED.ip_local, nodes.ip_local),
		    ip_vpn = COALESCE(EXCLUDED.ip_vpn, nodes.ip_vpn),
		    os = COALESCE(NULLIF(EXCLUDED.os, ''), nodes.os),
		    arch = COALESCE(NULLIF(EXCLUDED.arch, ''), nodes.arch),
		    version_agent = COALESCE(NULLIF(EXCLUDED.version_agent, ''), nodes.version_agent),
		    updated_at = now()
		RETURNING id, hostname, COALESCE(ip_local::text, ''), ip_vpn, os, arch, version_agent, owner_id, project_id, is_local, last_seen_at, created_at, updated_at`,
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
		SELECT `+nodeSelectColumns+`
		FROM nodes n
		`+nodeVMJoinClause+`
		WHERE n.id = $1`, id)

	if err == sql.ErrNoRows {
		return nil, node.ErrNodeNotFound
	}
	if err != nil {
		return nil, err
	}

	return modelToNode(model), nil
}

func (r *NodeRepository) FindLocalNode() (*node.Node, error) {
	var model NodeModel
	err := r.db.Get(&model, `
		SELECT `+nodeSelectColumns+`
		FROM nodes n
		`+nodeVMJoinClause+`
		WHERE n.is_local = true
		ORDER BY n.created_at ASC
		LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return modelToNode(model), nil
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
		SELECT `+nodeSelectColumns+`
		FROM nodes n
		`+nodeVMJoinClause+`
		WHERE n.owner_id = $1
		ORDER BY n.is_local DESC, n.created_at DESC`, ownerID)

	if err != nil {
		return nil, err
	}

	nodes := make([]*node.Node, len(models))
	for i, model := range models {
		nodes[i] = modelToNode(model)
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
	return r.queryNodesWithMetrics(`WHERE n.owner_id = $1`, ownerID)
}

func (r *NodeRepository) queryNodesWithMetrics(whereClause string, args ...any) ([]*node.NodeWithMetrics, error) {
	var results []*node.NodeWithMetrics

	query := `
		SELECT ` + nodeSelectColumns + `,
			m.cpu_usage_percent, m.memory_usage_percent, m.disk_usage_percent, m.cpu_temp_celsius, m.uptime_seconds, m.collected_at
		FROM nodes n
		` + nodeVMJoinClause + `
		LEFT JOIN LATERAL (
			SELECT cpu_usage_percent, memory_usage_percent, disk_usage_percent, cpu_temp_celsius, uptime_seconds, collected_at
			FROM node_metrics
			WHERE node_id = n.id
			ORDER BY collected_at DESC
			LIMIT 1
		) m ON true
		` + whereClause + `
		ORDER BY n.is_local DESC, n.created_at DESC
	`

	rows, err := r.db.Queryx(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var nwm node.NodeWithMetrics
		var cpuUsage, memUsage, diskUsage, cpuTemp sql.NullFloat64
		var uptime sql.NullInt64
		var collectedAt sql.NullTime

		err := rows.Scan(
			&nwm.ID, &nwm.Hostname, &nwm.IPLocal, &nwm.IPVPN, &nwm.OS, &nwm.Arch,
			&nwm.VersionAgent, &nwm.OwnerID, &nwm.ProjectID, &nwm.IsLocal, &nwm.LastSeenAt,
			&nwm.CreatedAt, &nwm.UpdatedAt,
			&nwm.IsVM, &nwm.VMID, &nwm.HostNodeID, &nwm.HostHostname,
			&cpuUsage, &memUsage, &diskUsage, &cpuTemp, &uptime, &collectedAt,
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
				UptimeSeconds:      nullInt64Ptr(uptime),
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

// FindAll returns all nodes registered in the platform.
func (r *NodeRepository) FindAll() ([]*node.Node, error) {
	var models []NodeModel
	err := r.db.Select(&models, `
		SELECT `+nodeSelectColumns+`
		FROM nodes n
		`+nodeVMJoinClause+`
		ORDER BY n.is_local DESC, n.created_at DESC`)
	if err != nil {
		return nil, err
	}
	nodes := make([]*node.Node, len(models))
	for i, model := range models {
		nodes[i] = modelToNode(model)
	}
	return nodes, nil
}

// FindLatestMetric returns the node's most recent metric sample, or nil when it has none.
func (r *NodeRepository) FindLatestMetric(nodeID string) (*node.LastMetric, error) {
	var cpu, mem, disk, cpuTemp sql.NullFloat64
	var uptime sql.NullInt64
	var collectedAt time.Time
	err := r.db.QueryRowContext(context.Background(), `
		SELECT cpu_usage_percent, memory_usage_percent, disk_usage_percent, cpu_temp_celsius, uptime_seconds, collected_at
		FROM node_metrics
		WHERE node_id = $1
		ORDER BY collected_at DESC
		LIMIT 1`, nodeID).Scan(&cpu, &mem, &disk, &cpuTemp, &uptime, &collectedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &node.LastMetric{
		CPUUsagePercent:    cpu.Float64,
		MemoryUsagePercent: mem.Float64,
		DiskUsagePercent:   disk.Float64,
		CPUTempCelsius:     cpuTemp.Float64,
		UptimeSeconds:      nullInt64Ptr(uptime),
		CollectedAt:        collectedAt,
	}, nil
}

// FindAllWithMetrics returns all nodes in the platform with their latest metric snapshot.
func (r *NodeRepository) FindAllWithMetrics() ([]*node.NodeWithMetrics, error) {
	return r.queryNodesWithMetrics("")
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
