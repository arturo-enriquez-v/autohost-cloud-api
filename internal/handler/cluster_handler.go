// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ClusterHandler struct {
	service *infra.ClusterManager
	rbacSvc *identity.RBACService
}

func NewClusterHandler(service *infra.ClusterManager) *ClusterHandler {
	return &ClusterHandler{service: service}
}

func (h *ClusterHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

// authorizeCluster loads the cluster from the URL and verifies the caller owns it (or is a global admin).
// It writes the error response and returns ok=false when access is denied.
func (h *ClusterHandler) authorizeCluster(w http.ResponseWriter, r *http.Request) (*infra.Cluster, bool) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	c, err := h.service.GetCluster(r.Context(), chi.URLParam(r, "clusterId"))
	if err != nil || c == nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return nil, false
	}
	if c.UserID != claims.UserID && !checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		// Same response as a missing cluster so IDs cannot be probed.
		http.Error(w, "cluster not found", http.StatusNotFound)
		return nil, false
	}
	return c, true
}

// authorizeClusterService verifies cluster ownership and that the service in the URL belongs to that cluster.
func (h *ClusterHandler) authorizeClusterService(w http.ResponseWriter, r *http.Request) (*infra.ClusterService, bool) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return nil, false
	}
	svc, err := h.service.GetService(r.Context(), chi.URLParam(r, "serviceId"))
	if err != nil || svc == nil || svc.ClusterID != c.ID {
		http.Error(w, "service not found", http.StatusNotFound)
		return nil, false
	}
	return svc, true
}

// canEnrollNode checks the caller may hand a node over to Swarm orchestration. Joining a cluster lets the
// cluster owner schedule arbitrary workloads on the node, so node-level update rights are required.
func (h *ClusterHandler) canEnrollNode(ctx context.Context, userID, nodeID string) bool {
	if h.rbacSvc == nil {
		return false
	}
	ok, err := h.rbacSvc.HasNodePermission(ctx, userID, nodeID, identity.PermNodeUpdate)
	return err == nil && ok
}

func (h *ClusterHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{clusterId}", h.GetByID)
	r.Delete("/{clusterId}", h.Delete)

	r.Post("/{clusterId}/nodes", h.AddNode)
	r.Delete("/{clusterId}/nodes/{nodeId}", h.RemoveNode)

	r.Post("/{clusterId}/services", h.CreateService)
	r.Put("/{clusterId}/services/{serviceId}/scale", h.ScaleService)
	r.Delete("/{clusterId}/services/{serviceId}", h.DeleteService)

	r.Get("/{clusterId}/services/{serviceId}/autoscaling", h.GetAutoscaling)
	r.Put("/{clusterId}/services/{serviceId}/autoscaling", h.ConfigureAutoscaling)

	return r
}

func (h *ClusterHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	clusters, err := h.service.ListClusters(r.Context(), claims.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clusters)
}

type createClusterReq struct {
	Name          string   `json:"name"`
	ManagerNodeID string   `json:"manager_node_id"`
	WorkerNodeIDs []string `json:"worker_node_ids"`
}

func (h *ClusterHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createClusterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.ManagerNodeID == "" {
		http.Error(w, "name and manager_node_id are required", http.StatusBadRequest)
		return
	}
	for _, nid := range append([]string{req.ManagerNodeID}, req.WorkerNodeIDs...) {
		if !h.canEnrollNode(r.Context(), claims.UserID, nid) {
			http.Error(w, "forbidden: insufficient permissions on node "+nid, http.StatusForbidden)
			return
		}
	}

	det, err := h.service.CreateCluster(r.Context(), claims.UserID, req.Name, req.ManagerNodeID, req.WorkerNodeIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(det)
}

func (h *ClusterHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return
	}
	det, err := h.service.GetClusterWithDetails(r.Context(), c.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(det)
}

func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteCluster(r.Context(), c.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type addNodeReq struct {
	NodeID string            `json:"node_id"`
	Role   infra.ClusterRole `json:"role"`
}

func (h *ClusterHandler) AddNode(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return
	}
	clusterID := c.ID
	var req addNodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		req.Role = infra.RoleWorker
	}
	if req.Role != infra.RoleWorker && req.Role != infra.RoleManager {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if claims := middleware.GetClaims(r.Context()); claims == nil || !h.canEnrollNode(r.Context(), claims.UserID, req.NodeID) {
		http.Error(w, "forbidden: insufficient permissions on node", http.StatusForbidden)
		return
	}

	if err := h.service.AddNodeToCluster(r.Context(), clusterID, req.NodeID, req.Role); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *ClusterHandler) RemoveNode(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return
	}
	clusterID := c.ID
	nodeID := chi.URLParam(r, "nodeId")
	// Only members of this cluster can be told to leave it (RemoveNodeFromCluster dispatches swarm.leave).
	if member, err := h.service.IsClusterMember(r.Context(), clusterID, nodeID); err != nil || !member {
		http.Error(w, "node is not a member of this cluster", http.StatusNotFound)
		return
	}
	if err := h.service.RemoveNodeFromCluster(r.Context(), clusterID, nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createServiceReq struct {
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	Ports    string            `json:"ports"`
	EnvVars  map[string]string `json:"env_vars"`
	Replicas int               `json:"replicas"`
}

func (h *ClusterHandler) CreateService(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorizeCluster(w, r)
	if !ok {
		return
	}
	clusterID := c.ID
	var req createServiceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Image == "" {
		http.Error(w, "name and image are required", http.StatusBadRequest)
		return
	}

	svc, err := h.service.CreateService(r.Context(), clusterID, req.Name, req.Image, req.Ports, req.EnvVars, req.Replicas)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(svc)
}

type scaleServiceReq struct {
	Replicas int `json:"replicas"`
}

func (h *ClusterHandler) ScaleService(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.authorizeClusterService(w, r)
	if !ok {
		return
	}
	serviceID := svc.ID
	var req scaleServiceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Replicas < 1 {
		http.Error(w, "replicas must be >= 1", http.StatusBadRequest)
		return
	}

	if err := h.service.ScaleService(r.Context(), serviceID, req.Replicas); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"success": true, "replicas": req.Replicas})
}

func (h *ClusterHandler) DeleteService(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.authorizeClusterService(w, r)
	if !ok {
		return
	}
	serviceID := svc.ID
	if err := h.service.DeleteService(r.Context(), serviceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ClusterHandler) GetAutoscaling(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.authorizeClusterService(w, r)
	if !ok {
		return
	}
	serviceID := svc.ID
	policy, err := h.service.GetAutoscaling(r.Context(), serviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if policy == nil {
		http.Error(w, "no autoscaling policy found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(policy)
}

func (h *ClusterHandler) ConfigureAutoscaling(w http.ResponseWriter, r *http.Request) {
	svc, ok := h.authorizeClusterService(w, r)
	if !ok {
		return
	}
	serviceID := svc.ID
	var policy infra.AutoscalePolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	saved, err := h.service.ConfigureAutoscaling(r.Context(), serviceID, &policy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(saved)
}
