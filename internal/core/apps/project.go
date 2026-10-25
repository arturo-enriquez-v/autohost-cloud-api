// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

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

// ProjectRepository defines persistence operations for projects.
type ProjectRepository interface {
	Create(p *Project) (*Project, error)
	FindByID(id string) (*Project, error)
	FindByUserID(userID string) ([]*Project, error)
	Update(p *Project) (*Project, error)
	Delete(id string) error
}

// ProjectService handles business logic for projects.
type ProjectService struct {
	repo ProjectRepository
}

func NewProjectService(repo ProjectRepository) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) Create(userID, name string) (*Project, error) {
	if name == "" {
		return nil, ErrInvalidProject
	}
	p := &Project{UserID: userID, Name: name}
	return s.repo.Create(p)
}

func (s *ProjectService) GetByID(id string) (*Project, error) {
	return s.repo.FindByID(id)
}

func (s *ProjectService) GetByUser(userID string) ([]*Project, error) {
	return s.repo.FindByUserID(userID)
}

func (s *ProjectService) Update(id, userID, name string) (*Project, error) {
	p, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if p.UserID != userID {
		return nil, ErrUnauthorized
	}
	if name != "" {
		p.Name = name
	}
	return s.repo.Update(p)
}

func (s *ProjectService) Delete(id, userID string) error {
	p, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if p.UserID != userID {
		return ErrUnauthorized
	}
	return s.repo.Delete(id)
}
