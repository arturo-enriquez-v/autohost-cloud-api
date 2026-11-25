// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
)

// ConnectorRepository implements connectors.Repository.
type ConnectorRepository struct {
	db *sqlx.DB
}

func NewConnectorRepository(db *sqlx.DB) *ConnectorRepository {
	return &ConnectorRepository{db: db}
}

const connectorColumns = `id, project_id, name, description, kind, method, url, headers, body_type, body,
	params, auth, secret_ciphertext, response_mapping, cache_ttl_seconds, timeout_seconds,
	COALESCE(created_by::text, '') AS created_by, created_at, updated_at`

type connectorRow struct {
	ID               string    `db:"id"`
	ProjectID        string    `db:"project_id"`
	Name             string    `db:"name"`
	Description      string    `db:"description"`
	Kind             string    `db:"kind"`
	Method           string    `db:"method"`
	URL              string    `db:"url"`
	Headers          []byte    `db:"headers"`
	BodyType         string    `db:"body_type"`
	Body             string    `db:"body"`
	Params           []byte    `db:"params"`
	Auth             []byte    `db:"auth"`
	SecretCiphertext string    `db:"secret_ciphertext"`
	ResponseMapping  []byte    `db:"response_mapping"`
	CacheTTLSeconds  int       `db:"cache_ttl_seconds"`
	TimeoutSeconds   int       `db:"timeout_seconds"`
	CreatedBy        string    `db:"created_by"`
	CreatedAt        time.Time `db:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"`
}

func (r connectorRow) toDomain() (*connectors.Connector, error) {
	c := &connectors.Connector{
		ID: r.ID,
		Definition: connectors.Definition{
			ProjectID: r.ProjectID, Name: r.Name, Description: r.Description, Kind: r.Kind,
			Method: r.Method, URL: r.URL, BodyType: r.BodyType, Body: r.Body,
			CacheTTLSeconds: r.CacheTTLSeconds, TimeoutSeconds: r.TimeoutSeconds,
		},
		SecretCiphertext: r.SecretCiphertext,
		HasSecret:        r.SecretCiphertext != "",
		CreatedBy:        r.CreatedBy,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
	for _, f := range []struct {
		raw []byte
		dst any
	}{{r.Headers, &c.Headers}, {r.Params, &c.Params}, {r.Auth, &c.Auth}, {r.ResponseMapping, &c.Response}} {
		if len(f.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(f.raw, f.dst); err != nil {
			return nil, err
		}
	}
	c.Normalize()
	return c, nil
}

type connectorJSON struct {
	headers, params, auth, response []byte
}

func marshalConnector(c *connectors.Connector) (connectorJSON, error) {
	var out connectorJSON
	var err error
	if out.headers, err = json.Marshal(c.Headers); err != nil {
		return out, err
	}
	if out.params, err = json.Marshal(c.Params); err != nil {
		return out, err
	}
	if out.auth, err = json.Marshal(c.Auth); err != nil {
		return out, err
	}
	out.response, err = json.Marshal(c.Response)
	return out, err
}

func mapConnectorErr(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return connectors.ErrNameExists
	}
	if errors.Is(err, sql.ErrNoRows) {
		return connectors.ErrNotFound
	}
	return err
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *ConnectorRepository) Create(ctx context.Context, c *connectors.Connector) (*connectors.Connector, error) {
	j, err := marshalConnector(c)
	if err != nil {
		return nil, err
	}
	var row connectorRow
	err = r.db.GetContext(ctx, &row, `
		INSERT INTO connectors (id, project_id, name, description, kind, method, url, headers, body_type, body,
			params, auth, secret_ciphertext, response_mapping, cache_ttl_seconds, timeout_seconds, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING `+connectorColumns,
		c.ID, c.ProjectID, c.Name, c.Description, c.Kind, c.Method, c.URL, j.headers, c.BodyType, c.Body,
		j.params, j.auth, c.SecretCiphertext, j.response, c.CacheTTLSeconds, c.TimeoutSeconds, nullableUUID(c.CreatedBy))
	if err != nil {
		return nil, mapConnectorErr(err)
	}
	return row.toDomain()
}

func (r *ConnectorRepository) Get(ctx context.Context, id string) (*connectors.Connector, error) {
	var row connectorRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+connectorColumns+` FROM connectors WHERE id = $1`, id); err != nil {
		return nil, mapConnectorErr(err)
	}
	return row.toDomain()
}

func (r *ConnectorRepository) list(ctx context.Context, query string, args ...any) ([]*connectors.Connector, error) {
	var rows []connectorRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	out := make([]*connectors.Connector, 0, len(rows))
	for _, row := range rows {
		c, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *ConnectorRepository) ListByProjects(ctx context.Context, projectIDs []string) ([]*connectors.Connector, error) {
	return r.list(ctx, `SELECT `+connectorColumns+` FROM connectors WHERE project_id = ANY($1::uuid[]) ORDER BY name`, pq.Array(projectIDs))
}

func (r *ConnectorRepository) ListAll(ctx context.Context) ([]*connectors.Connector, error) {
	return r.list(ctx, `SELECT `+connectorColumns+` FROM connectors ORDER BY name`)
}

func (r *ConnectorRepository) Update(ctx context.Context, c *connectors.Connector) (*connectors.Connector, error) {
	j, err := marshalConnector(c)
	if err != nil {
		return nil, err
	}
	var row connectorRow
	err = r.db.GetContext(ctx, &row, `
		UPDATE connectors SET name = $2, description = $3, kind = $4, method = $5, url = $6, headers = $7,
			body_type = $8, body = $9, params = $10, auth = $11, secret_ciphertext = $12, response_mapping = $13,
			cache_ttl_seconds = $14, timeout_seconds = $15, updated_at = NOW()
		WHERE id = $1
		RETURNING `+connectorColumns,
		c.ID, c.Name, c.Description, c.Kind, c.Method, c.URL, j.headers, c.BodyType, c.Body, j.params, j.auth,
		c.SecretCiphertext, j.response, c.CacheTTLSeconds, c.TimeoutSeconds)
	if err != nil {
		return nil, mapConnectorErr(err)
	}
	return row.toDomain()
}

func (r *ConnectorRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM connectors WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return connectors.ErrNotFound
	}
	return nil
}

func (r *ConnectorRepository) InsertRun(ctx context.Context, run *connectors.Run, keep int) error {
	params, err := json.Marshal(run.Params)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO connector_runs (connector_id, user_id, params, status_code, ok, error, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		run.ConnectorID, nullableUUID(run.UserID), params, run.StatusCode, run.OK, run.Error, run.DurationMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM connector_runs
		WHERE connector_id = $1 AND id NOT IN (
			SELECT id FROM connector_runs WHERE connector_id = $1 ORDER BY created_at DESC LIMIT $2
		)`, run.ConnectorID, keep); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ConnectorRepository) ListRuns(ctx context.Context, connectorID string, limit int) ([]*connectors.Run, error) {
	var rows []struct {
		ID          string    `db:"id"`
		ConnectorID string    `db:"connector_id"`
		UserID      string    `db:"user_id"`
		Params      []byte    `db:"params"`
		StatusCode  int       `db:"status_code"`
		OK          bool      `db:"ok"`
		Error       string    `db:"error"`
		DurationMS  int64     `db:"duration_ms"`
		CreatedAt   time.Time `db:"created_at"`
	}
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, connector_id, COALESCE(user_id::text, '') AS user_id, params, status_code, ok, error,
			duration_ms, created_at
		FROM connector_runs WHERE connector_id = $1
		ORDER BY created_at DESC LIMIT $2`, connectorID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*connectors.Run, 0, len(rows))
	for _, row := range rows {
		run := &connectors.Run{
			ID: row.ID, ConnectorID: row.ConnectorID, UserID: row.UserID, StatusCode: row.StatusCode,
			OK: row.OK, Error: row.Error, DurationMS: row.DurationMS, CreatedAt: row.CreatedAt,
			Params: map[string]string{},
		}
		_ = json.Unmarshal(row.Params, &run.Params)
		out = append(out, run)
	}
	return out, nil
}
