package handler

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

type AuthHandler struct {
	service *auth.Service
	repo    auth.Repository
}

func NewAuthHandler(service *auth.Service, repo auth.Repository) *AuthHandler {
	return &AuthHandler{
		service: service,
		repo:    repo,
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

	// Usar el servicio para registrar
	userID, err := h.service.Register(in.Email, in.Name, in.Password)
	if err == auth.ErrUserAlreadyExists {
		http.Error(w, "email already exists", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Generar tokens
	platform.SignAccessToken(userID, in.Email)
	_, rtHash := platform.MakeRefreshPair()

	_ = h.repo.StoreRefreshToken(userID, rtHash, r.UserAgent(), clientIP(r))

	w.WriteHeader(http.StatusCreated)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var in creds
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	user, err := h.service.Login(in.Email, in.Password)
	if err == auth.ErrInvalidCredentials {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Generar tokens
	access, _ := platform.SignAccessToken(user.ID, user.Email)
	rt, rtHash := platform.MakeRefreshPair()
	_ = h.repo.StoreRefreshToken(user.ID, rtHash, r.UserAgent(), clientIP(r))

	// BFF pattern: devolver tokens como JSON — Next.js es el único que setea cookies
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"refresh_token": rt,
	})
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	// BFF pattern: el refresh_token llega en el body JSON, no en cookie
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshToken == "" {
		http.Error(w, "no refresh", http.StatusUnauthorized)
		return
	}

	oldHash := platform.HashRefreshToken(body.RefreshToken)

	// Buscar el userID asociado al refresh token en BD
	userID, err := h.repo.FindRefreshToken(oldHash)
	if err != nil {
		http.Error(w, "invalid or expired refresh token", http.StatusUnauthorized)
		return
	}

	// Buscar datos del usuario para incluirlos en el nuevo access token
	user, err := h.repo.FindUserByID(userID)
	if err != nil || user == nil {
		http.Error(w, "user not found", http.StatusUnauthorized)
		return
	}

	// Revocar el token viejo (rotación)
	_ = h.repo.RevokeRefreshToken(userID, oldHash)

	// Emitir nuevos tokens
	access, _ := platform.SignAccessToken(user.ID, user.Email)
	rt, rtHash := platform.MakeRefreshPair()
	_ = h.repo.StoreRefreshToken(user.ID, rtHash, r.UserAgent(), clientIP(r))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"refresh_token": rt,
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// BFF pattern: el refresh_token llega en el body JSON
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.RefreshToken != "" {
		if userID, _, err := platform.ParseRefreshToken(body.RefreshToken); err == nil {
			_ = h.repo.RevokeRefreshToken(userID, platform.HashRefreshToken(body.RefreshToken))
		}
	}
	// Next.js es el responsable de borrar las cookies — Go solo responde 204
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"user_id": claims.UserID,
		"email":   claims.Email,
	})
}

func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return ip
}

func (h *AuthHandler) OAuthGoogle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IDToken == "" {
		http.Error(w, "id_token required", http.StatusBadRequest)
		return
	}

	claims, err := platform.VerifyGoogleIDToken(body.IDToken)
	if err != nil {
		http.Error(w, "invalid google token", http.StatusUnauthorized)
		return
	}

	user, err := h.service.LoginOAuth(claims.Email, claims.Name, "google", claims.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	access, _ := platform.SignAccessToken(user.ID, user.Email)
	rt, rtHash := platform.MakeRefreshPair()
	_ = h.repo.StoreRefreshToken(user.ID, rtHash, r.UserAgent(), clientIP(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"refresh_token": rt,
	})
}
