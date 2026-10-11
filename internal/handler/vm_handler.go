package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/domain/vm"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type VMHandler struct {
	vmService *vm.Service
}

func NewVMHandler(vmService *vm.Service) *VMHandler {
	return &VMHandler{vmService: vmService}
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

	var req vm.CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := h.vmService.CreateVM(r.Context(), nodeID, claims.UserID, req)
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
	list, err := h.vmService.ListByHost(r.Context(), nodeID, claims.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if list == nil {
		list = []*vm.VM{}
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

	if err := h.vmService.ActionVM(r.Context(), vmID, claims.UserID, body.Action); err != nil {
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
	if err := h.vmService.ActionVM(r.Context(), vmID, claims.UserID, "destroy"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
