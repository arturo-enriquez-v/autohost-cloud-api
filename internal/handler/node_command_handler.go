package handler

import (
	"encoding/json"
	"log"
	"net/http"

	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

// NodeCommandHandler exposes two groups of routes:
//   - Node-facing routes (nodeAuth): register / delete / list own commands
//   - User-facing routes (userAuth): list commands for a given node (dashboard)
type NodeCommandHandler struct {
	service *nodecommand.Service
}

func NewNodeCommandHandler(service *nodecommand.Service) *NodeCommandHandler {
	return &NodeCommandHandler{service: service}
}

func (h *NodeCommandHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	// Node-facing endpoints (agent calls these)
	r.Group(func(node chi.Router) {
		node.Use(nodeAuthMiddleware)
		node.Post("/", h.Register)
		node.Get("/", h.ListForNode)
		node.Delete("/{id}", h.Delete)
	})

	// User-facing endpoints (dashboard calls these) – authenticated via JWT
	r.With(middleware.Auth).Get("/node/{nodeID}", h.ListByNodeID)

	return r
}

// registerCommandRequest is the payload sent by the node agent.
type registerCommandRequest struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Type        nodecommand.CommandType `json:"type"` // "default" | "custom"
	ScriptPath  string                  `json:"script_path,omitempty"`
}

// Register upserts a command for the authenticated node.
// POST /v1/node-commands
func (h *NodeCommandHandler) Register(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req registerCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	cmd := &nodecommand.NodeCommand{
		NodeID:      nodeToken.NodeID,
		Name:        req.Name,
		Description: req.Description,
		Type:        req.Type,
		ScriptPath:  req.ScriptPath,
	}

	saved, err := h.service.Register(cmd)
	if err != nil {
		log.Printf("[ERROR] register node command: %v", err)
		http.Error(w, "could not register command", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(saved)
}

// ListForNode returns all commands registered by the authenticated node.
// GET /v1/node-commands
func (h *NodeCommandHandler) ListForNode(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	cmds, err := h.service.ListByNode(nodeToken.NodeID)
	if err != nil {
		log.Printf("[ERROR] list node commands: %v", err)
		http.Error(w, "could not list commands", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cmds)
}

// Delete removes a command by ID (must belong to the authenticated node).
// DELETE /v1/node-commands/{id}
func (h *NodeCommandHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.service.Delete(id); err != nil {
		if err == nodecommand.ErrCommandNotFound {
			http.Error(w, "command not found", http.StatusNotFound)
			return
		}
		log.Printf("[ERROR] delete node command: %v", err)
		http.Error(w, "could not delete command", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListByNodeID returns all commands for a specific node (dashboard / user auth).
// GET /v1/node-commands/node/{nodeID}
func (h *NodeCommandHandler) ListByNodeID(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeID")
	cmds, err := h.service.ListByNode(nodeID)
	if err != nil {
		log.Printf("[ERROR] list commands by nodeID: %v", err)
		http.Error(w, "could not list commands", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cmds)
}
