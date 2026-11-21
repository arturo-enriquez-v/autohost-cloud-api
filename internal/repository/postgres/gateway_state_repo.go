// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
)

// GatewayStateRepository implements network.GatewayStateStore on Postgres.
type GatewayStateRepository struct {
	db *sqlx.DB
}

func NewGatewayStateRepository(db *sqlx.DB) *GatewayStateRepository {
	return &GatewayStateRepository{db: db}
}

const (
	settingFunnelDNSName  = "funnel.dns_name"
	settingCFTunnelID     = "cloudflare.tunnel_id"
	settingCFTunnelName   = "cloudflare.tunnel_name"
	settingCFBaseDomain   = "cloudflare.base_domain"
	settingCFMode         = "cloudflare.mode"
	settingCFQuickTunnURL = "cloudflare.quick_tunnel_url"
)

func getSetting(ctx context.Context, q sqlx.QueryerContext, key string) (string, error) {
	var v string
	err := sqlx.GetContext(ctx, q, &v, `SELECT value FROM platform_settings WHERE key = $1`, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

func setSetting(ctx context.Context, tx *sqlx.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO platform_settings (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// ─── Shares + Funnel ─────────────────────────────────────────────────────────

type fileShareRow struct {
	ID            string    `db:"id"`
	OwnerID       string    `db:"owner_id"`
	NodeID        string    `db:"node_id"`
	NodeName      string    `db:"node_name"`
	FolderPath    string    `db:"folder_path"`
	Title         string    `db:"title"`
	Permission    string    `db:"permission"`
	PasswordHash  string    `db:"password_hash"`
	FunnelEnabled bool      `db:"funnel_enabled"`
	FunnelURL     string    `db:"funnel_url"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

type funnelRouteRow struct {
	ID             string    `db:"id"`
	FunnelPort     int       `db:"funnel_port"`
	PathPrefix     string    `db:"path_prefix"`
	PublicURL      string    `db:"public_url"`
	TargetNodeID   string    `db:"target_node_id"`
	TargetNodeName string    `db:"target_node_name"`
	TargetIP       string    `db:"target_ip"`
	ContainerPort  int       `db:"container_port"`
	Upstream       string    `db:"upstream"`
	ContainerID    string    `db:"container_id"`
	ContainerName  string    `db:"container_name"`
	ContainerImage string    `db:"container_image"`
	CreatedAt      time.Time `db:"created_at"`
}

func (r *GatewayStateRepository) LoadShareFunnelState(ctx context.Context) (*network.ShareFunnelState, error) {
	st := &network.ShareFunnelState{Shares: []*network.SharedFolderLink{}, FunnelRoutes: []*network.TailscaleFunnelRoute{}}

	dns, err := getSetting(ctx, r.db, settingFunnelDNSName)
	if err != nil {
		return nil, err
	}
	st.FunnelDNSName = dns

	var shares []fileShareRow
	if err := r.db.SelectContext(ctx, &shares, `
		SELECT id, owner_id, node_id, node_name, folder_path, title, permission, password_hash,
		       funnel_enabled, funnel_url, created_at, updated_at
		FROM file_shares ORDER BY created_at`); err != nil {
		return nil, fmt.Errorf("load file shares: %w", err)
	}
	for _, s := range shares {
		st.Shares = append(st.Shares, &network.SharedFolderLink{
			ID: s.ID, OwnerID: s.OwnerID, NodeID: s.NodeID, NodeName: s.NodeName, FolderPath: s.FolderPath,
			Title: s.Title, Permission: network.SharePermission(s.Permission), PasswordHash: s.PasswordHash,
			FunnelEnabled: s.FunnelEnabled, FunnelURL: s.FunnelURL, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		})
	}

	var routes []funnelRouteRow
	if err := r.db.SelectContext(ctx, &routes, `
		SELECT id, funnel_port, path_prefix, public_url, target_node_id, target_node_name, target_ip,
		       container_port, upstream, container_id, container_name, container_image, created_at
		FROM funnel_routes ORDER BY created_at`); err != nil {
		return nil, fmt.Errorf("load funnel routes: %w", err)
	}
	for _, rt := range routes {
		st.FunnelRoutes = append(st.FunnelRoutes, &network.TailscaleFunnelRoute{
			ID: rt.ID, FunnelPort: rt.FunnelPort, PathPrefix: rt.PathPrefix, PublicURL: rt.PublicURL,
			TargetNodeID: rt.TargetNodeID, TargetNodeName: rt.TargetNodeName, TargetIP: rt.TargetIP,
			ContainerPort: rt.ContainerPort, Upstream: rt.Upstream, ContainerID: rt.ContainerID,
			ContainerName: rt.ContainerName, ContainerImage: rt.ContainerImage, CreatedAt: rt.CreatedAt,
		})
	}
	return st, nil
}

func (r *GatewayStateRepository) SaveShareFunnelState(ctx context.Context, st *network.ShareFunnelState) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := setSetting(ctx, tx, settingFunnelDNSName, st.FunnelDNSName); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM file_shares`); err != nil {
		return err
	}
	for _, s := range st.Shares {
		if !isUUID(s.OwnerID) || !isUUID(s.NodeID) {
			continue // malformed legacy record: a ::uuid cast error would abort the whole transaction
		}
		// INSERT ... SELECT WHERE EXISTS skips shares whose node/owner was deleted meanwhile.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO file_shares (id, owner_id, node_id, node_name, folder_path, title, permission,
			                         password_hash, funnel_enabled, funnel_url, created_at, updated_at)
			SELECT $1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12
			WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $3::uuid)
			  AND EXISTS (SELECT 1 FROM users WHERE id = $2::uuid)`,
			s.ID, s.OwnerID, s.NodeID, s.NodeName, s.FolderPath, s.Title, string(s.Permission),
			s.PasswordHash, s.FunnelEnabled, s.FunnelURL, nonZeroTime(s.CreatedAt), nonZeroTime(s.UpdatedAt),
		); err != nil {
			return fmt.Errorf("save share %s: %w", s.ID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM funnel_routes`); err != nil {
		return err
	}
	for _, rt := range st.FunnelRoutes {
		if !isUUID(rt.TargetNodeID) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO funnel_routes (id, funnel_port, path_prefix, public_url, target_node_id, target_node_name,
			                           target_ip, container_port, upstream, container_id, container_name,
			                           container_image, created_at)
			SELECT $1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13
			WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $5::uuid)`,
			rt.ID, rt.FunnelPort, rt.PathPrefix, rt.PublicURL, rt.TargetNodeID, rt.TargetNodeName, rt.TargetIP,
			rt.ContainerPort, rt.Upstream, rt.ContainerID, rt.ContainerName, rt.ContainerImage,
			nonZeroTime(rt.CreatedAt),
		); err != nil {
			return fmt.Errorf("save funnel route %s: %w", rt.ID, err)
		}
	}
	return tx.Commit()
}

// ─── Cloudflare Tunnel ───────────────────────────────────────────────────────

type cfRouteRow struct {
	Domain         string    `db:"domain"`
	Subdomain      string    `db:"subdomain"`
	BaseDomain     string    `db:"base_domain"`
	TargetNodeID   string    `db:"target_node_id"`
	TargetNodeName string    `db:"target_node_name"`
	TargetIP       string    `db:"target_ip"`
	Port           int       `db:"port"`
	Upstream       string    `db:"upstream"`
	ContainerID    string    `db:"container_id"`
	ContainerName  string    `db:"container_name"`
	ContainerImage string    `db:"container_image"`
	DNSConfigured  bool      `db:"dns_configured"`
	DNSMessage     string    `db:"dns_message"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

func (r *GatewayStateRepository) LoadCloudflareTunnelState(ctx context.Context) (*network.CloudflareTunnelState, error) {
	st := &network.CloudflareTunnelState{Routes: []*network.CloudflareTunnelRoute{}}
	for key, dst := range map[string]*string{
		settingCFTunnelID: &st.TunnelID, settingCFTunnelName: &st.TunnelName, settingCFBaseDomain: &st.BaseDomain,
		settingCFMode: &st.Mode, settingCFQuickTunnURL: &st.QuickTunnelURL,
	} {
		v, err := getSetting(ctx, r.db, key)
		if err != nil {
			return nil, err
		}
		*dst = v
	}

	var rows []cfRouteRow
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT domain, subdomain, base_domain, target_node_id, target_node_name, target_ip, port, upstream,
		       container_id, container_name, container_image, dns_configured, dns_message, created_at, updated_at
		FROM cloudflare_tunnel_routes ORDER BY created_at`); err != nil {
		return nil, fmt.Errorf("load cloudflare routes: %w", err)
	}
	for _, rt := range rows {
		st.Routes = append(st.Routes, &network.CloudflareTunnelRoute{
			Domain: rt.Domain, Subdomain: rt.Subdomain, BaseDomain: rt.BaseDomain, TargetNodeID: rt.TargetNodeID,
			TargetNodeName: rt.TargetNodeName, TargetIP: rt.TargetIP, Port: rt.Port, Upstream: rt.Upstream,
			ContainerID: rt.ContainerID, ContainerName: rt.ContainerName, ContainerImage: rt.ContainerImage,
			DNSConfigured: rt.DNSConfigured, DNSMessage: rt.DNSMessage, CreatedAt: rt.CreatedAt, UpdatedAt: rt.UpdatedAt,
		})
	}
	return st, nil
}

func (r *GatewayStateRepository) SaveCloudflareTunnelState(ctx context.Context, st *network.CloudflareTunnelState) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for key, val := range map[string]string{
		settingCFTunnelID: st.TunnelID, settingCFTunnelName: st.TunnelName, settingCFBaseDomain: st.BaseDomain,
		settingCFMode: st.Mode, settingCFQuickTunnURL: st.QuickTunnelURL,
	} {
		if err := setSetting(ctx, tx, key, val); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM cloudflare_tunnel_routes`); err != nil {
		return err
	}
	for _, rt := range st.Routes {
		if !isUUID(rt.TargetNodeID) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cloudflare_tunnel_routes (domain, subdomain, base_domain, target_node_id, target_node_name,
			                                      target_ip, port, upstream, container_id, container_name,
			                                      container_image, dns_configured, dns_message, created_at, updated_at)
			SELECT $1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
			WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $4::uuid)`,
			rt.Domain, rt.Subdomain, rt.BaseDomain, rt.TargetNodeID, rt.TargetNodeName, rt.TargetIP, rt.Port,
			rt.Upstream, rt.ContainerID, rt.ContainerName, rt.ContainerImage, rt.DNSConfigured, rt.DNSMessage,
			nonZeroTime(rt.CreatedAt), nonZeroTime(rt.UpdatedAt),
		); err != nil {
			return fmt.Errorf("save cloudflare route %s: %w", rt.Domain, err)
		}
	}
	return tx.Commit()
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
