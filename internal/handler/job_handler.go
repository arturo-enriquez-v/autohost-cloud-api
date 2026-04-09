package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

// NodeDispatcher abstracts sending an execute_job request to a connected node,
// regardless of the underlying transport (WebSocket or gRPC).
type NodeDispatcher interface {
	DispatchJob(nodeID, jobID, commandName string, commandType nodecommand.CommandType) error
}

// JobHandler handles job dispatch and status queries.
type JobHandler struct {
	jobService *job.Service
	dispatcher NodeDispatcher
}

func NewJobHandler(jobService *job.Service, dispatcher NodeDispatcher) *JobHandler {
	return &JobHandler{
		jobService: jobService,
		dispatcher: dispatcher,
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
	NodeID      string                  `json:"node_id"`
	CommandName string                  `json:"command_name"`
	CommandType nodecommand.CommandType `json:"command_type"` // "default" | "custom"
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

	j, err := h.jobService.Dispatch(req.NodeID, req.CommandName, req.CommandType)
	if err != nil {
		log.Printf("[ERROR] dispatch job: %v", err)
		http.Error(w, "could not create job", http.StatusInternalServerError)
		return
	}

	if err := h.dispatcher.DispatchJob(req.NodeID, j.ID, j.CommandName, j.CommandType); err != nil {
		log.Printf("[WARN] node %s not connected, job %s stays pending: %v", req.NodeID, j.ID, err)
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
		if err == job.ErrJobNotFound {
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
