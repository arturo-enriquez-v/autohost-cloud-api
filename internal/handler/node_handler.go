package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type NodeHandler struct {
	service          *node.Service
	nodeTokenService *nodetoken.Service
}

func NewNodeHandler(service *node.Service, nodeTokenService *nodetoken.Service) *NodeHandler {
	return &NodeHandler{service: service, nodeTokenService: nodeTokenService}
}

func (h *NodeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.With(middleware.Auth).Get("/", h.List)
	r.With(middleware.Auth).Get("/with-metrics", h.ListWithMetrics)
	r.With(middleware.Auth).Put("/", h.Update)
	r.With(middleware.Auth).Delete("/{id}", h.Delete)
	return r
}

func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Usar el servicio para obtener nodos
	nodes, err := h.service.GetByOwner(claims.UserID)
	if err != nil {
		log.Printf("[ERROR] list nodes: %v", err)
		http.Error(w, "could not list nodes", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

func (h *NodeHandler) ListWithMetrics(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Obtener nodos con sus últimas métricas
	nodes, err := h.service.GetByOwnerWithMetrics(claims.UserID)
	if err != nil {
		log.Printf("[ERROR] list nodes with metrics: %v", err)
		http.Error(w, "could not list nodes", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

func (h *NodeHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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

	node, err := h.service.Update(input.ID, input.Hostname)
	if err != nil {
		log.Printf("[ERROR] update node: %v", err)
		http.Error(w, "could not update node", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

func (h *NodeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
		http.Error(w, "could not find node", http.StatusInternalServerError)
		return
	}

	if n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Revoca tokens del nodo antes de eliminar
	if err := h.nodeTokenService.RevokeByNodeID(nodeID); err != nil {
		log.Printf("[ERROR] revoke node token: %v", err)
		http.Error(w, "could not revoke node token", http.StatusInternalServerError)
		return
	}

	if err := h.service.Delete(nodeID); err != nil {
		log.Printf("[ERROR] delete node: %v", err)
		http.Error(w, "could not delete node", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
