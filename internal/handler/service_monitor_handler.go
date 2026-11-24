// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"

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
	r.With(middleware.Auth).Post("/{id}/check", h.CheckNow)
	return r
}

// pushToAgent sends a monitor's configuration to its node. An offline node gets it when it reconnects.
func (h *ServiceMonitorHandler) pushToAgent(m *apps.ServiceMonitor) {
	if h.grpc == nil {
		return
	}
	if err := h.grpc.SendHealthCheckConfig(m.NodeID, m); err != nil && !errors.Is(err, grpcserver.ErrNodeNotConnected) {
		log.Printf("[ERROR] service monitor: push %s to node %s: %v", m.ID, m.NodeID, err)
	}
}

// dockerNameRe matches Docker container and Compose service names; anything else (e.g. a leading "-") could
// be read as a flag by "docker restart" or "docker compose up".
var dockerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// Limits for monitor settings; the web form uses the same ones.
const (
	minMonitorInterval      = 5
	maxMonitorInterval      = 3600
	defaultMonitorInterval  = 30
	maxFailureThreshold     = 20
	defaultFailureThreshold = 3
)

// monitorAutoHeal is the part of an auto-heal config the API checks.
type monitorAutoHeal struct {
	Enabled   bool   `json:"enabled"`
	Container string `json:"container"`
	Restart   struct {
		Enabled bool `json:"enabled"`
	} `json:"restart"`
	Recreate struct {
		Enabled         bool   `json:"enabled"`
		ComposeFilePath string `json:"compose_file_path"`
		ServiceName     string `json:"service_name"`
	} `json:"recreate"`
}

func parseMonitorAutoHeal(raw json.RawMessage) (monitorAutoHeal, error) {
	var cfg monitorAutoHeal
	if len(raw) == 0 || string(raw) == "null" {
		return cfg, nil
	}
	err := json.Unmarshal(raw, &cfg)
	return cfg, err
}

// authorizeAutoHeal validates an auto-heal config. Its "recreate" step makes the agent run
// "docker compose -f <compose_file_path> up" as root, which is equivalent to running arbitrary commands on
// the node, so enabling it requires terminal:exec on top of monitors:manage.
func (h *ServiceMonitorHandler) authorizeAutoHeal(ctx context.Context, userID, nodeID string, raw json.RawMessage) (int, string) {
	cfg, err := parseMonitorAutoHeal(raw)
	if err != nil {
		return http.StatusBadRequest, "invalid auto_heal_config"
	}
	rc := cfg.Recreate
	if p := rc.ComposeFilePath; p != "" && (!strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n")) {
		return http.StatusBadRequest, "auto_heal_config.recreate.compose_file_path must be an absolute path"
	}
	if rc.ServiceName != "" && !dockerNameRe.MatchString(rc.ServiceName) {
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

// validateAutoHealTarget checks the container auto-heal acts on: auto_heal_config.container, or the
// monitor's service name when it is empty. The agent runs "docker restart <container>" and reads the
// container's Compose labels to recreate it.
func validateAutoHealTarget(raw json.RawMessage, serviceName string) string {
	cfg, err := parseMonitorAutoHeal(raw)
	if err != nil {
		return "invalid auto_heal_config"
	}
	if cfg.Container != "" && !dockerNameRe.MatchString(cfg.Container) {
		return "auto_heal_config.container must be a container name"
	}
	if !cfg.Enabled || (!cfg.Restart.Enabled && !cfg.Recreate.Enabled) || cfg.Container != "" {
		return ""
	}
	if !dockerNameRe.MatchString(serviceName) {
		return "auto-heal needs a container: set auto_heal_config.container"
	}
	return ""
}

// validateMonitorRequest checks a create or update body and fills in defaults. It returns the message for
// the client, or "" when the request is valid.
func validateMonitorRequest(req *upsertMonitorRequest) string {
	req.ServiceName = strings.TrimSpace(req.ServiceName)
	if req.ServiceName == "" {
		return "service_name is required"
	}
	if len(req.ServiceName) > 128 {
		return "service_name must be at most 128 characters"
	}
	if req.HTTPExpectedStatus == 0 {
		req.HTTPExpectedStatus = 200
	}
	switch req.CheckType {
	case apps.CheckTypeProcess:
		if !dockerNameRe.MatchString(req.ServiceName) {
			return "service_name must be a container name for process checks"
		}
	case apps.CheckTypeHTTP:
		u, err := url.Parse(strings.TrimSpace(req.HTTPURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "http_url must be an http:// or https:// URL"
		}
		req.HTTPURL = u.String()
		if req.HTTPExpectedStatus < 100 || req.HTTPExpectedStatus > 599 {
			return "http_expected_status must be between 100 and 599"
		}
	case apps.CheckTypeTCP:
		req.TCPHost = strings.TrimSpace(req.TCPHost)
		if req.TCPHost == "" || len(req.TCPHost) > 253 || strings.ContainsAny(req.TCPHost, " /") {
			return "tcp_host must be a host name or IP address"
		}
		if req.TCPPort < 1 || req.TCPPort > 65535 {
			return "tcp_port must be between 1 and 65535"
		}
	default:
		return "check_type must be process, tcp or http"
	}
	if req.IntervalSeconds == 0 {
		req.IntervalSeconds = defaultMonitorInterval
	}
	if req.IntervalSeconds < minMonitorInterval || req.IntervalSeconds > maxMonitorInterval {
		return "interval_seconds must be between 5 and 3600"
	}
	if req.FailureThreshold == 0 {
		req.FailureThreshold = defaultFailureThreshold
	}
	if req.FailureThreshold < 1 || req.FailureThreshold > maxFailureThreshold {
		return "failure_threshold must be between 1 and 20"
	}
	return ""
}

// autoHealConfigChanged reports whether two stored auto-heal configs differ in meaning (the database may
// return the JSON with different spacing or key order).
func autoHealConfigChanged(oldRaw, newRaw json.RawMessage) bool {
	var oldCfg, newCfg identity.AutoHealConfig
	if len(oldRaw) > 0 && string(oldRaw) != "null" {
		if err := json.Unmarshal(oldRaw, &oldCfg); err != nil {
			return true
		}
	}
	if len(newRaw) > 0 && string(newRaw) != "null" {
		if err := json.Unmarshal(newRaw, &newCfg); err != nil {
			return true
		}
	}
	return oldCfg != newCfg
}

// respondNodeAccess answers a failed node access check. A missing node is 403, like a node the caller
// cannot see.
func respondNodeAccess(w http.ResponseWriter, status int) {
	if status == http.StatusNotFound {
		status = http.StatusForbidden
	}
	if status == http.StatusUnauthorized {
		response.RespondError(w, status, "unauthorized")
		return
	}
	response.RespondError(w, status, "forbidden")
}

// authorizeMonitorRead loads a monitor and checks the caller may read it. Monitors the caller cannot read
// answer 404, like unknown IDs.
func (h *ServiceMonitorHandler) authorizeMonitorRead(w http.ResponseWriter, r *http.Request) (*apps.ServiceMonitor, bool) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	monitor, ok := h.findMonitor(w, chi.URLParam(r, "id"))
	if !ok {
		return nil, false
	}
	if h.nodeSvc == nil {
		if monitor.UserID != claims.UserID {
			response.RespondError(w, http.StatusNotFound, "not found")
			return nil, false
		}
		return monitor, true
	}
	if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, monitor.NodeID, claims.UserID, identity.PermMonitorsRead); status != http.StatusOK {
		response.RespondError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return monitor, true
}

// authorizeMonitorManage loads a monitor and checks the caller may manage monitors on its node.
func (h *ServiceMonitorHandler) authorizeMonitorManage(w http.ResponseWriter, r *http.Request, userID string) (*apps.ServiceMonitor, bool) {
	monitor, ok := h.findMonitor(w, chi.URLParam(r, "id"))
	if !ok {
		return nil, false
	}
	if h.nodeSvc == nil {
		if monitor.UserID != userID {
			response.RespondError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
		return monitor, true
	}
	if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, monitor.NodeID, userID, identity.PermMonitorsManage); status != http.StatusOK {
		respondNodeAccess(w, status)
		return nil, false
	}
	return monitor, true
}

func (h *ServiceMonitorHandler) findMonitor(w http.ResponseWriter, id string) (*apps.ServiceMonitor, bool) {
	monitor, err := h.service.FindByID(id)
	if err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			response.RespondError(w, http.StatusNotFound, "not found")
			return nil, false
		}
		log.Printf("[ERROR] service monitor: find %s: %v", id, err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch monitor")
		return nil, false
	}
	return monitor, true
}

// ─── List ─────────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "nodeId query param required")
		return
	}
	if h.nodeSvc != nil {
		if _, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, nodeID, claims.UserID, identity.PermMonitorsRead); status != http.StatusOK {
			respondNodeAccess(w, status)
			return
		}
	}

	monitors, err := h.service.ListByNodeID(nodeID)
	if err != nil {
		log.Printf("[ERROR] service monitor: list node %s: %v", nodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch service monitors")
		return
	}
	if monitors == nil {
		monitors = []*apps.ServiceMonitor{}
	}
	response.RespondJSON(w, http.StatusOK, monitors)
}

// ─── Create ───────────────────────────────────────────────────────────────────

// upsertMonitorRequest is the create/update body. The auto-heal state is not accepted: only the agent
// writes it.
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
}

func (h *ServiceMonitorHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req upsertMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	ownerID := claims.UserID
	if h.nodeSvc != nil {
		n, status := checkNodeIDAccess(r.Context(), h.rbac, h.nodeSvc, req.NodeID, claims.UserID, identity.PermMonitorsManage)
		if status != http.StatusOK {
			respondNodeAccess(w, status)
			return
		}
		ownerID = effectiveNodeOwnerID(n, claims.UserID)
	}
	if msg := validateMonitorRequest(&req); msg != "" {
		response.RespondError(w, http.StatusBadRequest, msg)
		return
	}
	if status, msg := h.authorizeAutoHeal(r.Context(), claims.UserID, req.NodeID, req.AutoHealConfig); status != http.StatusOK {
		response.RespondError(w, status, msg)
		return
	}
	if msg := validateAutoHealTarget(req.AutoHealConfig, req.ServiceName); msg != "" {
		response.RespondError(w, http.StatusBadRequest, msg)
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
	}

	created, err := h.service.Create(monitor)
	if err != nil {
		log.Printf("[ERROR] service monitor: create on node %s: %v", req.NodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not create service monitor")
		return
	}

	h.pushToAgent(created)

	response.RespondJSON(w, http.StatusCreated, created)
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	existing, ok := h.authorizeMonitorManage(w, r, claims.UserID)
	if !ok {
		return
	}

	var req upsertMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateMonitorRequest(&req); msg != "" {
		response.RespondError(w, http.StatusBadRequest, msg)
		return
	}
	if status, msg := h.authorizeAutoHeal(r.Context(), claims.UserID, existing.NodeID, req.AutoHealConfig); status != http.StatusOK {
		response.RespondError(w, status, msg)
		return
	}
	autoHealConfig := existing.AutoHealConfig
	if len(req.AutoHealConfig) > 0 {
		autoHealConfig = req.AutoHealConfig
	}
	if msg := validateAutoHealTarget(autoHealConfig, req.ServiceName); msg != "" {
		response.RespondError(w, http.StatusBadRequest, msg)
		return
	}

	// A new auto-heal config starts from a clean state: otherwise a monitor left in CRITICAL_FAILED (or with
	// its attempts used up) would not try the corrected strategy until the service recovered on its own.
	if autoHealConfigChanged(existing.AutoHealConfig, autoHealConfig) {
		existing.AutoHealState = json.RawMessage("{}")
	}
	existing.AutoHealConfig = autoHealConfig
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

	updated, err := h.service.Update(existing)
	if err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			response.RespondError(w, http.StatusNotFound, "not found")
			return
		}
		log.Printf("[ERROR] service monitor: update %s: %v", existing.ID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not update service monitor")
		return
	}

	h.pushToAgent(updated)

	response.RespondJSON(w, http.StatusOK, updated)
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (h *ServiceMonitorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Fetch before deleting so we have the nodeID to notify the agent.
	monitor, ok := h.authorizeMonitorManage(w, r, claims.UserID)
	if !ok {
		return
	}

	if err := h.service.Delete(monitor.ID); err != nil {
		if errors.Is(err, apps.ErrServiceMonitorNotFound) {
			response.RespondError(w, http.StatusNotFound, "not found")
			return
		}
		log.Printf("[ERROR] service monitor: delete %s: %v", monitor.ID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not delete service monitor")
		return
	}

	// Tell the agent to stop this health check. An offline node does not get the monitor back on reconnect.
	if h.grpc != nil {
		if err := h.grpc.StopHealthCheck(monitor.NodeID, monitor.ID); err != nil && !errors.Is(err, grpcserver.ErrNodeNotConnected) {
			log.Printf("[ERROR] service monitor: stop %s on node %s: %v", monitor.ID, monitor.NodeID, err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── Check now ────────────────────────────────────────────────────────────────

// CheckNow makes the agent run the monitor's check right away instead of waiting for the next interval.
func (h *ServiceMonitorHandler) CheckNow(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	monitor, ok := h.authorizeMonitorManage(w, r, claims.UserID)
	if !ok {
		return
	}
	if !monitor.Enabled {
		response.RespondError(w, http.StatusConflict, "monitor is paused")
		return
	}
	if err := h.grpc.SendHealthCheckConfig(monitor.NodeID, monitor); err != nil {
		if errors.Is(err, grpcserver.ErrNodeNotConnected) {
			response.RespondError(w, http.StatusConflict, "node is offline")
			return
		}
		log.Printf("[ERROR] service monitor: check now %s on node %s: %v", monitor.ID, monitor.NodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not reach the node")
		return
	}
	response.RespondJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
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
		log.Printf("[ERROR] service monitor: history %s: %v", id, err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch event history")
		return
	}
	if events == nil {
		events = []*apps.ServiceHealthEvent{}
	}
	response.RespondJSON(w, http.StatusOK, events)
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
		log.Printf("[ERROR] service monitor: uptime %s: %v", id, err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch uptime history")
		return
	}
	if points == nil {
		points = []*apps.UptimePoint{}
	}
	response.RespondJSON(w, http.StatusOK, points)
}
