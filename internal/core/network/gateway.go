// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"context"
	"time"
)

// SharePermission defines the access level for a shared folder link:
//   - "read":   View folder contents, stream media, and download files.
//   - "write":  "read" + upload new files and create subfolders.
//   - "editor": "write" + delete files and folders inside the shared scope.
type SharePermission string

const (
	SharePermRead   SharePermission = "read"
	SharePermWrite  SharePermission = "write"
	SharePermEditor SharePermission = "editor"
)

// SharedFolderLink represents a Google-Drive-style password-protected folder share.
// The password is only stored as a bcrypt hash and is never serialized.
type SharedFolderLink struct {
	ID            string          `json:"id"`
	OwnerID       string          `json:"owner_id"`
	NodeID        string          `json:"node_id"`
	NodeName      string          `json:"node_name"`
	FolderPath    string          `json:"folder_path"` // "/" for all folders, or "/specific/folder"
	Title         string          `json:"title"`
	Permission    SharePermission `json:"permission"` // "read" | "write" | "editor"
	PasswordHash  string          `json:"-"`
	FunnelEnabled bool            `json:"funnel_enabled"`
	FunnelURL     string          `json:"funnel_url,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// TailscaleFunnelRoute represents a Docker container exposed via Tailscale Funnel.
type TailscaleFunnelRoute struct {
	ID             string    `json:"id"`
	FunnelPort     int       `json:"funnel_port"` // 443, 8443, or 10000
	PathPrefix     string    `json:"path_prefix"` // "/" or "/app"
	PublicURL      string    `json:"public_url"`
	TargetNodeID   string    `json:"target_node_id"`
	TargetNodeName string    `json:"target_node_name"`
	TargetIP       string    `json:"target_ip"`
	ContainerPort  int       `json:"container_port"`
	Upstream       string    `json:"upstream"`
	ContainerID    string    `json:"container_id,omitempty"`
	ContainerName  string    `json:"container_name,omitempty"`
	ContainerImage string    `json:"container_image,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ShareFunnelState is the full persisted state of folder shares and Tailscale Funnel routes.
type ShareFunnelState struct {
	FunnelDNSName string                  `json:"funnel_dns_name"`
	Shares        []*SharedFolderLink     `json:"shares"`
	FunnelRoutes  []*TailscaleFunnelRoute `json:"funnel_routes"`
}

// CloudflareTunnelRoute maps a public hostname to a container on a node through the Cloudflare Tunnel.
type CloudflareTunnelRoute struct {
	Domain         string    `json:"domain"`
	Subdomain      string    `json:"subdomain"`
	BaseDomain     string    `json:"base_domain"`
	TargetNodeID   string    `json:"target_node_id"`
	TargetNodeName string    `json:"target_node_name"`
	TargetIP       string    `json:"target_ip"`
	Port           int       `json:"port"`
	Upstream       string    `json:"upstream"`
	ContainerID    string    `json:"container_id,omitempty"`
	ContainerName  string    `json:"container_name,omitempty"`
	ContainerImage string    `json:"container_image,omitempty"`
	DNSConfigured  bool      `json:"dns_configured"`
	DNSMessage     string    `json:"dns_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CloudflareTunnelState is the persisted Cloudflare Tunnel configuration (credentials stay on disk, where
// cloudflared reads them).
type CloudflareTunnelState struct {
	TunnelID       string                   `json:"tunnel_id"`
	TunnelName     string                   `json:"tunnel_name"`
	BaseDomain     string                   `json:"base_domain"`
	Mode           string                   `json:"mode"` // "managed" | "quick" | "token"
	QuickTunnelURL string                   `json:"quick_tunnel_url,omitempty"`
	Routes         []*CloudflareTunnelRoute `json:"routes"`
}

// GatewayStateStore persists the public gateway configuration (shares, Funnel, Cloudflare Tunnel).
// Save* replace the whole state atomically; rows referencing nodes or users that no longer exist are
// dropped instead of failing the save.
type GatewayStateStore interface {
	LoadShareFunnelState(ctx context.Context) (*ShareFunnelState, error)
	SaveShareFunnelState(ctx context.Context, st *ShareFunnelState) error
	LoadCloudflareTunnelState(ctx context.Context) (*CloudflareTunnelState, error)
	SaveCloudflareTunnelState(ctx context.Context, st *CloudflareTunnelState) error
}
