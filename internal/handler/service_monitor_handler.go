// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
)

// ServiceMonitorHandler handles CRUD for service monitors and exposes health event history.
type ServiceMonitorHandler struct {
	service *apps.ServiceMonitorService
	grpc    *grpcserver.NodeAgentServer
}

func NewServiceMonitorHandler(service *apps.ServiceMonitorService, grpc *grpcserver.NodeAgentServer) *ServiceMonitorHandler {
	return &ServiceMonitorHandler{service: service, grpc: grpc}
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

// ─── List ─────────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) List(w http.ResponseWriter, r *http.Request) {
	if middleware.GetClaims(r.Context()) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId query param required", http.StatusBadRequest)
		return
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

	monitor := &apps.ServiceMonitor{
		NodeID:             req.NodeID,
		UserID:             claims.UserID,
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
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req upsertMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
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
		http.Error(w, "forbidden", http.StatusForbidden)
		return
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
	h.grpc.StopHealthCheck(monitor.NodeID, id)

	w.WriteHeader(http.StatusNoContent)
}

// ─── Event History ────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) EventHistory(w http.ResponseWriter, r *http.Request) {
	if middleware.GetClaims(r.Context()) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")

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
	if middleware.GetClaims(r.Context()) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	points, err := h.service.UptimeHistory24h(id)
	if err != nil {
		http.Error(w, "could not fetch uptime history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}
