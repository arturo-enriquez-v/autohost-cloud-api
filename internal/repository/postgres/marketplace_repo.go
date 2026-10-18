// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/marketplace"
	"github.com/jmoiron/sqlx"
)

// MarketplaceRepository implements marketplace.Repository using PostgreSQL.
type MarketplaceRepository struct {
	db *sqlx.DB
}

func NewMarketplaceRepository(db *sqlx.DB) *MarketplaceRepository {
	return &MarketplaceRepository{db: db}
}

type marketplaceAppModel struct {
	ID        string         `db:"id"`
	NodeID    string         `db:"node_id"`
	AppName   string         `db:"app_name"`
	JobID     sql.NullString `db:"job_id"`
	Status    string         `db:"status"`
	CreatedAt time.Time      `db:"created_at"`
	UpdatedAt time.Time      `db:"updated_at"`
}

func (r *MarketplaceRepository) Create(app *marketplace.MarketplaceApp) (*marketplace.MarketplaceApp, error) {
	var m marketplaceAppModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO marketplace_apps (node_id, app_name, status)
		VALUES ($1, $2, $3)
		ON CONFLICT (node_id, app_name)
		DO UPDATE SET status = 'installing', job_id = NULL, updated_at = NOW()
		RETURNING id, node_id, app_name, job_id, status, created_at, updated_at`,
		app.NodeID, app.AppName, app.Status,
	).Scan(&m.ID, &m.NodeID, &m.AppName, &m.JobID, &m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return modelToMarketplaceApp(m), nil
}

func (r *MarketplaceRepository) FindByNodeID(nodeID string) ([]*marketplace.MarketplaceApp, error) {
	var models []marketplaceAppModel
	err := r.db.SelectContext(context.Background(), &models,
		`SELECT id, node_id, app_name, job_id, status, created_at, updated_at
		 FROM marketplace_apps WHERE node_id = $1 ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	result := make([]*marketplace.MarketplaceApp, len(models))
	for i, m := range models {
		result[i] = modelToMarketplaceApp(m)
	}
	return result, nil
}

func (r *MarketplaceRepository) FindByJobID(jobID string) (*marketplace.MarketplaceApp, error) {
	var m marketplaceAppModel
	err := r.db.GetContext(context.Background(), &m,
		`SELECT id, node_id, app_name, job_id, status, created_at, updated_at
		 FROM marketplace_apps WHERE job_id = $1`, jobID)
	if err != nil {
		return nil, err
	}
	return modelToMarketplaceApp(m), nil
}

func (r *MarketplaceRepository) UpdateStatus(id string, status marketplace.AppStatus) error {
	_, err := r.db.ExecContext(context.Background(),
		`UPDATE marketplace_apps SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

func (r *MarketplaceRepository) UpdateJobID(id, jobID string) error {
	_, err := r.db.ExecContext(context.Background(),
		`UPDATE marketplace_apps SET job_id = $1, updated_at = NOW() WHERE id = $2`, jobID, id)
	return err
}

func modelToMarketplaceApp(m marketplaceAppModel) *marketplace.MarketplaceApp {
	app := &marketplace.MarketplaceApp{
		ID:        m.ID,
		NodeID:    m.NodeID,
		AppName:   m.AppName,
		Status:    marketplace.AppStatus(m.Status),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	if m.JobID.Valid {
		app.JobID = &m.JobID.String
	}
	return app
}
