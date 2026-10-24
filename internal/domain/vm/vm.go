// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package vm

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("vm not found")
	ErrAlreadyExists = errors.New("vm with this name already exists on host")
	ErrInvalidSpecs  = errors.New("invalid vm specifications")
	ErrUnauthorized  = errors.New("unauthorized")
)

type VMStatus string

const (
	StatusCreating  VMStatus = "creating"
	StatusRunning   VMStatus = "running"
	StatusStopped   VMStatus = "stopped"
	StatusError     VMStatus = "error"
	StatusDestroyed VMStatus = "destroyed"
)

type VM struct {
	ID             string    `db:"id"               json:"id"`
	HostNodeID     string    `db:"host_node_id"     json:"host_node_id"`
	NodeID         *string   `db:"node_id"          json:"node_id,omitempty"`
	OwnerID        string    `db:"owner_id"         json:"owner_id"`
	Name           string    `db:"name"             json:"name"`
	VCPUs          int       `db:"vcpus"            json:"vcpus"`
	RAMMB          int       `db:"ram_mb"           json:"ram_mb"`
	DiskGB         int       `db:"disk_gb"          json:"disk_gb"`
	Image          string    `db:"image"            json:"image"`
	Status         string    `db:"status"           json:"status"`
	IPAddress      string    `db:"ip_address"       json:"ip_address"`
	CloudInitUser  string    `db:"cloud_init_user"  json:"cloud_init_user"`
	ErrorMessage   string    `db:"error_message"    json:"error_message,omitempty"`
	CreatedAt      time.Time `db:"created_at"       json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"       json:"updated_at"`
}

type Repository interface {
	Create(ctx context.Context, v *VM) (*VM, error)
	GetByID(ctx context.Context, id string) (*VM, error)
	GetByHostAndName(ctx context.Context, hostNodeID, name string) (*VM, error)
	ListByHost(ctx context.Context, hostNodeID string) ([]*VM, error)
	ListByOwner(ctx context.Context, ownerID string) ([]*VM, error)
	Update(ctx context.Context, v *VM) (*VM, error)
	Delete(ctx context.Context, id string) error
	LinkNodeByHostname(ctx context.Context, ownerID, hostname, nodeID, ipLocal string) error
}
