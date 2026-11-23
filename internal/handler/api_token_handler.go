// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
)

// apiTokenService is the part of identity.APITokenService the handler uses.
type apiTokenService interface {
	Create(ctx context.Context, userID string, in identity.CreateAPITokenInput) (*identity.CreatedAPIToken, error)
	List(ctx context.Context, userID string) ([]*identity.APIToken, error)
	Revoke(ctx context.Context, userID, tokenID string) error
}

// APITokenHandler manages the caller's personal API tokens. Every route requires a session login: an API
// token cannot list, create or revoke tokens.
type APITokenHandler struct {
	svc apiTokenService
}

func NewAPITokenHandler(svc *identity.APITokenService) *APITokenHandler {
	return &APITokenHandler{svc: svc}
}

func (h *APITokenHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth, middleware.RequireSession)
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Delete("/{id}", h.Revoke)
	return r
}

func (h *APITokenHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	tokens, err := h.svc.List(r.Context(), userID)
	if err != nil {
		log.Printf("[ERROR] list api tokens for user %s: %v", userID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not list api tokens")
		return
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{"api_tokens": tokens})
}

func (h *APITokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	var in identity.CreateAPITokenInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	created, err := h.svc.Create(r.Context(), userID, in)
	switch {
	case errors.Is(err, identity.ErrAPITokenNameRequired),
		errors.Is(err, identity.ErrAPITokenNameTooLong),
		errors.Is(err, identity.ErrAPITokenInvalidScope),
		errors.Is(err, identity.ErrAPITokenInvalidTTL),
		errors.Is(err, identity.ErrAPITokenPermissionsRequired),
		errors.Is(err, identity.ErrAPITokenInvalidPermission):
		response.RespondError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, identity.ErrAPITokenLimitReached):
		response.RespondError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		log.Printf("[ERROR] create api token for user %s: %v", userID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not create api token")
		return
	}
	response.RespondJSON(w, http.StatusCreated, created)
}

func (h *APITokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	err := h.svc.Revoke(r.Context(), userID, chi.URLParam(r, "id"))
	if errors.Is(err, identity.ErrAPITokenNotFound) {
		response.RespondError(w, http.StatusNotFound, "api token not found")
		return
	}
	if err != nil {
		log.Printf("[ERROR] revoke api token for user %s: %v", userID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not revoke api token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
