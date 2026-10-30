// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/jmoiron/sqlx"
)

// ProjectRepository implements apps.ProjectRepository using PostgreSQL.
type ProjectRepository struct {
	db *sqlx.DB
}

func NewProjectRepository(db *sqlx.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

type projectModel struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	Name      string `db:"name"`
	CreatedAt string `db:"created_at"`
	UpdatedAt string `db:"updated_at"`
}

func (r *ProjectRepository) Create(p *apps.Project) (*apps.Project, error) {
	var m apps.Project
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO projects (user_id, name)
		VALUES ($1, $2)
		RETURNING id, user_id, name, created_at, updated_at`,
		p.UserID, p.Name,
	).Scan(&m.ID, &m.UserID, &m.Name, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *ProjectRepository) FindByID(id string) (*apps.Project, error) {
	var m apps.Project
	err := r.db.GetContext(context.Background(), &m, `
		SELECT id, user_id, name, created_at, updated_at
		FROM projects WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, apps.ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *ProjectRepository) FindByUserID(userID string) ([]*apps.Project, error) {
	var projects []*apps.Project
	err := r.db.SelectContext(context.Background(), &projects, `
		SELECT id, user_id, name, created_at, updated_at
		FROM projects
		WHERE user_id = $1
		ORDER BY name ASC`, userID)
	if err != nil {
		return nil, err
	}
	return projects, nil
}

func (r *ProjectRepository) Update(p *apps.Project) (*apps.Project, error) {
	var m apps.Project
	err := r.db.QueryRowContext(context.Background(), `
		UPDATE projects
		SET name = $1, updated_at = NOW()
		WHERE id = $2
		RETURNING id, user_id, name, created_at, updated_at`,
		p.Name, p.ID,
	).Scan(&m.ID, &m.UserID, &m.Name, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, apps.ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *ProjectRepository) Delete(id string) error {
	_, err := r.db.ExecContext(context.Background(), `
		DELETE FROM projects WHERE id = $1`, id)
	return err
}
