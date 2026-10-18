// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package project

import (
	"errors"
	"time"
)

var (
	ErrProjectNotFound   = errors.New("project not found")
	ErrProjectNameExists = errors.New("project name already exists")
	ErrInvalidProject    = errors.New("invalid project data")
	ErrUnauthorized      = errors.New("unauthorized")
)

// Project represents a user-defined group of nodes.
type Project struct {
	ID        string    `db:"id"         json:"id"`
	UserID    string    `db:"user_id"    json:"user_id"`
	Name      string    `db:"name"        json:"name"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// Repository defines persistence operations for projects.
type Repository interface {
	Create(p *Project) (*Project, error)
	FindByID(id string) (*Project, error)
	FindByUserID(userID string) ([]*Project, error)
	Update(p *Project) (*Project, error)
	Delete(id string) error
}
