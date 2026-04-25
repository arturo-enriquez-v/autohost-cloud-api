package handler

import (
	"encoding/json"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type HeartbeatsHandler struct {
	nodeService *node.Service
}

func NewHeartbeatsHandler(service *node.Service) *HeartbeatsHandler {
	return &HeartbeatsHandler{
		nodeService: service,
	}
}

func (h *HeartbeatsHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(nodeAuthMiddleware)
		protected.Post("/heartbeat", h.HandleHeartbeat)
		protected.Patch("/version", h.HandleVersionReport)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
