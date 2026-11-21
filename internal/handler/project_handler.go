// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ProjectHandler struct {
	projectService *apps.ProjectService
	nodeService    *node.NodeService
	rbacSvc        *identity.RBACService
}

func NewProjectHandler(projectService *apps.ProjectService, nodeService *node.NodeService) *ProjectHandler {
	return &ProjectHandler{projectService: projectService, nodeService: nodeService}
}

func (h *ProjectHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
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
	r.With(h.nodePerm(identity.PermNodeUpdate)).Put("/{id}/nodes/{nodeId}", h.AssignNode)
	r.With(h.nodePerm(identity.PermNodeUpdate)).Delete("/{id}/nodes/{nodeId}", h.RemoveNode)
	// Project members & permissions
	r.Get("/{id}/members", h.ListMembers)
	r.Post("/{id}/members", h.AssignMember)
	r.Delete("/{id}/members/{userId}", h.RemoveMember)
	return r
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.rbacSvc != nil {
		if checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
			projects, err := h.projectService.GetAll()
			if err == nil && len(projects) > 0 {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(projects)
				return
			}
		} else {
			// Member user: return only projects they are assigned to or have node permissions in
			uPerms, err := h.rbacSvc.GetUserWithPermissions(claims.UserID)
			if err == nil && uPerms != nil {
				allowedProjIDs := make(map[string]bool)
				for _, pm := range uPerms.Projects {
					allowedProjIDs[pm.ProjectID] = true
				}
				for _, np := range uPerms.NodePermissions {
					if np.ProjectID != nil && *np.ProjectID != "" {
						allowedProjIDs[*np.ProjectID] = true
					}
				}
				out := make([]*apps.Project, 0, len(allowedProjIDs))
				allProjs, _ := h.projectService.GetAll()
				if len(allProjs) > 0 {
					for _, p := range allProjs {
						if allowedProjIDs[p.ID] {
							out = append(out, p)
						}
					}
				} else {
					for pid := range allowedProjIDs {
						if p, err := h.projectService.GetByID(pid); err == nil && p != nil {
							out = append(out, p)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(out)
				return
			}
		}
	}

	projects, err := h.projectService.GetByUser(claims.UserID)
	if err != nil {
		log.Printf("[ERROR] list projects: %v", err)
		http.Error(w, "could not list projects", http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []*apps.Project{}
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
	if h.rbacSvc != nil && !checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		http.Error(w, "forbidden: only administrators can create projects", http.StatusForbidden)
		return
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
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}

	allowed := p.UserID == claims.UserID
	if !allowed && h.rbacSvc != nil {
		isAdmin, isMember, _ := h.rbacSvc.CanAccessProject(r.Context(), claims.UserID, id)
		allowed = isAdmin || isMember
	}
	if !allowed {
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

	var p *apps.Project
	var err error
	if h.rbacSvc != nil && checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		p, err = h.projectService.UpdateAsAdmin(id, req.Name)
	} else {
		p, err = h.projectService.Update(id, claims.UserID, req.Name)
	}
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, apps.ErrUnauthorized) {
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
	var err error
	if h.rbacSvc != nil && checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		err = h.projectService.DeleteAsAdmin(id)
	} else {
		err = h.projectService.Delete(id, claims.UserID)
	}
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, apps.ErrUnauthorized) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "could not delete project", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) canManageProject(ctx context.Context, userID string, p *apps.Project) bool {
	if p != nil && p.UserID == userID {
		return true
	}
	return checkAdminAccess(ctx, h.rbacSvc, userID)
}

func (h *ProjectHandler) AssignNode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")

	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if !h.canManageProject(r.Context(), claims.UserID, p) {
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

	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if !h.canManageProject(r.Context(), claims.UserID, p) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Only detach the node from this project: without this check, managing any project would allow
	// detaching a node from a different project (revoking the access its members inherit).
	n, err := h.nodeService.GetByID(nodeID)
	if err != nil || n == nil || n.ProjectID == nil || *n.ProjectID != projectID {
		http.Error(w, "node not found in this project", http.StatusNotFound)
		return
	}

	if err := h.nodeService.RemoveFromProject(nodeID); err != nil {
		log.Printf("[ERROR] remove node from project: %v", err)
		http.Error(w, "could not remove node", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMembers lists all users assigned to a project and their node permissions within the project.
// GET /v1/projects/{id}/members
func (h *ProjectHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rbacSvc == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
		return
	}

	projectID := chi.URLParam(r, "id")
	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if !h.canManageProject(r.Context(), claims.UserID, p) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	members, err := h.rbacSvc.ListProjectMembers(projectID)
	if err != nil {
		log.Printf("[ERROR] list project members %s: %v", projectID, err)
		http.Error(w, "could not list project members", http.StatusInternalServerError)
		return
	}
	if members == nil {
		members = []*identity.ProjectMemberDetail{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}

type assignProjectMemberReq struct {
	UserID             string                              `json:"user_id"`
	DefaultRole        string                              `json:"default_role"`
	AllNodes           bool                                `json:"all_nodes"`
	DefaultPermissions []string                            `json:"default_permissions,omitempty"`
	NodePermissions    []identity.NodePermissionGrantInput `json:"node_permissions,omitempty"`
}

// AssignMember assigns or updates a user in a project along with optional node permissions.
// POST /v1/projects/{id}/members
func (h *ProjectHandler) AssignMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rbacSvc == nil {
		http.Error(w, "rbac service unavailable", http.StatusServiceUnavailable)
		return
	}

	projectID := chi.URLParam(r, "id")
	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if !h.canManageProject(r.Context(), claims.UserID, p) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req assignProjectMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	if err := h.rbacSvc.AssignProjectMember(claims.UserID, projectID, req.UserID, req.DefaultRole, req.AllNodes, req.DefaultPermissions, req.NodePermissions); err != nil {
		if errors.Is(err, identity.ErrUserNotFound) || errors.Is(err, identity.ErrNodeNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if errors.Is(err, identity.ErrNodeNotInProject) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("[ERROR] assign project member: %v", err)
		http.Error(w, "could not assign project member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember removes a user from a project and revokes their node permissions in that project.
// DELETE /v1/projects/{id}/members/{userId}
func (h *ProjectHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rbacSvc == nil {
		http.Error(w, "rbac service unavailable", http.StatusServiceUnavailable)
		return
	}

	projectID := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userId")
	p, err := h.projectService.GetByID(projectID)
	if err != nil {
		if errors.Is(err, apps.ErrProjectNotFound) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get project", http.StatusInternalServerError)
		return
	}
	if !h.canManageProject(r.Context(), claims.UserID, p) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.rbacSvc.RemoveProjectMember(projectID, targetUserID); err != nil {
		log.Printf("[ERROR] remove project member: %v", err)
		http.Error(w, "could not remove project member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// nodePerm returns the route-level node authorization middleware (see requireNodePerm).
// Assigning a node to (or removing it from) a project changes who inherits access to it, so the caller
// must control the node itself, not only the project.
func (h *ProjectHandler) nodePerm(perm string) func(http.Handler) http.Handler {
	return requireNodePerm(func() *identity.RBACService { return h.rbacSvc }, perm)
}
