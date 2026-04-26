package caddyroute

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("caddy route not found")
	ErrAlreadyExists = errors.New("caddy route already exists for this domain")
)

// CaddyRoute represents a Caddy reverse-proxy route managed by autohost.
type CaddyRoute struct {
	ID        string    `db:"id"         json:"id"`
	NodeID    string    `db:"node_id"    json:"node_id"`
	UserID    string    `db:"user_id"    json:"user_id"`
	Domain    string    `db:"domain"     json:"domain"`
	Upstream  string    `db:"upstream"   json:"upstream"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// Repository defines persistence operations for Caddy routes.
type Repository interface {
	Upsert(r *CaddyRoute) (*CaddyRoute, error)
	ListByNode(nodeID string) ([]*CaddyRoute, error)
	Delete(nodeID, domain string) error
}

// Service contains the business logic for managing Caddy routes.
type Service struct {
	repo Repository
}

// NewService creates a new CaddyRoute service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Upsert creates or updates a Caddy route record.
func (s *Service) Upsert(r *CaddyRoute) (*CaddyRoute, error) {
	return s.repo.Upsert(r)
}

// ListByNode returns all Caddy routes for the given node.
func (s *Service) ListByNode(nodeID string) ([]*CaddyRoute, error) {
	return s.repo.ListByNode(nodeID)
}

// Delete removes a Caddy route record for the given node + domain.
func (s *Service) Delete(nodeID, domain string) error {
	return s.repo.Delete(nodeID, domain)
}
