// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

type NodeHandler struct {
	service          *node.NodeService
	nodeTokenService *node.NodeTokenService
	rbacSvc          *identity.RBACService
}

func NewNodeHandler(service *node.NodeService, nodeTokenService *node.NodeTokenService) *NodeHandler {
	return &NodeHandler{service: service, nodeTokenService: nodeTokenService}
}

func (h *NodeHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func (h *NodeHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.Auth)

	r.Get("/", h.List)
	r.Get("/with-metrics", h.ListWithMetrics)
	r.Put("/", h.Update)
	r.Get("/{id}", h.Get)
	r.Delete("/{id}", h.Delete)

	return r
}

func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	if h.rbacSvc != nil {
		isAdmin := checkAdminAccess(r.Context(), h.rbacSvc, userID)
		allNodes, err := h.service.GetAll()
		if err == nil && len(allNodes) > 0 {
			out := make([]*node.Node, 0, len(allNodes))
			for _, n := range allNodes {
				if isAdmin {
					n.RolePreset = identity.NodePresetAdmin
					n.Permissions = []string{identity.PermWildcard}
					out = append(out, n)
					continue
				}
				preset, perms, allowed, err := h.rbacSvc.GetEffectiveNodePermissions(r.Context(), userID, n.ID)
				if err == nil && allowed && identity.HasPermission(perms, identity.PermNodeRead) {
					n.RolePreset = preset
					n.Permissions = perms
					out = append(out, n)
				}
			}
			response.RespondJSON(w, http.StatusOK, out)
			return
		}
		if !isAdmin {
			// Fallback for mock repositories where GetAll returns empty
			uPerms, err := h.rbacSvc.GetUserWithPermissions(userID)
			if err == nil && uPerms != nil {
				out := make([]*node.Node, 0, len(uPerms.NodePermissions))
				for _, np := range uPerms.NodePermissions {
					if n, err := h.service.GetByID(np.NodeID); err == nil && n != nil {
						preset, perms, allowed, _ := h.rbacSvc.GetEffectiveNodePermissions(r.Context(), userID, n.ID)
						if allowed && identity.HasPermission(perms, identity.PermNodeRead) {
							n.RolePreset = preset
							n.Permissions = perms
							out = append(out, n)
						}
					}
				}
				response.RespondJSON(w, http.StatusOK, out)
				return
			}
		}
	}

	nodes, err := h.service.GetByOwner(userID)
	if err != nil {
		log.Printf("[ERROR] list nodes: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	for _, n := range nodes {
		n.RolePreset = identity.NodePresetAdmin
		n.Permissions = []string{identity.PermWildcard}
	}

	response.RespondJSON(w, http.StatusOK, nodes)
}

func (h *NodeHandler) ListWithMetrics(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	if h.rbacSvc != nil {
		isAdmin := checkAdminAccess(r.Context(), h.rbacSvc, userID)
		allNodes, err := h.service.GetAllWithMetrics()
		if err == nil && len(allNodes) > 0 {
			out := make([]*node.NodeWithMetrics, 0, len(allNodes))
			for _, n := range allNodes {
				if isAdmin {
					n.RolePreset = identity.NodePresetAdmin
					n.Permissions = []string{identity.PermWildcard}
					out = append(out, n)
					continue
				}
				preset, perms, allowed, err := h.rbacSvc.GetEffectiveNodePermissions(r.Context(), userID, n.ID)
				if err == nil && allowed && identity.HasPermission(perms, identity.PermNodeRead) {
					n.RolePreset = preset
					n.Permissions = perms
					out = append(out, n)
				}
			}
			response.RespondJSON(w, http.StatusOK, out)
			return
		}
	}

	// Obtener nodos con sus últimas métricas
	nodes, err := h.service.GetByOwnerWithMetrics(userID)
	if err != nil {
		log.Printf("[ERROR] list nodes with metrics: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	for _, n := range nodes {
		n.RolePreset = identity.NodePresetAdmin
		n.Permissions = []string{identity.PermWildcard}
	}

	response.RespondJSON(w, http.StatusOK, nodes)
}

func (h *NodeHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}

	n, err := h.service.GetByID(nodeID)
	if err != nil {
		if err == node.ErrNodeNotFound {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		log.Printf("[ERROR] get node: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not find node")
		return
	}

	if !checkNodeAccess(r.Context(), h.rbacSvc, n, userID, identity.PermNodeRead) {
		response.RespondError(w, http.StatusForbidden, "forbidden")
		return
	}

	rolePreset := identity.NodePresetAdmin
	perms := []string{identity.PermWildcard}
	if h.rbacSvc != nil {
		if rp, p, ok, _ := h.rbacSvc.GetEffectiveNodePermissions(r.Context(), userID, n.ID); ok {
			rolePreset = rp
			perms = p
		}
	}

	result := &node.NodeWithMetrics{
		ID:           n.ID,
		Hostname:     n.Hostname,
		IPLocal:      n.IPLocal,
		IPVPN:        n.IPVPN,
		OS:           n.OS,
		Arch:         n.Arch,
		VersionAgent: n.VersionAgent,
		OwnerID:      n.OwnerID,
		ProjectID:    n.ProjectID,
		IsLocal:      n.IsLocal,
		LastSeenAt:   n.LastSeenAt,
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
		RolePreset:   rolePreset,
		Permissions:  perms,
	}

	response.RespondJSON(w, http.StatusOK, result)
}

func (h *NodeHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	var input struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	existing, err := h.service.GetByID(input.ID)
	if err != nil || existing == nil {
		response.RespondError(w, http.StatusNotFound, "node not found")
		return
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, existing, userID, identity.PermNodeUpdate) {
		response.RespondError(w, http.StatusForbidden, "forbidden")
		return
	}

	updatedNode, err := h.service.Update(input.ID, input.Hostname)
	if err != nil {
		log.Printf("[ERROR] update node: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not update node")
		return
	}

	response.RespondJSON(w, http.StatusOK, updatedNode)
}

func (h *NodeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing node id")
		return
	}

	n, err := h.service.GetByID(nodeID)
	if err != nil {
		if err == node.ErrNodeNotFound {
			response.RespondError(w, http.StatusNotFound, "node not found")
			return
		}
		log.Printf("[ERROR] get node: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not find node")
		return
	}

	if !checkNodeAccess(r.Context(), h.rbacSvc, n, userID, identity.PermNodeDelete) {
		response.RespondError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Revoca tokens del nodo antes de eliminar
	if err := h.nodeTokenService.RevokeByNodeID(nodeID); err != nil {
		log.Printf("[ERROR] revoke node token: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not revoke node token")
		return
	}

	if err := h.service.Delete(nodeID); err != nil {
		log.Printf("[ERROR] delete node: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not delete node")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
