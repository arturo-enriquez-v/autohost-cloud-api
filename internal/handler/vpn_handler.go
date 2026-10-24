// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/domain/vpn"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type VPNHandler struct {
	service *vpn.Service
	nodeSvc *node.Service
}

func NewVPNHandler(service *vpn.Service, nodeSvc *node.Service) *VPNHandler {
	return &VPNHandler{service: service, nodeSvc: nodeSvc}
}

func (h *VPNHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Post("/key", h.GenerateVPNKey)
		protected.Get("/key", h.GenerateVPNKey)
	})

	r.Group(func(nodeAuth chi.Router) {
		if nodeAuthMiddleware != nil {
			nodeAuth.Use(nodeAuthMiddleware)
		}
		nodeAuth.Post("/status", h.UpdateVPNStatus)
	})

	return r
}

func (h *VPNHandler) GenerateVPNKey(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	resp, err := h.service.GenerateVPNKey(r.Context(), claims.UserID)
	if err != nil {
		log.Printf("[ERROR] generate vpn key for user %s: %v", claims.UserID, err)
		http.Error(w, "could not generate vpn key", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *VPNHandler) UpdateVPNStatus(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		IPVPN string `json:"ip_vpn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.nodeSvc.UpdateVPNIP(nodeToken.NodeID, req.IPVPN); err != nil {
		log.Printf("[ERROR] update vpn ip for node %s: %v", nodeToken.NodeID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("📌 VPN status updated for node %s -> ip_vpn: %q", nodeToken.NodeID, req.IPVPN)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"ip_vpn": req.IPVPN,
	})
}
