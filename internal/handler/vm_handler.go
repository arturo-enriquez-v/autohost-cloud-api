// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type VMHandler struct {
	vmService *infra.VMService
	rbacSvc   *identity.RBACService
}

func NewVMHandler(vmService *infra.VMService) *VMHandler {
	return &VMHandler{vmService: vmService}
}

func (h *VMHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

// NodeVMRoutes mounts routes scoped to a host node (/node-vms/{nodeId} or /nodes/{nodeId}/vms).
func (h *VMHandler) NodeVMRoutes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Post("/{nodeId}", h.createVM)
		protected.Get("/{nodeId}", h.listVMs)
		protected.Post("/", h.createVM)
		protected.Get("/", h.listVMs)
	})
	return r
}

// VMRoutes mounts global routes scoped by VM ID (/vms/{vmId}).
func (h *VMHandler) VMRoutes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{vmId}", h.getVM)
		protected.Post("/{vmId}/actions", h.actionVM)
		protected.Delete("/{vmId}", h.deleteVM)
	})
	return r
}

func (h *VMHandler) createVM(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	if nodeID == "" {
		http.Error(w, "missing node ID", http.StatusBadRequest)
		return
	}

	ownerID := claims.UserID
	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermVMsManage); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ownerID = h.vmService.ResolveHostOwnerID(nodeID, claims.UserID)

	var req infra.CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := h.vmService.CreateVM(r.Context(), nodeID, ownerID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

func (h *VMHandler) listVMs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	ownerID := claims.UserID
	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermVMsRead); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ownerID = h.vmService.ResolveHostOwnerID(nodeID, claims.UserID)

	list, err := h.vmService.ListByHost(r.Context(), nodeID, ownerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if list == nil {
		list = []*infra.VM{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (h *VMHandler) getVM(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vmID := chi.URLParam(r, "vmId")
	if h.rbacSvc != nil {
		v, err := h.vmService.GetByIDUnchecked(r.Context(), vmID)
		if err != nil || v == nil {
			http.Error(w, "vm not found", http.StatusNotFound)
			return
		}
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, v.HostNodeID, identity.PermVMsRead); err != nil || !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
		return
	}

	v, err := h.vmService.GetByID(r.Context(), vmID, claims.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (h *VMHandler) actionVM(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vmID := chi.URLParam(r, "vmId")
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		http.Error(w, "invalid action body", http.StatusBadRequest)
		return
	}

	ownerID := claims.UserID
	if h.rbacSvc != nil {
		v, err := h.vmService.GetByIDUnchecked(r.Context(), vmID)
		if err != nil || v == nil {
			http.Error(w, "vm not found", http.StatusNotFound)
			return
		}
		reqPerm := identity.PermVMsAction
		if strings.EqualFold(strings.TrimSpace(body.Action), "destroy") {
			reqPerm = identity.PermVMsManage
		}
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, v.HostNodeID, reqPerm); err != nil || !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ownerID = v.OwnerID
	}

	if err := h.vmService.ActionVM(r.Context(), vmID, ownerID, body.Action); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": body.Action})
}

func (h *VMHandler) deleteVM(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vmID := chi.URLParam(r, "vmId")
	ownerID := claims.UserID
	if h.rbacSvc != nil {
		v, err := h.vmService.GetByIDUnchecked(r.Context(), vmID)
		if err != nil || v == nil {
			http.Error(w, "vm not found", http.StatusNotFound)
			return
		}
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, v.HostNodeID, identity.PermVMsManage); err != nil || !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ownerID = v.OwnerID
	}

	if err := h.vmService.ActionVM(r.Context(), vmID, ownerID, "destroy"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
