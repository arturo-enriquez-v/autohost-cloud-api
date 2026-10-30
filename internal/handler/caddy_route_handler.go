// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

// CaddyRouteHandler manages Caddy reverse-proxy routes for a node.
type CaddyRouteHandler struct {
	service    *apps.CaddyRouteService
	jobService *jobs.JobService
	dispatcher dispatcher.ParamDispatcher
	nodeSvc    *node.NodeService
}

func NewCaddyRouteHandler(
	service *apps.CaddyRouteService,
	jobService *jobs.JobService,
	disp dispatcher.ParamDispatcher,
	nodeSvc *node.NodeService,
) *CaddyRouteHandler {
	return &CaddyRouteHandler{
		service:    service,
		jobService: jobService,
		dispatcher: disp,
		nodeSvc:    nodeSvc,
	}
}

func (h *CaddyRouteHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/{nodeId}", h.List)
	r.Put("/{nodeId}", h.Upsert)
	r.Delete("/{nodeId}/{domain}", h.Delete)
	return r
}

// List returns all Caddy routes stored for the given node.
// GET /v1/caddy-routes/{nodeId}
func (h *CaddyRouteHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
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

	routes, err := h.service.ListByNode(nodeID)
	if err != nil {
		log.Printf("[ERROR] list caddy routes for node %s: %v", nodeID, err)
		http.Error(w, "could not list caddy routes", http.StatusInternalServerError)
		return
	}
	if routes == nil {
		routes = []*apps.CaddyRoute{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(routes)
}

type upsertCaddyRouteRequest struct {
	Domain   string `json:"domain"`
	Upstream string `json:"upstream"`
}

// Upsert creates or updates a Caddy reverse-proxy route and dispatches it to the agent.
// PUT /v1/caddy-routes/{nodeId}
func (h *CaddyRouteHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
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

	var req upsertCaddyRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Domain == "" || req.Upstream == "" {
		http.Error(w, "domain and upstream are required", http.StatusBadRequest)
		return
	}

	// Persist the route in the DB.
	saved, err := h.service.Upsert(&apps.CaddyRoute{
		NodeID:   nodeID,
		UserID:   claims.UserID,
		Domain:   req.Domain,
		Upstream: req.Upstream,
	})
	if err != nil {
		log.Printf("[ERROR] upsert caddy route for node %s: %v", nodeID, err)
		http.Error(w, "could not save caddy route", http.StatusInternalServerError)
		return
	}

	// Dispatch the caddy.upsert-route job to the agent asynchronously.
	params, _ := json.Marshal(map[string]string{
		"domain":   req.Domain,
		"upstream": req.Upstream,
	})
	j, err := h.jobService.Dispatch(nodeID, "caddy.upsert-route", jobs.CommandTypeDefault)
	if err != nil {
		log.Printf("[WARN] create caddy.upsert-route job for node %s: %v", nodeID, err)
	} else if err := h.dispatcher.DispatchJobWithParams(nodeID, j.ID, "caddy.upsert-route", jobs.CommandTypeDefault, string(params)); err != nil {
		log.Printf("[WARN] dispatch caddy.upsert-route to node %s: %v", nodeID, err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(saved)
}

// Delete removes a Caddy route from the DB and asks the agent to delete it on the node.
// DELETE /v1/caddy-routes/{nodeId}/{domain}
func (h *CaddyRouteHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	domain := chi.URLParam(r, "domain")

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

	if err := h.service.Delete(nodeID, domain); err != nil {
		if errors.Is(err, apps.ErrCaddyRouteNotFound) {
			http.Error(w, "route not found", http.StatusNotFound)
			return
		}
		log.Printf("[ERROR] delete caddy route %s for node %s: %v", domain, nodeID, err)
		http.Error(w, "could not delete caddy route", http.StatusInternalServerError)
		return
	}

	// Dispatch the caddy.delete-route job to the agent asynchronously.
	params, _ := json.Marshal(map[string]string{"domain": domain})
	j, err := h.jobService.Dispatch(nodeID, "caddy.delete-route", jobs.CommandTypeDefault)
	if err != nil {
		log.Printf("[WARN] create caddy.delete-route job for node %s: %v", nodeID, err)
	} else if err := h.dispatcher.DispatchJobWithParams(nodeID, j.ID, "caddy.delete-route", jobs.CommandTypeDefault, string(params)); err != nil {
		log.Printf("[WARN] dispatch caddy.delete-route to node %s: %v", nodeID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}
