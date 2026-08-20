package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/domain/plan"
	"github.com/arturo/autohost-cloud-api/internal/domain/project"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ProjectHandler struct {
	projectService *project.Service
	nodeService    *node.Service
	planService    *plan.Service
}

func NewProjectHandler(projectService *project.Service, nodeService *node.Service, planService *plan.Service) *ProjectHandler {
	return &ProjectHandler{projectService: projectService, nodeService: nodeService, planService: planService}
}

func (h *ProjectHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Put("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	// Node assignment
	r.Put("/{id}/nodes/{nodeId}", h.AssignNode)
	r.Delete("/{id}/nodes/{nodeId}", h.RemoveNode)
	return r
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projects, err := h.projectService.GetByUser(claims.UserID)
	if err != nil {
		log.Printf("[ERROR] list projects: %v", err)
		http.Error(w, "could not list projects", http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []*project.Project{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.planService != nil {
		projects, err := h.projectService.GetByUser(claims.UserID)
		if err == nil {
			if err := h.planService.CheckProjectLimit(claims.UserID, len(projects)); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	p, err := h.projectService.Create(claims.UserID, req.Name)
	if err != nil {
		log.Printf("[ERROR] create project: %v", err)
		http.Error(w, "could not create project", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	p, err := h.projectService.GetByID(id)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if p.UserID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	p, err := h.projectService.Update(id, claims.UserID, req.Name)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, project.ErrUnauthorized) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "could not update project", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	err := h.projectService.Delete(id, claims.UserID)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, project.ErrUnauthorized) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "could not delete project", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) AssignNode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")

	// Verify project belongs to this user
	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if p.UserID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.nodeService.AssignToProject(nodeID, projectID); err != nil {
		log.Printf("[ERROR] assign node to project: %v", err)
		http.Error(w, "could not assign node", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) RemoveNode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")

	// Verify project belongs to this user
	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if p.UserID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.nodeService.RemoveFromProject(nodeID); err != nil {
		log.Printf("[ERROR] remove node from project: %v", err)
		http.Error(w, "could not remove node", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
