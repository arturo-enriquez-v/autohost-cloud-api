package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type NodeHandler struct {
	service *node.Service
}

func NewNodeHandler(service *node.Service) *NodeHandler {
	return &NodeHandler{service: service}
}

func (h *NodeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.With(middleware.Auth).Get("/", h.List)
	r.With(middleware.Auth).Get("/with-metrics", h.ListWithMetrics)
	r.With(middleware.Auth).Put("/", h.Update)
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
