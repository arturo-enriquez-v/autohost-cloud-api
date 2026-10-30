// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// HypervisorHandler exposes the virtualization capacity reported by a node's
// agent (KVM + libvirt). Only the node owner can read it.
type HypervisorHandler struct {
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.NodeService
}

func NewHypervisorHandler(grpc *grpcserver.NodeAgentServer, nodeSvc *node.NodeService) *HypervisorHandler {
	return &HypervisorHandler{grpc: grpc, nodeSvc: nodeSvc}
}

func (h *HypervisorHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{nodeId}", h.get)
	})
	return r
}

type hypervisorResponse struct {
	// Reported is false when the agent never announced hypervisor support
	// (regular node, or agent not connected since the feature was added).
	Reported bool                       `json:"reported"`
	Info     *grpcserver.HypervisorInfo `json:"info,omitempty"`
}

// get handles GET /v1/hypervisors/{nodeId}.
func (h *HypervisorHandler) get(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	if h.nodeSvc != nil {
		n, err := h.nodeSvc.GetByID(nodeID)
		if err != nil || n == nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if n.OwnerID == nil || *n.OwnerID != claims.UserID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	resp := hypervisorResponse{}
	if h.grpc != nil {
		if info, ok := h.grpc.GetHypervisorInfo(r.Context(), nodeID); ok {
			resp.Reported = true
			resp.Info = info
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
