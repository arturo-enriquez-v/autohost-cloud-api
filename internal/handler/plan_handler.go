// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

// PlanHandler exposes resource usage endpoints.
type PlanHandler struct {
	service *identity.PlanService
}

func NewPlanHandler(service *identity.PlanService) *PlanHandler {
	return &PlanHandler{service: service}
}

func (h *PlanHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth)
		r.Get("/", h.GetUsage)
		r.Get("/usage", h.GetUsage)
	})

	return r
}

// GetUsage godoc
// GET /v1/usage (and GET /v1/plans/usage)
// Returns the authenticated user's current resource utilization figures.
func (h *PlanHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	usage, err := h.service.GetUsage(userID)
	if err != nil {
		response.RespondError(w, http.StatusInternalServerError, "failed to retrieve usage")
		return
	}
	response.RespondJSON(w, http.StatusOK, usage)
}
