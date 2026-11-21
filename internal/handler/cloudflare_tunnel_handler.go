// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/network"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// Domain types live in core/network; aliases keep this handler's code readable.
type (
	CloudflareTunnelRoute = network.CloudflareTunnelRoute
	CloudflareTunnelState = network.CloudflareTunnelState
)

// CloudflareTunnelStatusResponse is returned by GET /v1/cloudflare-tunnel/status.
type CloudflareTunnelStatusResponse struct {
	Installed      bool                     `json:"installed"`
	Authenticated  bool                     `json:"authenticated"`
	Running        bool                     `json:"running"`
	LoginInProcess bool                     `json:"login_in_process"`
	LoginURL       string                   `json:"login_url,omitempty"`
	LoginError     string                   `json:"login_error,omitempty"`
	Mode           string                   `json:"mode"`
	TunnelID       string                   `json:"tunnel_id"`
	TunnelName     string                   `json:"tunnel_name"`
	BaseDomain     string                   `json:"base_domain"`
	QuickTunnelURL string                   `json:"quick_tunnel_url,omitempty"`
	MasterNodeID   string                   `json:"master_node_id,omitempty"`
	MasterHostname string                   `json:"master_hostname,omitempty"`
	Routes         []*CloudflareTunnelRoute `json:"routes"`
	RecentLogs     []string                 `json:"recent_logs,omitempty"`
}

// CloudflareTunnelHandler exposes the Cloudflare Tunnel API. It authenticates and authorizes callers;
// network.CloudflareTunnel owns state, cloudflared processes, DNS and the internal proxy.
type CloudflareTunnelHandler struct {
	nodeSvc *node.NodeService
	rbacSvc *identity.RBACService
	tunnel  *network.CloudflareTunnel
}

func (h *CloudflareTunnelHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func NewCloudflareTunnelHandler(
	nodeSvc *node.NodeService,
	caddySvc *apps.CaddyRouteService,
	store network.GatewayStateStore,
) *CloudflareTunnelHandler {
	baseDir := filepath.Join(platform.StorageDir(), "cloudflared")
	var mirror network.CloudflareRouteMirror
	if caddySvc != nil && nodeSvc != nil {
		mirror = &masterCaddyMirror{nodeSvc: nodeSvc, caddySvc: caddySvc}
	}
	tunnel := network.NewCloudflareTunnel(network.CloudflareTunnelConfig{
		Store:           store,
		BaseDir:         baseDir,
		LegacyStatePath: filepath.Join(baseDir, "state.json"),
		Mirror:          mirror,
	})
	tunnel.Start()
	return &CloudflareTunnelHandler{nodeSvc: nodeSvc, tunnel: tunnel}
}

// masterCaddyMirror keeps the master node's Caddy routes in sync with tunnel routes.
type masterCaddyMirror struct {
	nodeSvc  *node.NodeService
	caddySvc *apps.CaddyRouteService
}

func (m *masterCaddyMirror) Upsert(domain, upstream, userID string) {
	if master, err := m.nodeSvc.FindLocalNode(); err == nil && master != nil {
		_, _ = m.caddySvc.Upsert(&apps.CaddyRoute{
			NodeID: master.ID, UserID: userID, Domain: domain, Upstream: upstream, TLSMode: "http_only",
		})
	}
}

func (m *masterCaddyMirror) Delete(domain string) {
	if master, err := m.nodeSvc.FindLocalNode(); err == nil && master != nil {
		_ = m.caddySvc.Delete(master.ID, domain)
	}
}

func (h *CloudflareTunnelHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/status", h.GetStatus)
	r.Post("/login", h.StartLogin)
	r.Post("/quick-tunnel", h.StartQuickTunnel)
	r.Post("/config", h.UpdateConfig)
	r.Post("/disconnect", h.Disconnect)
	r.Post("/routes", h.UpsertRoute)
	r.Delete("/routes/{domain}", h.DeleteRoute)
	return r
}

// requireCFAdmin enforces that platform-wide tunnel settings are only changed by global admins.
func (h *CloudflareTunnelHandler) requireCFAdmin(ctx context.Context, w http.ResponseWriter, userID string) bool {
	if !checkAdminAccess(ctx, h.rbacSvc, userID) {
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
		return false
	}
	return true
}

// canManageCFRoute reports whether userID may modify a tunnel route (admin, or network:manage on its node).
func (h *CloudflareTunnelHandler) canManageCFRoute(ctx context.Context, userID string, rt *CloudflareTunnelRoute) bool {
	if checkAdminAccess(ctx, h.rbacSvc, userID) {
		return true
	}
	if h.rbacSvc == nil || rt == nil || rt.TargetNodeID == "" {
		return false
	}
	ok, err := h.rbacSvc.HasNodePermission(ctx, userID, rt.TargetNodeID, identity.PermNetworkManage)
	return err == nil && ok
}

func writeTunnelError(w http.ResponseWriter, err error, persistMsg string) {
	switch {
	case errors.Is(err, network.ErrCFRouteNotFound):
		http.Error(w, "route not found", http.StatusNotFound)
	case errors.Is(err, network.ErrCFDomainTaken):
		http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
	case errors.Is(err, network.ErrCFInvalidDomain):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, network.ErrCFPersist):
		http.Error(w, persistMsg, http.StatusInternalServerError)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// GetStatus returns the current state of the master Cloudflare Tunnel and its routes.
// GET /v1/cloudflare-tunnel/status
func (h *CloudflareTunnelHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	st := h.tunnel.Status()
	resp := CloudflareTunnelStatusResponse{
		Installed: st.Installed, Authenticated: st.Authenticated, Running: st.Running,
		LoginInProcess: st.LoginInProcess, LoginURL: st.LoginURL, LoginError: st.LoginError,
		Mode: st.State.Mode, TunnelID: st.State.TunnelID, TunnelName: st.State.TunnelName,
		BaseDomain: st.State.BaseDomain, QuickTunnelURL: st.State.QuickTunnelURL,
		Routes: st.State.Routes, RecentLogs: st.RecentLogs,
	}
	if h.nodeSvc != nil {
		if localNode, err := h.nodeSvc.FindLocalNode(); err == nil && localNode != nil {
			resp.MasterNodeID, resp.MasterHostname = localNode.ID, localNode.Hostname
		}
	}
	if resp.Routes == nil {
		resp.Routes = []*CloudflareTunnelRoute{}
	}

	// Non-admins only see routes on nodes they can read, and never the login URL or raw tunnel logs.
	if !checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		visible := make([]*CloudflareTunnelRoute, 0, len(resp.Routes))
		for _, rt := range resp.Routes {
			if h.rbacSvc == nil {
				break
			}
			if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rt.TargetNodeID, identity.PermNetworkRead); err == nil && ok {
				visible = append(visible, rt)
			}
		}
		resp.Routes = visible
		resp.LoginURL = ""
		resp.LoginError = ""
		resp.RecentLogs = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// StartLogin initiates `cloudflared tunnel login` and returns the browser authorization URL.
// POST /v1/cloudflare-tunnel/login
func (h *CloudflareTunnelHandler) StartLogin(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.requireCFAdmin(r.Context(), w, claims.UserID) {
		return
	}
	loginURL, err := h.tunnel.StartLogin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "waiting_auth", "login_url": loginURL})
}

// StartQuickTunnel starts a zero-account *.trycloudflare.com tunnel.
// POST /v1/cloudflare-tunnel/quick-tunnel
func (h *CloudflareTunnelHandler) StartQuickTunnel(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.requireCFAdmin(r.Context(), w, claims.UserID) {
		return
	}
	quickURL, err := h.tunnel.StartQuickTunnel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "mode": "quick", "quick_tunnel_url": quickURL})
}

// UpdateConfig updates the base domain (e.g. "tudominio.com").
// POST /v1/cloudflare-tunnel/config
func (h *CloudflareTunnelHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.requireCFAdmin(r.Context(), w, claims.UserID) {
		return
	}
	var req struct {
		BaseDomain string `json:"base_domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	clean, err := h.tunnel.SetBaseDomain(req.BaseDomain)
	if err != nil {
		writeTunnelError(w, err, "could not save tunnel configuration")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"base_domain": clean})
}

// Disconnect stops the running tunnel and clears the Cloudflare account credentials.
// POST /v1/cloudflare-tunnel/disconnect
func (h *CloudflareTunnelHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.requireCFAdmin(r.Context(), w, claims.UserID) {
		return
	}
	_ = h.tunnel.Disconnect()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "disconnected"})
}

type upsertCloudflareRouteRequest struct {
	Subdomain      string `json:"subdomain"`
	BaseDomain     string `json:"base_domain"`
	Domain         string `json:"domain"`
	TargetNodeID   string `json:"target_node_id"`
	Port           int    `json:"port"`
	ContainerID    string `json:"container_id"`
	ContainerName  string `json:"container_name"`
	ContainerImage string `json:"container_image"`
}

// UpsertRoute maps a hostname to a container on a node (via its Tailscale IP, or loopback for the master),
// registers the DNS record and reloads cloudflared.
// POST /v1/cloudflare-tunnel/routes
func (h *CloudflareTunnelHandler) UpsertRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req upsertCloudflareRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetNodeID == "" || req.Port <= 0 || req.Port > 65535 {
		http.Error(w, "target_node_id and valid port (1-65535) are required", http.StatusBadRequest)
		return
	}

	targetNode, err := h.nodeSvc.GetByID(req.TargetNodeID)
	if err != nil || targetNode == nil {
		http.Error(w, "target node not found", http.StatusNotFound)
		return
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, targetNode, claims.UserID, identity.PermNetworkManage) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Prefer the Tailscale VPN IP, fall back to loopback for the master node or its local IP.
	targetIP := ""
	if targetNode.IPVPN != nil && strings.TrimSpace(*targetNode.IPVPN) != "" {
		targetIP = hostIPOnly(*targetNode.IPVPN)
	} else if targetNode.IsLocal {
		targetIP = "127.0.0.1"
	} else if strings.TrimSpace(targetNode.IPLocal) != "" {
		targetIP = hostIPOnly(targetNode.IPLocal)
	}
	if targetIP == "" {
		http.Error(w, "target node has no reachable Tailscale or local IP", http.StatusBadRequest)
		return
	}

	fullDomain, sub, base, err := h.tunnel.ResolveRouteDomain(req.Subdomain, req.BaseDomain, req.Domain, targetNode.Hostname, req.Port)
	if err != nil {
		writeTunnelError(w, err, "")
		return
	}
	// Only admins may change the platform-wide base domain through a route request.
	if current := h.tunnel.BaseDomain(); base != "" && current != "" && base != current && !checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		http.Error(w, "forbidden: only admins can change the tunnel base domain", http.StatusForbidden)
		return
	}
	// Replacing a route that already serves this domain requires managing that route's node too.
	allowReplace := false
	if existing, ok := h.tunnel.Route(fullDomain); ok {
		if !h.canManageCFRoute(r.Context(), claims.UserID, existing) {
			http.Error(w, "forbidden: domain is already routed to a node you cannot manage", http.StatusForbidden)
			return
		}
		allowReplace = true
	}

	route, err := h.tunnel.UpsertRoute(network.CloudflareRouteInput{
		Domain: fullDomain, Subdomain: sub, BaseDomain: base, TargetNodeID: targetNode.ID,
		TargetNodeName: targetNode.Hostname, TargetIP: targetIP, Port: req.Port,
		ContainerID: req.ContainerID, ContainerName: req.ContainerName, ContainerImage: req.ContainerImage,
		UserID: claims.UserID,
	}, allowReplace)
	if err != nil {
		writeTunnelError(w, err, "could not save tunnel route")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(route)
}

// DeleteRoute removes a domain route from the tunnel configuration.
// DELETE /v1/cloudflare-tunnel/routes/{domain}
func (h *CloudflareTunnelHandler) DeleteRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	domain := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "domain")))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	rt, ok := h.tunnel.Route(domain)
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if !h.canManageCFRoute(r.Context(), claims.UserID, rt) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.tunnel.DeleteRoute(rt.Domain); err != nil {
		writeTunnelError(w, err, "could not delete tunnel route")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
