// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/infra"
)

type VMRepository struct {
	db *sqlx.DB
}

func NewVMRepository(db *sqlx.DB) *VMRepository {
	return &VMRepository{db: db}
}

func (r *VMRepository) Create(ctx context.Context, v *infra.VM) (*infra.VM, error) {
	var result infra.VM
	query := `
		INSERT INTO vms (
			host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
			image, status, ip_address, cloud_init_user, error_message
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		          image, status, ip_address, cloud_init_user, error_message, created_at, updated_at`

	err := r.db.QueryRowContext(ctx, query,
		v.HostNodeID, v.NodeID, v.OwnerID, v.Name, v.VCPUs, v.RAMMB, v.DiskGB,
		v.Image, v.Status, v.IPAddress, v.CloudInitUser, v.ErrorMessage,
	).Scan(
		&result.ID, &result.HostNodeID, &result.NodeID, &result.OwnerID, &result.Name,
		&result.VCPUs, &result.RAMMB, &result.DiskGB, &result.Image, &result.Status,
		&result.IPAddress, &result.CloudInitUser, &result.ErrorMessage,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *VMRepository) GetByID(ctx context.Context, id string) (*infra.VM, error) {
	var result infra.VM
	query := `
		SELECT id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		       image, status, ip_address, cloud_init_user, error_message, created_at, updated_at
		FROM vms
		WHERE id = $1`

	err := r.db.GetContext(ctx, &result, query, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infra.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *VMRepository) GetByHostAndName(ctx context.Context, hostNodeID, name string) (*infra.VM, error) {
	var result infra.VM
	query := `
		SELECT id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		       image, status, ip_address, cloud_init_user, error_message, created_at, updated_at
		FROM vms
		WHERE host_node_id = $1 AND name = $2`

	err := r.db.GetContext(ctx, &result, query, hostNodeID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infra.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *VMRepository) ListByHost(ctx context.Context, hostNodeID string) ([]*infra.VM, error) {
	var list []*infra.VM
	query := `
		SELECT id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		       image, status, ip_address, cloud_init_user, error_message, created_at, updated_at
		FROM vms
		WHERE host_node_id = $1
		ORDER BY created_at DESC`

	err := r.db.SelectContext(ctx, &list, query, hostNodeID)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *VMRepository) ListByOwner(ctx context.Context, ownerID string) ([]*infra.VM, error) {
	var list []*infra.VM
	query := `
		SELECT id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		       image, status, ip_address, cloud_init_user, error_message, created_at, updated_at
		FROM vms
		WHERE owner_id = $1
		ORDER BY created_at DESC`

	err := r.db.SelectContext(ctx, &list, query, ownerID)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *VMRepository) Update(ctx context.Context, v *infra.VM) (*infra.VM, error) {
	var result infra.VM
	query := `
		UPDATE vms
		SET node_id = $1, status = $2, ip_address = $3, error_message = $4, updated_at = NOW()
		WHERE id = $5
		RETURNING id, host_node_id, node_id, owner_id, name, vcpus, ram_mb, disk_gb,
		          image, status, ip_address, cloud_init_user, error_message, created_at, updated_at`

	err := r.db.QueryRowContext(ctx, query,
		v.NodeID, v.Status, v.IPAddress, v.ErrorMessage, v.ID,
	).Scan(
		&result.ID, &result.HostNodeID, &result.NodeID, &result.OwnerID, &result.Name,
		&result.VCPUs, &result.RAMMB, &result.DiskGB, &result.Image, &result.Status,
		&result.IPAddress, &result.CloudInitUser, &result.ErrorMessage,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infra.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *VMRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM vms WHERE id = $1`, id)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return infra.ErrNotFound
	}
	return nil
}

func (r *VMRepository) LinkNodeByHostname(ctx context.Context, ownerID, hostname, nodeID, ipLocal string) error {
	query := `
		UPDATE vms
		SET node_id = $1, status = 'running', ip_address = COALESCE(NULLIF($2, ''), ip_address), updated_at = NOW()
		WHERE owner_id = $3 AND name = $4 AND node_id IS NULL`
	_, err := r.db.ExecContext(ctx, query, nodeID, ipLocal, ownerID, hostname)
	return err
}
