// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ClusterHandler struct {
	service *infra.ClusterManager
}

func NewClusterHandler(service *infra.ClusterManager) *ClusterHandler {
	return &ClusterHandler{service: service}
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
	clusterID := chi.URLParam(r, "clusterId")
	det, err := h.service.GetClusterWithDetails(r.Context(), clusterID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(det)
}

func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "clusterId")
	if err := h.service.DeleteCluster(r.Context(), clusterID); err != nil {
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
	clusterID := chi.URLParam(r, "clusterId")
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

	if err := h.service.AddNodeToCluster(r.Context(), clusterID, req.NodeID, req.Role); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *ClusterHandler) RemoveNode(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "clusterId")
	nodeID := chi.URLParam(r, "nodeId")
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
	clusterID := chi.URLParam(r, "clusterId")
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
	serviceID := chi.URLParam(r, "serviceId")
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
	serviceID := chi.URLParam(r, "serviceId")
	if err := h.service.DeleteService(r.Context(), serviceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ClusterHandler) GetAutoscaling(w http.ResponseWriter, r *http.Request) {
	serviceID := chi.URLParam(r, "serviceId")
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
	serviceID := chi.URLParam(r, "serviceId")
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
