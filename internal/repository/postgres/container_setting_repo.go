// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

type ContainerSettingRepo struct {
	db *sqlx.DB
}

func NewContainerSettingRepository(db *sqlx.DB) *ContainerSettingRepo {
	return &ContainerSettingRepo{db: db}
}

func (r *ContainerSettingRepo) Upsert(ctx context.Context, s *node.ContainerSetting) (*node.ContainerSetting, error) {
	sortOrderJSON, err := json.Marshal(s.SortOrder)
	if err != nil {
		return nil, fmt.Errorf("marshal sort_order: %w", err)
	}

	locksJSON, err := json.Marshal(s.LockedContainers)
	if err != nil {
		return nil, fmt.Errorf("marshal locked_containers: %w", err)
	}

	query := `
		INSERT INTO node_container_settings (node_id, sort_order, locked_containers)
		VALUES ($1, $2, $3)
		ON CONFLICT (node_id) DO UPDATE SET
			sort_order        = EXCLUDED.sort_order,
			locked_containers = EXCLUDED.locked_containers,
			updated_at        = NOW()
	`

	if _, err := r.db.ExecContext(ctx, query, s.NodeID, sortOrderJSON, locksJSON); err != nil {
		return nil, fmt.Errorf("upsert container settings: %w", err)
	}

	return r.GetByNodeID(ctx, s.NodeID)
}

func (r *ContainerSettingRepo) GetByNodeID(ctx context.Context, nodeID string) (*node.ContainerSetting, error) {
	type row struct {
		ID               string    `db:"id"`
		NodeID           string    `db:"node_id"`
		SortOrder        []byte    `db:"sort_order"`
		LockedContainers []byte    `db:"locked_containers"`
		CreatedAt        time.Time `db:"created_at"`
		UpdatedAt        time.Time `db:"updated_at"`
	}

	var r0 row
	err := r.db.GetContext(ctx, &r0, `
		SELECT id, node_id, sort_order, locked_containers, created_at, updated_at
		FROM node_container_settings
		WHERE node_id = $1
	`, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("get container settings: %w", err)
	}

	res := &node.ContainerSetting{
		ID:               r0.ID,
		NodeID:           r0.NodeID,
		SortOrder:        []string{},
		LockedContainers: make(map[string]bool),
		CreatedAt:        r0.CreatedAt,
		UpdatedAt:        r0.UpdatedAt,
	}

	if len(r0.SortOrder) > 0 {
		_ = json.Unmarshal(r0.SortOrder, &res.SortOrder)
	}
	if len(r0.LockedContainers) > 0 {
		_ = json.Unmarshal(r0.LockedContainers, &res.LockedContainers)
	}

	return res, nil
}

func (r *ContainerSettingRepo) IsContainerLocked(ctx context.Context, nodeID, containerName string) (bool, error) {
	var locked bool
	query := `
		SELECT COALESCE((locked_containers->>$2)::boolean, false)
		FROM node_container_settings
		WHERE node_id = $1
	`
	err := r.db.GetContext(ctx, &locked, query, nodeID, containerName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return locked, nil
}
