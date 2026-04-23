package postgres

import (
	"context"
	"database/sql"

	"github.com/arturo/autohost-cloud-api/internal/domain/project"
	"github.com/jmoiron/sqlx"
)

// ProjectRepository implements project.Repository using PostgreSQL.
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

func (r *ProjectRepository) Create(p *project.Project) (*project.Project, error) {
	var m project.Project
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

func (r *ProjectRepository) FindByID(id string) (*project.Project, error) {
	var m project.Project
	err := r.db.GetContext(context.Background(), &m, `
		SELECT id, user_id, name, created_at, updated_at
		FROM projects WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, project.ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *ProjectRepository) FindByUserID(userID string) ([]*project.Project, error) {
	var projects []*project.Project
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

func (r *ProjectRepository) Update(p *project.Project) (*project.Project, error) {
	var m project.Project
	err := r.db.QueryRowContext(context.Background(), `
		UPDATE projects
		SET name = $1, updated_at = NOW()
		WHERE id = $2
		RETURNING id, user_id, name, created_at, updated_at`,
		p.Name, p.ID,
	).Scan(&m.ID, &m.UserID, &m.Name, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, project.ErrProjectNotFound
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
