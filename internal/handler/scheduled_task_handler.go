package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	scheduledtask "github.com/arturo/autohost-cloud-api/internal/domain/scheduled_task"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ScheduledTaskHandler struct {
	service *scheduledtask.Service
}

func NewScheduledTaskHandler(service *scheduledtask.Service) *ScheduledTaskHandler {
	return &ScheduledTaskHandler{service: service}
}

func (h *ScheduledTaskHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Put("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	r.Patch("/{id}/toggle", h.Toggle)
	return r
}

// List returns all scheduled tasks.
// GET /v1/scheduled-tasks
func (h *ScheduledTaskHandler) List(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.service.List()
	if err != nil {
		log.Printf("[ERROR] list scheduled tasks: %v", err)
		http.Error(w, "could not list tasks", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

type createTaskRequest struct {
	NodeID          string                     `json:"node_id"`
	CommandID       string                     `json:"command_id"`
	CommandName     string                     `json:"command_name"`
	ScheduleType    scheduledtask.ScheduleType `json:"schedule_type"`
	IntervalSeconds *int                       `json:"interval_seconds"`
	RunAtTime       *string                    `json:"run_at_time"`
	Enabled         *bool                      `json:"enabled"`
}

// Create registers a new scheduled task.
// POST /v1/scheduled-tasks
func (h *ScheduledTaskHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" || req.CommandID == "" || req.CommandName == "" || req.ScheduleType == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	task := &scheduledtask.ScheduledTask{
		NodeID:          req.NodeID,
		CommandID:       req.CommandID,
		CommandName:     req.CommandName,
		ScheduleType:    req.ScheduleType,
		IntervalSeconds: req.IntervalSeconds,
		RunAtTime:       req.RunAtTime,
		Enabled:         enabled,
	}

	created, err := h.service.Create(task)
	if err != nil {
		log.Printf("[ERROR] create scheduled task: %v", err)
		http.Error(w, "could not create task", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

type updateTaskRequest struct {
	CommandID       string                     `json:"command_id"`
	CommandName     string                     `json:"command_name"`
	ScheduleType    scheduledtask.ScheduleType `json:"schedule_type"`
	IntervalSeconds *int                       `json:"interval_seconds"`
	RunAtTime       *string                    `json:"run_at_time"`
	Enabled         bool                       `json:"enabled"`
}

// Update modifies an existing scheduled task.
// PUT /v1/scheduled-tasks/{id}
func (h *ScheduledTaskHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.service.FindByID(id)
	if err != nil {
		if errors.Is(err, scheduledtask.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not fetch task", http.StatusInternalServerError)
		return
	}

	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CommandID == "" || req.CommandName == "" || req.ScheduleType == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	existing.CommandID = req.CommandID
	existing.CommandName = req.CommandName
	existing.ScheduleType = req.ScheduleType
	existing.IntervalSeconds = req.IntervalSeconds
	existing.RunAtTime = req.RunAtTime
	existing.Enabled = req.Enabled

	updated, err := h.service.Update(existing)
	if err != nil {
		log.Printf("[ERROR] update scheduled task: %v", err)
		http.Error(w, "could not update task", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// Delete removes a scheduled task.
// DELETE /v1/scheduled-tasks/{id}
func (h *ScheduledTaskHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.service.Delete(id); err != nil {
		if errors.Is(err, scheduledtask.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("[ERROR] delete scheduled task: %v", err)
		http.Error(w, "could not delete task", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Toggle enables or disables a scheduled task.
// PATCH /v1/scheduled-tasks/{id}/toggle
func (h *ScheduledTaskHandler) Toggle(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	task, err := h.service.FindByID(id)
	if err != nil {
		if errors.Is(err, scheduledtask.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not fetch task", http.StatusInternalServerError)
		return
	}

	task.Enabled = !task.Enabled
	updated, err := h.service.Update(task)
	if err != nil {
		log.Printf("[ERROR] toggle scheduled task: %v", err)
		http.Error(w, "could not toggle task", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}
