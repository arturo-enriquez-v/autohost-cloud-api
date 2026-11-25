// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

// connectorRunLimiter bounds runs and tests per user: each one makes a request to an external API from
// the server.
var connectorRunLimiter = middleware.NewRateLimiter(60, time.Minute)

// ConnectorHandler serves /v1/connectors. Connectors belong to a project and permissions come from the
// project membership (identity.RBACService.ProjectPermissions).
type ConnectorHandler struct {
	svc      *connectors.Service
	projects *apps.ProjectService
	rbac     *identity.RBACService
}

func NewConnectorHandler(svc *connectors.Service, projects *apps.ProjectService) *ConnectorHandler {
	return &ConnectorHandler{svc: svc, projects: projects}
}

func (h *ConnectorHandler) SetRBACService(rbac *identity.RBACService) { h.rbac = rbac }

func (h *ConnectorHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Post("/test", h.Test)
	r.Get("/{id}", h.Get)
	r.Put("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	r.Post("/{id}/run", h.Run)
	r.Get("/{id}/runs", h.ListRuns)
	return r
}

// projectPerms returns the caller's project-level permissions; nil when the project does not exist or
// the caller has no access to it.
func (h *ConnectorHandler) projectPerms(ctx context.Context, userID, projectID string) ([]string, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, nil
	}
	p, err := h.projects.GetByID(projectID)
	if errors.Is(err, apps.ErrProjectNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if h.rbac == nil {
		if p.UserID == userID {
			return []string{identity.PermWildcard}, nil
		}
		return nil, nil
	}
	return h.rbac.ProjectPermissions(ctx, userID, projectID, p.UserID)
}

// loadConnector returns the connector in {id} and the caller's permissions on its project. It writes a
// 404 (the connector is not visible) or 403 (visible, but perm is missing) and returns nil on failure.
func (h *ConnectorHandler) loadConnector(w http.ResponseWriter, r *http.Request, perm string) (*connectors.Connector, []string) {
	claims := middleware.GetClaims(r.Context())
	c, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, connectors.ErrNotFound) {
		response.RespondError(w, http.StatusNotFound, "connector not found")
		return nil, nil
	}
	if err != nil {
		log.Printf("[ERROR] connectors: get %s: %v", chi.URLParam(r, "id"), err)
		response.RespondError(w, http.StatusInternalServerError, "could not get connector")
		return nil, nil
	}
	perms, err := h.projectPerms(r.Context(), claims.UserID, c.ProjectID)
	if err != nil {
		log.Printf("[ERROR] connectors: permissions for %s: %v", c.ID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not check permissions")
		return nil, nil
	}
	if !identity.HasPermission(perms, identity.PermConnectorsRead) {
		response.RespondError(w, http.StatusNotFound, "connector not found")
		return nil, nil
	}
	if !identity.HasPermission(perms, perm) {
		response.RespondError(w, http.StatusForbidden, "forbidden: requires "+perm)
		return nil, nil
	}
	return c, perms
}

// view hides the request details (URL, headers, body, auth settings) from callers without
// connectors:manage: they may contain tokens typed directly into them.
func view(c *connectors.Connector, perms []string) *connectors.Connector {
	if identity.HasPermission(perms, identity.PermConnectorsManage) {
		return c
	}
	cp := *c
	cp.URL, cp.Body, cp.Headers = "", "", []connectors.KeyValue{}
	cp.Auth = connectors.Auth{Type: c.Auth.Type}
	return &cp
}

func writeConnectorError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, connectors.ErrInvalid), errors.Is(err, connectors.ErrInvalidParams),
		errors.Is(err, connectors.ErrSecretOriginChanged):
		response.RespondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, connectors.ErrNotFound):
		response.RespondError(w, http.StatusNotFound, "connector not found")
	case errors.Is(err, connectors.ErrNameExists), errors.Is(err, connectors.ErrSecretRequired):
		response.RespondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, connectors.ErrSecretsNotConfigured):
		response.RespondError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, connectors.ErrUpstream):
		response.RespondError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, secretbox.ErrDecrypt):
		log.Printf("[ERROR] connectors: %s: stored credentials do not decrypt (was %s changed?)", op, secretbox.EnvKey)
		response.RespondError(w, http.StatusInternalServerError, "could not decrypt the connector's credentials")
	default:
		log.Printf("[ERROR] connectors: %s: %v", op, err)
		response.RespondError(w, http.StatusInternalServerError, "could not "+op)
	}
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func allowRun(w http.ResponseWriter, userID string) bool {
	if ok, retry := connectorRunLimiter.Allow(userID); !ok {
		middleware.WriteRateLimited(w, retry)
		return false
	}
	return true
}

// List returns the connectors of every project where the caller has connectors:read, optionally only
// those of ?project_id.
func (h *ConnectorHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	ctx := r.Context()
	filter := r.URL.Query().Get("project_id")

	var all []*connectors.Connector
	var err error
	if checkAdminAccess(ctx, h.rbac, claims.UserID) {
		all, err = h.svc.ListAll(ctx)
	} else {
		ids := map[string]bool{}
		owned, oerr := h.projects.GetByUser(claims.UserID)
		if oerr != nil {
			err = oerr
		}
		for _, p := range owned {
			ids[p.ID] = true
		}
		if err == nil && h.rbac != nil {
			memberships, merr := h.rbac.ListUserProjectMemberships(claims.UserID)
			err = merr
			for _, m := range memberships {
				ids[m.ProjectID] = true
			}
		}
		if err == nil {
			list := make([]string, 0, len(ids))
			for id := range ids {
				if filter == "" || id == filter {
					list = append(list, id)
				}
			}
			all, err = h.svc.ListByProjects(ctx, list)
		}
	}
	if err != nil {
		log.Printf("[ERROR] connectors: list for %s: %v", claims.UserID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not list connectors")
		return
	}

	permsByProject := map[string][]string{}
	out := []*connectors.Connector{}
	for _, c := range all {
		if filter != "" && c.ProjectID != filter {
			continue
		}
		perms, ok := permsByProject[c.ProjectID]
		if !ok {
			if perms, err = h.projectPerms(ctx, claims.UserID, c.ProjectID); err != nil {
				log.Printf("[ERROR] connectors: permissions for project %s: %v", c.ProjectID, err)
				response.RespondError(w, http.StatusInternalServerError, "could not list connectors")
				return
			}
			permsByProject[c.ProjectID] = perms
		}
		if identity.HasPermission(perms, identity.PermConnectorsRead) {
			out = append(out, view(c, perms))
		}
	}
	response.RespondJSON(w, http.StatusOK, out)
}

func (h *ConnectorHandler) Get(w http.ResponseWriter, r *http.Request) {
	c, perms := h.loadConnector(w, r, identity.PermConnectorsRead)
	if c == nil {
		return
	}
	response.RespondJSON(w, http.StatusOK, view(c, perms))
}

// requireProjectManage checks connectors:manage on a project given in a request body: 404 when the
// project is not visible, 403 when it is but the permission is missing.
func (h *ConnectorHandler) requireProjectManage(w http.ResponseWriter, r *http.Request, projectID string) bool {
	claims := middleware.GetClaims(r.Context())
	if projectID == "" {
		response.RespondError(w, http.StatusBadRequest, "project_id is required")
		return false
	}
	perms, err := h.projectPerms(r.Context(), claims.UserID, projectID)
	if err != nil {
		log.Printf("[ERROR] connectors: permissions for project %s: %v", projectID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not check permissions")
		return false
	}
	if !identity.HasPermission(perms, identity.PermConnectorsRead) {
		response.RespondError(w, http.StatusNotFound, "project not found")
		return false
	}
	if !identity.HasPermission(perms, identity.PermConnectorsManage) {
		response.RespondError(w, http.StatusForbidden, "forbidden: requires "+identity.PermConnectorsManage)
		return false
	}
	return true
}

func (h *ConnectorHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var in connectors.Input
	if err := decodeJSON(r, &in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !h.requireProjectManage(w, r, in.ProjectID) {
		return
	}
	c, err := h.svc.Create(r.Context(), in, claims.UserID)
	if err != nil {
		writeConnectorError(w, err, "create connector")
		return
	}
	response.RespondJSON(w, http.StatusCreated, c)
}

func (h *ConnectorHandler) Update(w http.ResponseWriter, r *http.Request) {
	existing, _ := h.loadConnector(w, r, identity.PermConnectorsManage)
	if existing == nil {
		return
	}
	var in connectors.Input
	if err := decodeJSON(r, &in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	c, err := h.svc.Update(r.Context(), existing, in)
	if err != nil {
		writeConnectorError(w, err, "update connector")
		return
	}
	response.RespondJSON(w, http.StatusOK, c)
}

func (h *ConnectorHandler) Delete(w http.ResponseWriter, r *http.Request) {
	c, _ := h.loadConnector(w, r, identity.PermConnectorsManage)
	if c == nil {
		return
	}
	if err := h.svc.Delete(r.Context(), c.ID); err != nil {
		writeConnectorError(w, err, "delete connector")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type runRequest struct {
	Params map[string]any `json:"params"`
}

// Run executes a connector: query connectors need connectors:read, action connectors connectors:run.
// Callers without connectors:manage only get the raw body when the connector has no response mapping.
func (h *ConnectorHandler) Run(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	c, perms := h.loadConnector(w, r, identity.PermConnectorsRead)
	if c == nil {
		return
	}
	if c.Kind == connectors.KindAction && !identity.HasPermission(perms, identity.PermConnectorsRun) {
		response.RespondError(w, http.StatusForbidden, "forbidden: requires "+identity.PermConnectorsRun)
		return
	}
	var req runRequest
	if err := decodeJSON(r, &req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !allowRun(w, claims.UserID) {
		return
	}
	res, err := h.svc.Run(r.Context(), c, req.Params, claims.UserID)
	if err != nil {
		writeConnectorError(w, err, "run connector")
		return
	}
	if !identity.HasPermission(perms, identity.PermConnectorsManage) && res.Mapped != nil {
		cp := *res
		cp.Body = nil
		res = &cp
	}
	response.RespondJSON(w, http.StatusOK, res)
}

type testRequest struct {
	connectors.Input
	// ConnectorID lets an edit form test with the stored credentials without sending them again.
	ConnectorID string         `json:"connector_id"`
	Params      map[string]any `json:"params"`
}

// Test executes an unsaved definition (connectors:manage on its project). Nothing is cached or recorded.
func (h *ConnectorHandler) Test(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !h.requireProjectManage(w, r, req.ProjectID) {
		return
	}
	var existing *connectors.Connector
	if req.ConnectorID != "" {
		c, err := h.svc.Get(r.Context(), req.ConnectorID)
		if errors.Is(err, connectors.ErrNotFound) || (err == nil && c.ProjectID != req.ProjectID) {
			response.RespondError(w, http.StatusNotFound, "connector not found")
			return
		}
		if err != nil {
			writeConnectorError(w, err, "test connector")
			return
		}
		existing = c
	}
	if !allowRun(w, claims.UserID) {
		return
	}
	res, err := h.svc.Test(r.Context(), req.Input, existing, req.Params)
	if err != nil {
		writeConnectorError(w, err, "test connector")
		return
	}
	response.RespondJSON(w, http.StatusOK, res)
}

func (h *ConnectorHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	c, _ := h.loadConnector(w, r, identity.PermConnectorsRead)
	if c == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := h.svc.ListRuns(r.Context(), c.ID, limit)
	if err != nil {
		writeConnectorError(w, err, "list connector runs")
		return
	}
	if runs == nil {
		runs = []*connectors.Run{}
	}
	response.RespondJSON(w, http.StatusOK, runs)
}
