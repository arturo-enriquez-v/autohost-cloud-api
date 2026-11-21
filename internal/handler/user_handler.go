// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

// UserHandler exposes administrative CRUD for users, roles, project memberships, and granular node permissions.
type UserHandler struct {
	rbacSvc *identity.RBACService
}

func NewUserHandler(rbacSvc *identity.RBACService) *UserHandler {
	return &UserHandler{rbacSvc: rbacSvc}
}

func (h *UserHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)

	// Accessible to any authenticated user
	r.Get("/permissions-catalog", h.GetCatalog)
	r.Get("/my-permissions", h.GetMyPermissions)

	// Admin-managed user & permission endpoints
	r.Get("/", h.ListUsers)
	r.Post("/", h.CreateUser)
	r.Get("/{id}", h.GetUser)
	r.Put("/{id}", h.UpdateUser)
	r.Delete("/{id}", h.DeleteUser)
	r.Put("/{id}/permissions", h.SetUserPermissions)
	r.Put("/{id}/nodes/{nodeId}/permissions", h.UpsertNodePermission)
	r.Delete("/{id}/nodes/{nodeId}/permissions", h.RemoveNodePermission)

	return r
}

func (h *UserHandler) requireAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return "", false
	}
	if !checkAdminAccess(r.Context(), h.rbacSvc, userID) {
		response.RespondError(w, http.StatusForbidden, "admin privileges required")
		return "", false
	}
	return userID, true
}

// GetCatalog returns the global roles, node presets, and granular permissions catalog.
// GET /v1/users/permissions-catalog
func (h *UserHandler) GetCatalog(w http.ResponseWriter, r *http.Request) {
	response.RespondJSON(w, http.StatusOK, identity.GetPermissionsCatalog())
}

// GetMyPermissions returns the caller's own RBAC profile, project memberships, and node permissions.
// GET /v1/users/my-permissions
func (h *UserHandler) GetMyPermissions(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	u, err := h.rbacSvc.GetUserWithPermissions(userID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			response.RespondError(w, http.StatusNotFound, "user not found")
			return
		}
		log.Printf("[ERROR] get my permissions: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not load permissions")
		return
	}
	response.RespondJSON(w, http.StatusOK, u)
}

// ListUsers returns all users with their assigned projects and node permissions.
// GET /v1/users
func (h *UserHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}

	users, err := h.rbacSvc.ListUsers()
	if err != nil {
		log.Printf("[ERROR] list users: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	response.RespondJSON(w, http.StatusOK, users)
}

// CreateUser creates a new user account and configures their role, projects, and granular node permissions.
// POST /v1/users
func (h *UserHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}

	var in identity.CreateUserInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	created, err := h.rbacSvc.CreateUser(actorID, in)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrUserAlreadyExists):
			response.RespondError(w, http.StatusConflict, "email already exists")
		case errors.Is(err, identity.ErrInvalidRole):
			response.RespondError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[ERROR] create user: %v", err)
			response.RespondError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	response.RespondJSON(w, http.StatusCreated, created)
}

// GetUser returns a single user and their permissions (admin or self).
// GET /v1/users/{id}
func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	callerID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	if targetID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id")
		return
	}

	if callerID != targetID && !checkAdminAccess(r.Context(), h.rbacSvc, callerID) {
		response.RespondError(w, http.StatusForbidden, "admin privileges required")
		return
	}

	u, err := h.rbacSvc.GetUserWithPermissions(targetID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			response.RespondError(w, http.StatusNotFound, "user not found")
			return
		}
		log.Printf("[ERROR] get user %s: %v", targetID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not get user")
		return
	}

	response.RespondJSON(w, http.StatusOK, u)
}

// UpdateUser updates a user's profile, role, active state, password, and optionally permissions.
// PUT /v1/users/{id}
func (h *UserHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	if targetID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id")
		return
	}

	var in identity.UpdateUserInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.rbacSvc.UpdateUser(actorID, targetID, in)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			response.RespondError(w, http.StatusNotFound, "user not found")
		case errors.Is(err, identity.ErrUserAlreadyExists):
			response.RespondError(w, http.StatusConflict, "email already exists")
		case errors.Is(err, identity.ErrCannotDemoteLastAdmin):
			response.RespondError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, identity.ErrInvalidRole):
			response.RespondError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[ERROR] update user %s: %v", targetID, err)
			response.RespondError(w, http.StatusInternalServerError, "could not update user")
		}
		return
	}

	response.RespondJSON(w, http.StatusOK, updated)
}

// DeleteUser removes a user account and revokes all their sessions and permissions.
// DELETE /v1/users/{id}
func (h *UserHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	if targetID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id")
		return
	}

	if err := h.rbacSvc.DeleteUser(actorID, targetID); err != nil {
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			response.RespondError(w, http.StatusNotFound, "user not found")
		case errors.Is(err, identity.ErrCannotDeleteSelf), errors.Is(err, identity.ErrCannotDemoteLastAdmin):
			response.RespondError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[ERROR] delete user %s: %v", targetID, err)
			response.RespondError(w, http.StatusInternalServerError, "could not delete user")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type setUserPermissionsReq struct {
	Projects        []identity.ProjectMembershipInput   `json:"projects"`
	NodePermissions []identity.NodePermissionGrantInput `json:"node_permissions"`
}

// SetUserPermissions replaces a user's project assignments and granular per-node permissions.
// PUT /v1/users/{id}/permissions
func (h *UserHandler) SetUserPermissions(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	if targetID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id")
		return
	}

	var req setUserPermissionsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.rbacSvc.SetUserPermissions(actorID, targetID, req.Projects, req.NodePermissions)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			response.RespondError(w, http.StatusNotFound, "user not found")
			return
		}
		log.Printf("[ERROR] set user permissions %s: %v", targetID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not update permissions")
		return
	}

	response.RespondJSON(w, http.StatusOK, updated)
}

// UpsertNodePermission configures granular permissions for a user on a single node.
// PUT /v1/users/{id}/nodes/{nodeId}/permissions
func (h *UserHandler) UpsertNodePermission(w http.ResponseWriter, r *http.Request) {
	actorID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")
	if targetID == "" || nodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id or node id")
		return
	}

	var in identity.NodePermissionGrantInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.NodeID = nodeID

	grant, err := h.rbacSvc.UpsertNodePermission(actorID, targetID, in)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			response.RespondError(w, http.StatusNotFound, "user not found")
			return
		}
		log.Printf("[ERROR] upsert node permission (user=%s node=%s): %v", targetID, nodeID, err)
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, grant)
}

// RemoveNodePermission revokes a user's permissions on a single node.
// DELETE /v1/users/{id}/nodes/{nodeId}/permissions
func (h *UserHandler) RemoveNodePermission(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	targetID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "nodeId")
	if targetID == "" || nodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing user id or node id")
		return
	}

	if err := h.rbacSvc.RemoveNodePermission(targetID, nodeID); err != nil {
		log.Printf("[ERROR] remove node permission (user=%s node=%s): %v", targetID, nodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not remove node permission")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
