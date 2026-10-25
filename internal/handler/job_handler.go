// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

// JobHandler handles job dispatch and status queries.
type JobHandler struct {
	jobService          *jobs.JobService
	dispatcher          dispatcher.NodeDispatcher
	containerSettingSvc *node.ContainerSettingService
}

func NewJobHandler(
	jobService *jobs.JobService,
	disp dispatcher.NodeDispatcher,
	containerSettingSvc *node.ContainerSettingService,
) *JobHandler {
	return &JobHandler{
		jobService:          jobService,
		dispatcher:          disp,
		containerSettingSvc: containerSettingSvc,
	}
}

func (h *JobHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Post("/", h.Dispatch)
	r.Get("/{id}", h.GetJob)
	r.Get("/node/{nodeID}", h.ListByNode)
	return r
}

// dispatchJobRequest is the payload from the dashboard.
type dispatchJobRequest struct {
	NodeID      string           `json:"node_id"`
	CommandName string           `json:"command_name"`
	CommandType jobs.CommandType `json:"command_type"` // "default" | "custom"
	Params      string           `json:"params,omitempty"`
}

// Dispatch creates a pending job and sends an execute_job message to the node.
// POST /v1/jobs
func (h *JobHandler) Dispatch(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req dispatchJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" || req.CommandName == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate whether container action is locked
	if strings.HasPrefix(req.CommandName, "container.") {
		parts := strings.Split(req.CommandName, ".")
		if len(parts) >= 3 && (parts[1] == "start" || parts[1] == "stop" || parts[1] == "restart") {
			containerName := strings.Join(parts[2:], ".")
			if h.containerSettingSvc != nil && h.containerSettingSvc.IsContainerLocked(r.Context(), req.NodeID, containerName) {
				http.Error(w, fmt.Sprintf("action blocked: container '%s' is locked", containerName), http.StatusForbidden)
				return
			}
		}
	}

	var j *jobs.Job
	var err error
	if req.Params != "" {
		j, err = h.jobService.DispatchWithParams(req.NodeID, req.CommandName, req.CommandType, req.Params)
	} else {
		j, err = h.jobService.Dispatch(req.NodeID, req.CommandName, req.CommandType)
	}
	if err != nil {
		log.Printf("[ERROR] dispatch job: %v", err)
		http.Error(w, "could not create job", http.StatusInternalServerError)
		return
	}

	var dispatchErr error
	if pd, ok := h.dispatcher.(dispatcher.ParamDispatcher); ok && req.Params != "" {
		dispatchErr = pd.DispatchJobWithParams(req.NodeID, j.ID, j.CommandName, j.CommandType, req.Params)
	} else {
		dispatchErr = h.dispatcher.DispatchJob(req.NodeID, j.ID, j.CommandName, j.CommandType)
	}

	if dispatchErr != nil {
		log.Printf("[WARN] node %s not connected, job %s stays pending: %v", req.NodeID, j.ID, dispatchErr)
		// Job stays pending – future: queue and push on reconnect.
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(j)
}

// GetJob returns the current status of a job.
// GET /v1/jobs/{id}
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	j, err := h.jobService.GetByID(id)
	if err != nil {
		if err == jobs.ErrJobNotFound {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not get job", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(j)
}

// ListByNode lists all jobs for a node.
// GET /v1/jobs/node/{nodeID}
func (h *JobHandler) ListByNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeID")
	jobs, err := h.jobService.ListByNode(nodeID)
	if err != nil {
		log.Printf("[ERROR] list jobs: %v", err)
		http.Error(w, "could not list jobs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}
