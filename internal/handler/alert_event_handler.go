// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

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

type listAlertsResponse struct {
	Events []*alertevent.AlertEvent `json:"events"`
	Total  int                      `json:"total"`
}

// List returns alert events for the authenticated user.
// GET /v1/alert-events?limit=20&offset=0&node_id=...&from=2024-01-01&to=2024-01-31
func (h *AlertEventHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	limit := 20
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

	var filter alertevent.AlertFilter
	if v := r.URL.Query().Get("node_id"); v != "" {
		filter.NodeID = v
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from := t.UTC()
			filter.From = &from
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			// Include the full last day.
			to := t.UTC().Add(24*time.Hour - time.Second)
			filter.To = &to
		}
	}

	events, total, err := h.service.ListFiltered(claims.UserID, filter, limit, offset)
	if err != nil {
		http.Error(w, "could not fetch alert events", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listAlertsResponse{Events: events, Total: total})
}
