// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package containersetting

import (
	"context"
	"time"
)

// ContainerSetting holds display order and lock status for containers on a node.
type ContainerSetting struct {
	ID               string          `db:"id" json:"id"`
	NodeID           string          `db:"node_id" json:"node_id"`
	SortOrder        []string        `db:"sort_order" json:"sort_order"`
	LockedContainers map[string]bool `db:"locked_containers" json:"locked_containers"`
	CreatedAt        time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time       `db:"updated_at" json:"updated_at"`
}

// Repository defines persistence operations for container settings.
type Repository interface {
	GetByNodeID(ctx context.Context, nodeID string) (*ContainerSetting, error)
	Upsert(ctx context.Context, setting *ContainerSetting) (*ContainerSetting, error)
	IsContainerLocked(ctx context.Context, nodeID, containerName string) (bool, error)
}
