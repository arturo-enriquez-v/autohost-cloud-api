// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	logdiagnostic "github.com/arturo/autohost-cloud-api/internal/domain/log_diagnostic"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

// LogDiagnosticHandler exposes REST endpoints for log diagnostic operations.
type LogDiagnosticHandler struct {
	service *logdiagnostic.Service
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.Service
}

// NewLogDiagnosticHandler creates a new handler with all required dependencies.
func NewLogDiagnosticHandler(
	service *logdiagnostic.Service,
	grpc *grpcserver.NodeAgentServer,
	nodeSvc *node.Service,
) *LogDiagnosticHandler {
	return &LogDiagnosticHandler{
		service: service,
		grpc:    grpc,
		nodeSvc: nodeSvc,
	}
}

// Routes registers all log diagnostic REST endpoints.
func (h *LogDiagnosticHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Post("/collect", h.Collect)
	r.Get("/", h.List)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/approve", h.Approve)
	r.Post("/{id}/reject", h.Reject)
	r.Delete("/{id}", h.Delete)
	return r
}

// Collect triggers log collection on a node agent.
func (h *LogDiagnosticHandler) Collect(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req logdiagnostic.CollectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	// Verify the user owns the node.
	if h.nodeSvc != nil {
		n, err := h.nodeSvc.GetByID(req.NodeID)
		if err != nil || n == nil {
			response.RespondError(w, http.StatusNotFound, "node not found")
			return
		}
		if n.OwnerID == nil || *n.OwnerID != claims.UserID {
			response.RespondError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	// Verify the node is connected.
	if !h.grpc.IsNodeConnected(req.NodeID) {
		response.RespondError(w, http.StatusServiceUnavailable, "node is not connected — is the agent running?")
		return
	}

	// Create the pending diagnostic record.
	diagnostic, err := h.service.RequestCollection(r.Context(), claims.UserID, req)
	if err != nil {
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Dispatch the collection command to the agent.
	linesPerSource := req.LinesPerSource
	if linesPerSource <= 0 {
		linesPerSource = 200
	}
	if err := h.grpc.DispatchLogCollection(
		req.NodeID,
		diagnostic.RequestID,
		req.Sources,
		linesPerSource,
		req.IncludeContainers,
	); err != nil {
		response.RespondError(w, http.StatusInternalServerError, "failed to send collection command to agent: "+err.Error())
		return
	}

	response.RespondJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":    true,
		"diagnostic": diagnostic,
		"message":    "Log collection initiated. The diagnostic will appear as 'preview' once the agent responds.",
	})
}

// List returns all diagnostics for the authenticated user.
func (h *LogDiagnosticHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	limit := 20
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	var filter logdiagnostic.ListFilter
	if v := r.URL.Query().Get("node_id"); v != "" {
		filter.NodeID = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		filter.Status = &v
	}
	if v := r.URL.Query().Get("system_health"); v != "" {
		filter.SystemHealth = &v
	}

	diagnostics, total, err := h.service.List(r.Context(), claims.UserID, filter, limit, offset)
	if err != nil {
		response.RespondError(w, http.StatusInternalServerError, "could not fetch diagnostics")
		return
	}

	response.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"diagnostics": diagnostics,
		"total":       total,
	})
}

// GetByID returns a single diagnostic with full detail.
func (h *LogDiagnosticHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := chi.URLParam(r, "id")
	diagnostic, err := h.service.GetByID(r.Context(), claims.UserID, id)
	if err != nil {
		response.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, map[string]interface{}{
		"diagnostic": diagnostic,
	})
}

// Approve transitions a diagnostic from preview to approved.
func (h *LogDiagnosticHandler) Approve(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.service.Approve(r.Context(), claims.UserID, id); err != nil {
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Reject transitions a diagnostic from preview to rejected.
func (h *LogDiagnosticHandler) Reject(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.service.Reject(r.Context(), claims.UserID, id); err != nil {
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Delete removes a diagnostic record.
func (h *LogDiagnosticHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		response.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.service.Delete(r.Context(), claims.UserID, id); err != nil {
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, map[string]bool{"success": true})
}

