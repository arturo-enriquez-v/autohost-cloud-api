package handler

import (
	"encoding/json"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/notification"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type NotificationHandler struct {
	repo notification.MarkerRepository
}

func NewNotificationHandler(repo notification.MarkerRepository) *NotificationHandler {
	return &NotificationHandler{repo: repo}
}

func (h *NotificationHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/summary", h.GetSummary)
	r.Post("/ack", h.Acknowledge)
	return r
}

func (h *NotificationHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	summary, err := h.repo.GetSummary(r.Context(), claims.UserID)
	if err != nil {
		http.Error(w, `{"error":"failed to get notifications summary"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

type ackRequest struct {
	Scope string `json:"scope"` // "alerts" | "logs" | "all"
}

func (h *NotificationHandler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req ackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Scope == "" {
		req.Scope = "all"
	}
	if req.Scope != "alerts" && req.Scope != "logs" && req.Scope != "all" {
		http.Error(w, `{"error":"invalid scope: must be 'alerts', 'logs' or 'all'"}`, http.StatusBadRequest)
		return
	}

	if err := h.repo.Acknowledge(r.Context(), claims.UserID, req.Scope); err != nil {
		http.Error(w, `{"error":"failed to acknowledge notifications"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
