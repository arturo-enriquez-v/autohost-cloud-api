package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	networkstat "github.com/arturo/autohost-cloud-api/internal/domain/network_stat"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeNetworkHandler handles agent ingestion and frontend reads of
// network stats (interface counters + listening ports).
type NodeNetworkHandler struct {
	svc     *networkstat.Service
	nodeSvc *node.Service
}

func NewNodeNetworkHandler(svc *networkstat.Service, nodeSvc *node.Service) *NodeNetworkHandler {
	return &NodeNetworkHandler{svc: svc, nodeSvc: nodeSvc}
}

func (h *NodeNetworkHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	// Agent → POST /v1/agent/network-stats (authenticated with node token)
	r.Group(func(nodeAuth chi.Router) {
		if nodeAuthMiddleware != nil {
			nodeAuth.Use(nodeAuthMiddleware)
		}
		nodeAuth.Post("/", h.IngestStats)
	})

	return r
}

// NodeRoutes returns routes mounted under /v1/node-network/{nodeId}
// and protected by user JWT.
func (h *NodeNetworkHandler) NodeRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/{nodeId}", h.GetLatest)
	return r
}

// IngestStats receives the POST from the agent and stores the snapshot.
func (h *NodeNetworkHandler) IngestStats(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var payload networkstat.IngestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if err := h.svc.Ingest(r.Context(), nodeToken.NodeID, &payload); err != nil {
		log.Printf("[ERROR] ingest network stats for node %s: %v", nodeToken.NodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetLatest returns the latest network snapshot for a node (user-facing).
func (h *NodeNetworkHandler) GetLatest(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	if nodeID == "" {
		http.Error(w, "missing nodeId", http.StatusBadRequest)
		return
	}

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

	snap, err := h.svc.GetLatest(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Return empty payload when no data yet
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(&networkstat.NodeNetworkStats{NodeID: nodeID})
			return
		}
		log.Printf("[ERROR] get network stats for node %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}
