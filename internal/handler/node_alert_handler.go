// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type NodeAlertHandler struct {
	service     *node.NodeAlertService
	nodeService *node.NodeService
}

func NewNodeAlertHandler(service *node.NodeAlertService, nodeService *node.NodeService) *NodeAlertHandler {
	return &NodeAlertHandler{service: service, nodeService: nodeService}
}

func (h *NodeAlertHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.With(middleware.Auth).Get("/{nodeId}", h.GetByNode)
	r.With(middleware.Auth).Put("/{nodeId}", h.UpsertByNode)
	return r
}

func (h *NodeAlertHandler) GetByNode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	// Verify the requesting user owns this node (prevents IDOR).
	n, err := h.nodeService.GetByID(nodeID)
	if err != nil || n == nil || n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	alert, err := h.service.FindByNodeID(nodeID)
	if err != nil {
		if errors.Is(err, node.ErrNodeAlertNotFound) {
			// Return empty config (node has no alerts configured yet)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(&node.NodeAlert{NodeID: nodeID})
			return
		}
		http.Error(w, "could not fetch alert config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alert)
}

type upsertAlertRequest struct {
	TelegramBotID    *string  `json:"telegram_bot_id"`
	TelegramBotToken string   `json:"telegram_bot_token"`
	TelegramChatID   string   `json:"telegram_chat_id"`
	CPUThreshold     *float32 `json:"cpu_threshold"`
	RAMThreshold     *float32 `json:"ram_threshold"`
	DiskThreshold    *float32 `json:"disk_threshold"`
	AlertOnOffline   bool     `json:"alert_on_offline"`
}

func (h *NodeAlertHandler) UpsertByNode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	// Verify the requesting user owns this node (prevents IDOR).
	n, err := h.nodeService.GetByID(nodeID)
	if err != nil || n == nil || n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req upsertAlertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	alert := &node.NodeAlert{
		NodeID:           nodeID,
		TelegramBotID:    req.TelegramBotID,
		TelegramBotToken: req.TelegramBotToken,
		TelegramChatID:   req.TelegramChatID,
		CPUThreshold:     req.CPUThreshold,
		RAMThreshold:     req.RAMThreshold,
		DiskThreshold:    req.DiskThreshold,
		AlertOnOffline:   req.AlertOnOffline,
	}

	result, err := h.service.Upsert(alert)
	if err != nil {
		http.Error(w, "could not save alert config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}
