package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/plan"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

// PlanHandler exposes plan and subscription endpoints.
type PlanHandler struct {
	service *plan.Service
}

func NewPlanHandler(service *plan.Service) *PlanHandler {
	return &PlanHandler{service: service}
}

func (h *PlanHandler) Routes() chi.Router {
	r := chi.NewRouter()

	// Public: list available plans
	r.Get("/", h.ListPlans)

	// Protected: user's own subscription
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth)
		r.Get("/my-plan", h.GetMyPlan)
		r.Put("/my-plan", h.ChangePlan)
		r.Get("/usage", h.GetUsage)
	})

	return r
}

// ListPlans godoc
// GET /v1/plans
// Returns all active plans ordered by price.
func (h *PlanHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.service.ListPlans()
	if err != nil {
		response.RespondError(w, http.StatusInternalServerError, "failed to retrieve plans")
		return
	}
	response.RespondJSON(w, http.StatusOK, plans)
}

// GetMyPlan godoc
// GET /v1/plans/my-plan
// Returns the authenticated user's current subscription and plan details.
func (h *PlanHandler) GetMyPlan(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	up, err := h.service.GetUserPlan(userID)
	if err != nil {
		response.RespondError(w, http.StatusInternalServerError, "failed to retrieve plan")
		return
	}
	response.RespondJSON(w, http.StatusOK, up)
}

type changePlanRequest struct {
	PlanID string `json:"plan_id"`
}

// ChangePlan godoc
// PUT /v1/plans/my-plan
// Switches the authenticated user to a different plan.
func (h *PlanHandler) ChangePlan(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	var req changePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PlanID == "" {
		response.RespondError(w, http.StatusBadRequest, "plan_id is required")
		return
	}

	up, err := h.service.ChangePlan(userID, req.PlanID)
	if err != nil {
		switch {
		case errors.Is(err, plan.ErrPlanNotFound):
			response.RespondError(w, http.StatusNotFound, "plan not found")
		case errors.Is(err, plan.ErrSubscriptionNotFound):
			response.RespondError(w, http.StatusNotFound, "subscription not found")
		default:
			response.RespondError(w, http.StatusInternalServerError, "failed to change plan")
		}
		return
	}
	response.RespondJSON(w, http.StatusOK, up)
}

// GetUsage godoc
// GET /v1/plans/usage
// Returns the authenticated user's active plan limits and current utilization figures.
func (h *PlanHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	usage, err := h.service.GetPlanUsage(userID)
	if err != nil {
		response.RespondError(w, http.StatusInternalServerError, "failed to retrieve plan usage")
		return
	}
	response.RespondJSON(w, http.StatusOK, usage)
}

