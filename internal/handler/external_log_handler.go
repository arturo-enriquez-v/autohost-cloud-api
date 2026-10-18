// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	externallog "github.com/arturo/autohost-cloud-api/internal/domain/external_log"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
)

type ExternalLogHandler struct {
	service *externallog.Service
}

func NewExternalLogHandler(service *externallog.Service) *ExternalLogHandler {
	return &ExternalLogHandler{service: service}
}

func (h *ExternalLogHandler) IngestRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.ExternalLogAuth(h.service))
	r.Post("/", h.IngestLog)
	r.Post("/ingest", h.IngestLog)
	return r
}

func (h *ExternalLogHandler) UserLogRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.ListLogs)
	r.Delete("/", h.ClearLogs)
	r.Delete("/{id}", h.DeleteLog)
	return r
}

func (h *ExternalLogHandler) UserTokenRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.ListTokens)
	r.Post("/", h.CreateToken)
	r.Delete("/{id}", h.RevokeToken)
	return r
}

// IngestLog receives a log entry from an external application or third party using an ingestion token.
func (h *ExternalLogHandler) IngestLog(w http.ResponseWriter, r *http.Request) {
	token := middleware.GetExternalLogToken(r.Context())
	if token == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var input externallog.IngestLogInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}

	logEntry, err := h.service.IngestLog(r.Context(), token, input)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"log":     logEntry,
	})
}

// ListLogs returns filtered external logs for the authenticated user.
func (h *ExternalLogHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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

	var filter externallog.LogFilter
	if v := r.URL.Query().Get("node_id"); v != "" {
		filter.NodeID = &v
	}
	if v := r.URL.Query().Get("level"); v != "" {
		filter.Level = &v
	}
	if v := r.URL.Query().Get("tag"); v != "" {
		filter.Tag = &v
	}
	if v := r.URL.Query().Get("search"); v != "" {
		filter.Search = &v
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from := t.UTC()
			filter.From = &from
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to := t.UTC().Add(24*time.Hour - time.Second)
			filter.To = &to
		}
	}

	logs, total, err := h.service.ListLogs(r.Context(), claims.UserID, filter, limit, offset)
	if err != nil {
		http.Error(w, `{"error":"could not fetch external logs"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"logs":  logs,
		"total": total,
	})
}

// DeleteLog deletes a single log entry.
func (h *ExternalLogHandler) DeleteLog(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	logID := chi.URLParam(r, "id")
	if logID == "" {
		http.Error(w, `{"error":"missing log id"}`, http.StatusBadRequest)
		return
	}

	if err := h.service.DeleteLog(r.Context(), claims.UserID, logID); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// ClearLogs deletes all external logs for the user.
func (h *ExternalLogHandler) ClearLogs(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	if err := h.service.ClearLogs(r.Context(), claims.UserID); err != nil {
		http.Error(w, `{"error":"could not clear logs"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// ListTokens returns all external log tokens for the user.
func (h *ExternalLogHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	tokens, err := h.service.ListTokens(r.Context(), claims.UserID)
	if err != nil {
		http.Error(w, `{"error":"could not fetch tokens"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tokens": tokens,
	})
}

// CreateToken generates a new external log token.
func (h *ExternalLogHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var input externallog.CreateTokenInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}

	res, err := h.service.CreateToken(r.Context(), claims.UserID, input)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(res)
}

// RevokeToken revokes an external log token.
func (h *ExternalLogHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	tokenID := chi.URLParam(r, "id")
	if tokenID == "" {
		http.Error(w, `{"error":"missing token id"}`, http.StatusBadRequest)
		return
	}

	if err := h.service.RevokeToken(r.Context(), claims.UserID, tokenID); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
