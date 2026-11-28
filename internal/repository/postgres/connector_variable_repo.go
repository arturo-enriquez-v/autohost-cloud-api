// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
)

// ConnectorVariableRepository implements connectors.VariableRepository.
type ConnectorVariableRepository struct {
	db *sqlx.DB
}

func NewConnectorVariableRepository(db *sqlx.DB) *ConnectorVariableRepository {
	return &ConnectorVariableRepository{db: db}
}

const connectorVariableColumns = `id, project_id, name, description, secret, value, value_ciphertext,
	allowed_origins, COALESCE(created_by::text, '') AS created_by, created_at, updated_at`

type connectorVariableRow struct {
	ID              string         `db:"id"`
	ProjectID       string         `db:"project_id"`
	Name            string         `db:"name"`
	Description     string         `db:"description"`
	Secret          bool           `db:"secret"`
	Value           string         `db:"value"`
	ValueCiphertext string         `db:"value_ciphertext"`
	AllowedOrigins  pq.StringArray `db:"allowed_origins"`
	CreatedBy       string         `db:"created_by"`
	CreatedAt       time.Time      `db:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
}

func (r connectorVariableRow) toDomain() *connectors.Variable {
	origins := []string(r.AllowedOrigins)
	if origins == nil {
		origins = []string{}
	}
	return &connectors.Variable{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Description: r.Description, Secret: r.Secret,
		Value: r.Value, ValueCipher: r.ValueCiphertext, HasValue: !r.Secret || r.ValueCiphertext != "",
		AllowedOrigins: origins, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func mapVariableErr(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return connectors.ErrVariableExists
	}
	if errors.Is(err, sql.ErrNoRows) {
		return connectors.ErrVariableNotFound
	}
	return err
}

func (r *ConnectorVariableRepository) ListVariables(ctx context.Context, projectID string) ([]*connectors.Variable, error) {
	var rows []connectorVariableRow
	if err := r.db.SelectContext(ctx, &rows,
		`SELECT `+connectorVariableColumns+` FROM connector_variables WHERE project_id = $1 ORDER BY name`, projectID); err != nil {
		return nil, err
	}
	out := make([]*connectors.Variable, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

func (r *ConnectorVariableRepository) GetVariable(ctx context.Context, id string) (*connectors.Variable, error) {
	var row connectorVariableRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+connectorVariableColumns+` FROM connector_variables WHERE id = $1`, id); err != nil {
		return nil, mapVariableErr(err)
	}
	return row.toDomain(), nil
}

func (r *ConnectorVariableRepository) CreateVariable(ctx context.Context, v *connectors.Variable) (*connectors.Variable, error) {
	var row connectorVariableRow
	err := r.db.GetContext(ctx, &row, `
		INSERT INTO connector_variables (id, project_id, name, description, secret, value, value_ciphertext,
			allowed_origins, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+connectorVariableColumns,
		v.ID, v.ProjectID, v.Name, v.Description, v.Secret, v.Value, v.ValueCipher,
		pq.Array(v.AllowedOrigins), nullableUUID(v.CreatedBy))
	if err != nil {
		return nil, mapVariableErr(err)
	}
	return row.toDomain(), nil
}

func (r *ConnectorVariableRepository) UpdateVariable(ctx context.Context, v *connectors.Variable) (*connectors.Variable, error) {
	var row connectorVariableRow
	err := r.db.GetContext(ctx, &row, `
		UPDATE connector_variables SET name = $2, description = $3, secret = $4, value = $5,
			value_ciphertext = $6, allowed_origins = $7, updated_at = NOW()
		WHERE id = $1
		RETURNING `+connectorVariableColumns,
		v.ID, v.Name, v.Description, v.Secret, v.Value, v.ValueCipher, pq.Array(v.AllowedOrigins))
	if err != nil {
		return nil, mapVariableErr(err)
	}
	return row.toDomain(), nil
}

func (r *ConnectorVariableRepository) DeleteVariable(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM connector_variables WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return connectors.ErrVariableNotFound
	}
	return nil
}
