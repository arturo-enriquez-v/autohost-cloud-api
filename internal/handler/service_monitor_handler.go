// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

// ServiceMonitorHandler handles CRUD for service monitors and exposes health event history.
type ServiceMonitorHandler struct {
	service *apps.ServiceMonitorService
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.NodeService
	rbac    *identity.RBACService
}

func NewServiceMonitorHandler(service *apps.ServiceMonitorService, grpc *grpcserver.NodeAgentServer) *ServiceMonitorHandler {
	return &ServiceMonitorHandler{service: service, grpc: grpc}
}

func (h *ServiceMonitorHandler) SetRBACService(rbac *identity.RBACService, nodeSvc *node.NodeService) {
	h.rbac = rbac
	h.nodeSvc = nodeSvc
}

func (h *ServiceMonitorHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.With(middleware.Auth).Get("/", h.List)
	r.With(middleware.Auth).Post("/", h.Create)
	r.With(middleware.Auth).Put("/{id}", h.Update)
	r.With(middleware.Auth).Delete("/{id}", h.Delete)
	r.With(middleware.Auth).Get("/{id}/history", h.EventHistory)
	r.With(middleware.Auth).Get("/{id}/uptime", h.Uptime24h)
	return r
}

// composeServiceNameRe matches Docker Compose service names; anything else (e.g. a leading "-") could be
// read as a flag by "docker compose up".
var composeServiceNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// authorizeAutoHeal validates an auto-heal config. Its "recreate" step makes the agent run
// "docker compose -f <compose_file_path> up" as root, which is equivalent to running arbitrary commands on
// the node, so enabling it requires terminal:exec on top of monitors:manage.
func (h *ServiceMonitorHandler) authorizeAutoHeal(ctx context.Context, userID, nodeID string, raw json.RawMessage) (int, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return http.StatusOK, ""
	}
	var cfg struct {
		Recreate struct {
			Enabled         bool   `json:"enabled"`
			ComposeFilePath string `json:"compose_file_path"`
			ServiceName     string `json:"service_name"`
		} `json:"recreate"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return http.StatusBadRequest, "invalid auto_heal_config"
	}
	rc := cfg.Recreate
	if p := rc.ComposeFilePath; p != "" && (!strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n")) {
		return http.StatusBadRequest, "auto_heal_config.recreate.compose_file_path must be an absolute path"
	}
	if rc.ServiceName != "" && !composeServiceNameRe.MatchString(rc.ServiceName) {
		return http.StatusBadRequest, "auto_heal_config.recreate.service_name is not a valid compose service name"
	}
	if !rc.Enabled {
		return http.StatusOK, ""
	}
	if h.rbac == nil {
		return http.StatusForbidden, "forbidden"
	}
	if ok, err := h.rbac.HasNodePermission(ctx, userID, nodeID, identity.PermTerminalExec); err != nil || !ok {
		return http.StatusForbidden, "forbidden: auto-heal recreate requires terminal:exec on the node"
	}
	return http.StatusOK, ""
}

// authorizeMonitorRead loads a monitor and checks the caller may read it. Monitors the caller cannot read
// answer 404, like unknown IDs.
func (h *ServiceMonitorHandler) authorizeMonitorRead(w http.ResponseWriter, r *http.Request) (*apps.ServiceMonitor, bool) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	monitor, err := h.service.FindByID(chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return nil, false
		}
		http.Error(w, "could not fetch monitor", http.StatusInternalServerError)
		return nil, false
	}
	if h.nodeSvc == nil {
		if monitor.UserID != claims.UserID {
			http.Error(w, "not found", http.StatusNotFound)
			return nil, false
		}
		return monitor, true
	}
	if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, monitor.NodeID, claims.UserID, identity.PermMonitorsRead); status != http.StatusOK {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return monitor, true
}

// ─── List ─────────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId query param required", http.StatusBadRequest)
		return
	}
	if h.nodeSvc != nil {
		if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, nodeID, claims.UserID, identity.PermMonitorsRead); status != http.StatusOK {
			http.Error(w, "forbidden", status)
			return
		}
	}

	monitors, err := h.service.ListByNodeID(nodeID)
	if err != nil {
		http.Error(w, "could not fetch service monitors", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(monitors)
}

// ─── Create ───────────────────────────────────────────────────────────────────

type upsertMonitorRequest struct {
	NodeID             string          `json:"node_id"`
	ServiceName        string          `json:"service_name"`
	CheckType          string          `json:"check_type"`
	HTTPURL            string          `json:"http_url"`
	HTTPExpectedStatus int             `json:"http_expected_status"`
	TCPHost            string          `json:"tcp_host"`
	TCPPort            int             `json:"tcp_port"`
	IntervalSeconds    int             `json:"interval_seconds"`
	FailureThreshold   int             `json:"failure_threshold"`
	NotifyTelegram     bool            `json:"notify_telegram"`
	Enabled            bool            `json:"enabled"`
	AutoHealConfig     json.RawMessage `json:"auto_heal_config"`
	AutoHealState      json.RawMessage `json:"auto_heal_state"`
}

func (h *ServiceMonitorHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req upsertMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" || req.ServiceName == "" || req.CheckType == "" {
		http.Error(w, "node_id, service_name and check_type are required", http.StatusBadRequest)
		return
	}
	ownerID := claims.UserID
	if h.nodeSvc != nil {
		n, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, req.NodeID, claims.UserID, identity.PermMonitorsManage)
		if status != http.StatusOK {
			http.Error(w, "forbidden", status)
			return
		}
		ownerID = effectiveNodeOwnerID(n, claims.UserID)
	}
	if status, msg := h.authorizeAutoHeal(r.Context(), claims.UserID, req.NodeID, req.AutoHealConfig); status != http.StatusOK {
		http.Error(w, msg, status)
		return
	}

	monitor := &apps.ServiceMonitor{
		NodeID:             req.NodeID,
		UserID:             ownerID,
		ServiceName:        req.ServiceName,
		CheckType:          req.CheckType,
		HTTPURL:            req.HTTPURL,
		HTTPExpectedStatus: req.HTTPExpectedStatus,
		TCPHost:            req.TCPHost,
		TCPPort:            req.TCPPort,
		IntervalSeconds:    req.IntervalSeconds,
		FailureThreshold:   req.FailureThreshold,
		NotifyTelegram:     req.NotifyTelegram,
		Enabled:            req.Enabled,
		AutoHealConfig:     req.AutoHealConfig,
		AutoHealState:      req.AutoHealState,
	}

	created, err := h.service.Create(monitor)
	if err != nil {
		http.Error(w, "could not create service monitor", http.StatusInternalServerError)
		return
	}

	// Push config to connected agent
	if h.grpc != nil {
		h.grpc.SendHealthCheckConfig(req.NodeID, created)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.service.FindByID(id)
	if err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not fetch monitor", http.StatusInternalServerError)
		return
	}
	if existing.UserID != claims.UserID {
		if h.nodeSvc != nil {
			if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, existing.NodeID, claims.UserID, identity.PermMonitorsManage); status != http.StatusOK {
				http.Error(w, "forbidden", status)
				return
			}
		} else {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if h.rbac != nil && h.nodeSvc != nil {
		if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, existing.NodeID, claims.UserID, identity.PermMonitorsManage); status != http.StatusOK {
			http.Error(w, "forbidden", status)
			return
		}
	}

	var req upsertMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if status, msg := h.authorizeAutoHeal(r.Context(), claims.UserID, existing.NodeID, req.AutoHealConfig); status != http.StatusOK {
		http.Error(w, msg, status)
		return
	}

	existing.ServiceName = req.ServiceName
	existing.CheckType = req.CheckType
	existing.HTTPURL = req.HTTPURL
	existing.HTTPExpectedStatus = req.HTTPExpectedStatus
	existing.TCPHost = req.TCPHost
	existing.TCPPort = req.TCPPort
	existing.IntervalSeconds = req.IntervalSeconds
	existing.FailureThreshold = req.FailureThreshold
	existing.NotifyTelegram = req.NotifyTelegram
	existing.Enabled = req.Enabled
	if len(req.AutoHealConfig) > 0 {
		existing.AutoHealConfig = req.AutoHealConfig
	}
	if len(req.AutoHealState) > 0 {
		existing.AutoHealState = req.AutoHealState
	}

	updated, err := h.service.Update(existing)
	if err != nil {
		http.Error(w, "could not update service monitor", http.StatusInternalServerError)
		return
	}

	// Push updated config to connected agent
	if h.grpc != nil {
		h.grpc.SendHealthCheckConfig(existing.NodeID, updated)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")

	// Fetch before deleting so we have the nodeID to notify the agent.
	monitor, err := h.service.FindByID(id)
	if err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not fetch monitor", http.StatusInternalServerError)
		return
	}
	if monitor.UserID != claims.UserID {
		if h.nodeSvc != nil {
			if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, monitor.NodeID, claims.UserID, identity.PermMonitorsManage); status != http.StatusOK {
				http.Error(w, "forbidden", status)
				return
			}
		} else {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if h.rbac != nil && h.nodeSvc != nil {
		if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, monitor.NodeID, claims.UserID, identity.PermMonitorsManage); status != http.StatusOK {
			http.Error(w, "forbidden", status)
			return
		}
	}

	if err := h.service.Delete(id); err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not delete service monitor", http.StatusInternalServerError)
		return
	}

	// Tell the agent to stop this health check
	if h.grpc != nil {
		h.grpc.StopHealthCheck(monitor.NodeID, id)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── Event History ────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) EventHistory(w http.ResponseWriter, r *http.Request) {
	monitor, ok := h.authorizeMonitorRead(w, r)
	if !ok {
		return
	}
	id := monitor.ID

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	events, err := h.service.EventHistory(id, limit)
	if err != nil {
		http.Error(w, "could not fetch event history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// ─── Uptime 24h ───────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) Uptime24h(w http.ResponseWriter, r *http.Request) {
	monitor, ok := h.authorizeMonitorRead(w, r)
	if !ok {
		return
	}
	id := monitor.ID
	points, err := h.service.UptimeHistory24h(id)
	if err != nil {
		http.Error(w, "could not fetch uptime history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}
