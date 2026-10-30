// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
)

type AuthHandler struct {
	service         *identity.AuthService
	notificationSvc *identity.NotificationService
}

func NewAuthHandler(service *identity.AuthService, notificationSvc *identity.NotificationService) *AuthHandler {
	return &AuthHandler{
		service:         service,
		notificationSvc: notificationSvc,
	}
}

func (h *AuthHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/register", h.Register)
	r.Post("/login", h.Login)
	r.Post("/refresh", h.Refresh)
	r.Post("/logout", h.Logout)
	r.Post("/oauth/google", h.OAuthGoogle)
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.Auth)
		pr.Get("/me", h.Me)
		pr.Put("/profile", h.UpdateProfile)
	})
	return r
}

type creds struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var in creds
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if in.Email == "" || in.Password == "" {
		http.Error(w, "email and password required", http.StatusBadRequest)
		return
	}

	pair, err := h.service.RegisterAndIssue(in.Email, in.Name, in.Password, r.UserAgent(), clientIP(r))
	if errors.Is(err, identity.ErrUserAlreadyExists) {
		http.Error(w, "email already exists", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.notificationSvc != nil {
		h.notificationSvc.PublishUserRegistered(in.Email, in.Name, "")
	}

	response.RespondJSON(w, http.StatusCreated, pair)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var in creds
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	pair, err := h.service.LoginAndIssue(in.Email, in.Password, r.UserAgent(), clientIP(r))
	if errors.Is(err, identity.ErrInvalidCredentials) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response.RespondJSON(w, http.StatusOK, pair)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshToken == "" {
		http.Error(w, "no refresh", http.StatusUnauthorized)
		return
	}

	pair, err := h.service.RefreshTokens(body.RefreshToken, r.UserAgent(), clientIP(r))
	if errors.Is(err, identity.ErrInvalidRefreshToken) {
		http.Error(w, "invalid or expired refresh token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response.RespondJSON(w, http.StatusOK, pair)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.RefreshToken != "" {
		h.service.RevokeToken(body.RefreshToken)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := h.service.GetByID(claims.UserID)
	if err != nil || user == nil {
		response.RespondJSON(w, http.StatusOK, map[string]any{
			"uid":        claims.UserID,
			"email":      claims.Email,
			"name":       claims.Name,
			"avatar_url": "",
		})
		return
	}

	name := ""
	if user.Name != nil {
		name = *user.Name
	}
	avatarURL := ""
	if user.AvatarURL != nil {
		avatarURL = *user.AvatarURL
	}

	response.RespondJSON(w, http.StatusOK, map[string]any{
		"uid":        user.ID,
		"email":      user.Email,
		"name":       name,
		"avatar_url": avatarURL,
	})
}

type updateProfileReq struct {
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req updateProfileReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	user, err := h.service.UpdateProfile(claims.UserID, req.Name, req.AvatarURL)
	if err != nil || user == nil {
		http.Error(w, "could not update profile", http.StatusInternalServerError)
		return
	}

	name := ""
	if user.Name != nil {
		name = *user.Name
	}
	avatarURL := ""
	if user.AvatarURL != nil {
		avatarURL = *user.AvatarURL
	}

	response.RespondJSON(w, http.StatusOK, map[string]any{
		"uid":        user.ID,
		"email":      user.Email,
		"name":       name,
		"avatar_url": avatarURL,
	})
}

func (h *AuthHandler) OAuthGoogle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IDToken == "" {
		http.Error(w, "id_token required", http.StatusBadRequest)
		return
	}

	pair, err := h.service.OAuthLoginAndIssue(body.IDToken, r.UserAgent(), clientIP(r))
	if err != nil {
		log.Printf("[AUTH] OAuthGoogle error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response.RespondJSON(w, http.StatusOK, pair)
}

// clientIP returns the remote IP address of the request.
// chi's middleware.RealIP (registered globally in the router) has already
// rewritten r.RemoteAddr to the value of X-Real-IP / X-Forwarded-For coming
// from the trusted reverse-proxy, so we only read r.RemoteAddr here to avoid
// letting arbitrary clients spoof their IP via raw headers.
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
