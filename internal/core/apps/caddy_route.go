// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"errors"
	"time"
)

var (
	ErrCaddyRouteNotFound      = errors.New("caddy route not found")
	ErrCaddyRouteAlreadyExists = errors.New("caddy route already exists for this domain")
)

// CaddyRoute represents a Caddy reverse-proxy route managed by autohost.
type CaddyRoute struct {
	ID        string    `db:"id"         json:"id"`
	NodeID    string    `db:"node_id"    json:"node_id"`
	UserID    string    `db:"user_id"    json:"user_id"`
	Domain    string    `db:"domain"     json:"domain"`
	Upstream  string    `db:"upstream"   json:"upstream"`
	TLSMode   string    `db:"tls_mode"   json:"tls_mode"` // "auto", "internal", "http_only"
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// CaddyRouteRepository defines persistence operations for Caddy routes.
type CaddyRouteRepository interface {
	Upsert(r *CaddyRoute) (*CaddyRoute, error)
	ListByNode(nodeID string) ([]*CaddyRoute, error)
	Delete(nodeID, domain string) error
}

// CaddyRouteService contains the business logic for managing Caddy routes.
type CaddyRouteService struct {
	repo CaddyRouteRepository
}

func NewCaddyRouteService(repo CaddyRouteRepository) *CaddyRouteService {
	return &CaddyRouteService{repo: repo}
}

// Upsert creates or updates a Caddy route record.
func (s *CaddyRouteService) Upsert(r *CaddyRoute) (*CaddyRoute, error) {
	return s.repo.Upsert(r)
}

// ListByNode returns all Caddy routes for the given node.
func (s *CaddyRouteService) ListByNode(nodeID string) ([]*CaddyRoute, error) {
	return s.repo.ListByNode(nodeID)
}

// Delete removes a Caddy route record for the given node + domain.
func (s *CaddyRouteService) Delete(nodeID, domain string) error {
	return s.repo.Delete(nodeID, domain)
}
