// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	caddyroute "github.com/arturo/autohost-cloud-api/internal/domain/caddy_route"
)

// CaddyRouteRepository implements caddyroute.Repository using PostgreSQL.
type CaddyRouteRepository struct {
	db *sqlx.DB
}

func NewCaddyRouteRepository(db *sqlx.DB) *CaddyRouteRepository {
	return &CaddyRouteRepository{db: db}
}

func (r *CaddyRouteRepository) Upsert(cr *caddyroute.CaddyRoute) (*caddyroute.CaddyRoute, error) {
	var result caddyroute.CaddyRoute
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO caddy_routes (node_id, user_id, domain, upstream)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (node_id, domain) DO UPDATE
			SET upstream   = EXCLUDED.upstream,
			    updated_at = NOW()
		RETURNING id, node_id, user_id, domain, upstream, created_at, updated_at`,
		cr.NodeID, cr.UserID, cr.Domain, cr.Upstream,
	).Scan(&result.ID, &result.NodeID, &result.UserID, &result.Domain, &result.Upstream, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *CaddyRouteRepository) ListByNode(nodeID string) ([]*caddyroute.CaddyRoute, error) {
	var routes []*caddyroute.CaddyRoute
	err := r.db.SelectContext(context.Background(), &routes, `
		SELECT id, node_id, user_id, domain, upstream, created_at, updated_at
		FROM caddy_routes
		WHERE node_id = $1
		ORDER BY domain ASC`, nodeID)
	if err != nil {
		return nil, err
	}
	return routes, nil
}

func (r *CaddyRouteRepository) Delete(nodeID, domain string) error {
	res, err := r.db.ExecContext(context.Background(), `
		DELETE FROM caddy_routes WHERE node_id = $1 AND domain = $2`, nodeID, domain)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return caddyroute.ErrNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		return caddyroute.ErrNotFound
	}
	return nil
}
