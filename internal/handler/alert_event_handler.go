package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

// AlertEventHandler serves the persisted alert history.
type AlertEventHandler struct {
	service *alertevent.Service
}

func NewAlertEventHandler(service *alertevent.Service) *AlertEventHandler {
	return &AlertEventHandler{service: service}
}

func (h *AlertEventHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.List)
	return r
}

// List returns alert events for the authenticated user.
// GET /v1/alert-events?limit=50&offset=0
func (h *AlertEventHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	events, err := h.service.ListByUserID(claims.UserID, limit, offset)
	if err != nil {
		http.Error(w, "could not fetch alert events", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}
