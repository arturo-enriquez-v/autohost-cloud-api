// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type HeartbeatsHandler struct {
	nodeService *node.NodeService
}

func NewHeartbeatsHandler(service *node.NodeService) *HeartbeatsHandler {
	return &HeartbeatsHandler{
		nodeService: service,
	}
}

func (h *HeartbeatsHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(nodeAuthMiddleware)
		protected.Post("/heartbeat", h.HandleHeartbeat)
		protected.Post("/version", h.HandleVersionReport)
	})

	return r
}

func (h *HeartbeatsHandler) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	// El node_id viene del token validado, no del body
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Actualizar last_seen del nodo
	if err := h.nodeService.UpdateLastSeen(nodeToken.NodeID); err != nil {
		log.Printf("[ERROR] heartbeat for node %s: %v", nodeToken.NodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type versionReportRequest struct {
	Version string `json:"version"`
}

func (h *HeartbeatsHandler) HandleVersionReport(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req versionReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.nodeService.UpdateVersionAgent(nodeToken.NodeID, req.Version); err != nil {
		log.Printf("[ERROR] version report for node %s: %v", nodeToken.NodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
