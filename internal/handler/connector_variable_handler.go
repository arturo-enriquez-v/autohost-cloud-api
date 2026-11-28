// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
)

// VariableRoutes serves /v1/connector-variables: values shared by a project's connectors. Every route
// needs connectors:manage on the variable's project, the same permission that edits connectors.
func (h *ConnectorHandler) VariableRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.ListVariables)
	r.Post("/", h.CreateVariable)
	r.Get("/{id}", h.GetVariable)
	r.Put("/{id}", h.UpdateVariable)
	r.Delete("/{id}", h.DeleteVariable)
	return r
}

// loadVariable returns the variable in {id} if the caller can manage its project's connectors: 404 when
// the project is not visible, 403 when it is but connectors:manage is missing.
func (h *ConnectorHandler) loadVariable(w http.ResponseWriter, r *http.Request) *connectors.Variable {
	claims := middleware.GetClaims(r.Context())
	v, err := h.svc.GetVariable(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeConnectorError(w, err, "get variable")
		return nil
	}
	perms, err := h.projectPerms(r.Context(), claims.UserID, v.ProjectID)
	if err != nil {
		log.Printf("[ERROR] connectors: permissions for variable %s: %v", v.ID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not check permissions")
		return nil
	}
	if !identity.HasPermission(perms, identity.PermConnectorsRead) {
		response.RespondError(w, http.StatusNotFound, "variable not found")
		return nil
	}
	if !identity.HasPermission(perms, identity.PermConnectorsManage) {
		response.RespondError(w, http.StatusForbidden, "forbidden: requires "+identity.PermConnectorsManage)
		return nil
	}
	return v
}

// ListVariables returns the variables of ?project_id. Secret values are never returned.
func (h *ConnectorHandler) ListVariables(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if !h.requireProjectManage(w, r, projectID) {
		return
	}
	list, err := h.svc.ListVariables(r.Context(), projectID)
	if err != nil {
		writeConnectorError(w, err, "list variables")
		return
	}
	if list == nil {
		list = []*connectors.Variable{}
	}
	response.RespondJSON(w, http.StatusOK, list)
}

func (h *ConnectorHandler) GetVariable(w http.ResponseWriter, r *http.Request) {
	if v := h.loadVariable(w, r); v != nil {
		response.RespondJSON(w, http.StatusOK, v)
	}
}

func (h *ConnectorHandler) CreateVariable(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var in connectors.VariableInput
	if err := decodeJSON(r, &in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !h.requireProjectManage(w, r, in.ProjectID) {
		return
	}
	v, err := h.svc.CreateVariable(r.Context(), in, claims.UserID)
	if err != nil {
		writeConnectorError(w, err, "create variable")
		return
	}
	response.RespondJSON(w, http.StatusCreated, v)
}

func (h *ConnectorHandler) UpdateVariable(w http.ResponseWriter, r *http.Request) {
	existing := h.loadVariable(w, r)
	if existing == nil {
		return
	}
	var in connectors.VariableInput
	if err := decodeJSON(r, &in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	v, err := h.svc.UpdateVariable(r.Context(), existing, in)
	if err != nil {
		writeConnectorError(w, err, "update variable")
		return
	}
	response.RespondJSON(w, http.StatusOK, v)
}

func (h *ConnectorHandler) DeleteVariable(w http.ResponseWriter, r *http.Request) {
	v := h.loadVariable(w, r)
	if v == nil {
		return
	}
	if err := h.svc.DeleteVariable(r.Context(), v); err != nil {
		writeConnectorError(w, err, "delete variable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
