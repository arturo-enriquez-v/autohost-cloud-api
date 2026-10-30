// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

// InstallAppParams is the JSON payload the agent receives for marketplace installs.
type InstallAppParams struct {
	AppName string            `json:"app_name"`
	Compose string            `json:"compose"`
	Env     map[string]string `json:"env"`
}

// MarketplaceHandler handles marketplace app listing and deployment.
type MarketplaceHandler struct {
	jobService      *jobs.JobService
	marketplaceRepo apps.MarketplaceRepository
	dispatcher      dispatcher.ParamDispatcher
	r2              *platform.R2Client
}

func NewMarketplaceHandler(
	jobService *jobs.JobService,
	marketplaceRepo apps.MarketplaceRepository,
	disp dispatcher.ParamDispatcher,
	r2 *platform.R2Client,
) *MarketplaceHandler {
	return &MarketplaceHandler{
		jobService:      jobService,
		marketplaceRepo: marketplaceRepo,
		dispatcher:      disp,
		r2:              r2,
	}
}

func (h *MarketplaceHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/apps", h.ListApps)
	r.Post("/apps/deploy", h.DeployApp)
	r.Get("/apps/node/{nodeID}", h.ListNodeApps)
	return r
}

// ListApps returns the static app catalog.
// GET /v1/marketplace/apps
func (h *MarketplaceHandler) ListApps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps.Catalog)
}

// deployAppRequest is the payload from the dashboard.
type deployAppRequest struct {
	NodeID  string            `json:"node_id"`
	AppName string            `json:"app_name"`
	EnvVars map[string]string `json:"env_vars"`
}

// DeployApp downloads the compose template from R2, injects env vars,
// creates a job and dispatches it to the agent.
// POST /v1/marketplace/apps/deploy
func (h *MarketplaceHandler) DeployApp(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req deployAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" || req.AppName == "" {
		http.Error(w, "node_id and app_name are required", http.StatusBadRequest)
		return
	}

	// Validate the app is in the catalog.
	if _, ok := apps.GetCatalogEntry(req.AppName); !ok {
		http.Error(w, fmt.Sprintf("app %q not found in catalog", req.AppName), http.StatusBadRequest)
		return
	}

	if h.r2 == nil {
		http.Error(w, "marketplace storage (R2) is not configured on this server", http.StatusNotImplemented)
		return
	}

	// Download compose template from R2.
	composeKey := req.AppName + ".yml"
	composeContent, err := h.r2.GetObject(r.Context(), composeKey)
	if err != nil {
		log.Printf("[ERROR] marketplace: fetch compose from R2 (%s): %v", composeKey, err)
		http.Error(w, "could not fetch app template", http.StatusInternalServerError)
		return
	}

	// Build the JSON params to send to the agent (and persist in the DB).
	installParams := InstallAppParams{
		AppName: req.AppName,
		Compose: string(composeContent),
		Env:     req.EnvVars,
	}
	paramsBytes, err := json.Marshal(installParams)
	if err != nil {
		log.Printf("[ERROR] marketplace: marshal params: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	paramsJSON := string(paramsBytes)

	// Create the tracking record.
	mApp, err := h.marketplaceRepo.Create(&apps.MarketplaceApp{
		NodeID:  req.NodeID,
		AppName: req.AppName,
		Status:  apps.StatusInstalling,
	})
	if err != nil {
		log.Printf("[ERROR] marketplace: create app record: %v", err)
		http.Error(w, "could not create marketplace record", http.StatusInternalServerError)
		return
	}

	// Create the job — params are persisted so the node can receive them on reconnect.
	j, err := h.jobService.DispatchWithParams(req.NodeID, "marketplace.install", jobs.CommandTypeInstallApp, paramsJSON)
	if err != nil {
		log.Printf("[ERROR] marketplace: dispatch job: %v", err)
		http.Error(w, "could not create job", http.StatusInternalServerError)
		return
	}

	// Link job to marketplace app record.
	if err := h.marketplaceRepo.UpdateJobID(mApp.ID, j.ID); err != nil {
		log.Printf("[WARN] marketplace: update job_id on app %s: %v", mApp.ID, err)
	}

	if err := h.dispatcher.DispatchJobWithParams(
		req.NodeID, j.ID, "marketplace.install",
		jobs.CommandTypeInstallApp, paramsJSON,
	); err != nil {
		log.Printf("[WARN] marketplace: node %s not connected, job %s will be dispatched on reconnect: %v", req.NodeID, j.ID, err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"marketplace_app_id": mApp.ID,
		"job_id":             j.ID,
		"app_name":           req.AppName,
		"status":             mApp.Status,
	})
}

// ListNodeApps returns all marketplace apps deployed on a node.
// GET /v1/marketplace/apps/node/{nodeID}
func (h *MarketplaceHandler) ListNodeApps(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeID")
	apps, err := h.marketplaceRepo.FindByNodeID(nodeID)
	if err != nil {
		log.Printf("[ERROR] marketplace: list node apps: %v", err)
		http.Error(w, "could not list apps", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps)
}
