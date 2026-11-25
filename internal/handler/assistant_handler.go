// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/assistant"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/arturo/autohost-cloud-api/internal/mcpserver"
	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

// assistantChatLimiter bounds chat turns per user: each one runs a model and API calls.
var assistantChatLimiter = middleware.NewRateLimiter(30, time.Minute)

// AssistantHandler serves /v1/assistant: the built-in chat and its settings.
type AssistantHandler struct {
	svc   *assistant.Service
	rbac  *identity.RBACService
	index *mcpserver.Index
	api   atomic.Pointer[http.Handler]
}

func NewAssistantHandler(svc *assistant.Service) *AssistantHandler {
	return &AssistantHandler{svc: svc}
}

func (h *AssistantHandler) SetRBACService(rbac *identity.RBACService) { h.rbac = rbac }

// SetAPI sets the router tool calls run through (the root router, so every middleware applies).
func (h *AssistantHandler) SetAPI(api http.Handler) {
	if h.svc != nil {
		h.index = h.svc.Index()
	}
	h.api.Store(&api)
}

func (h *AssistantHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/status", h.Status)
	r.Post("/chat", h.Chat)
	r.Get("/settings", h.GetSettings)
	r.Put("/settings", h.SaveSettings)
	r.Get("/models", h.Models)
	return r
}

func (h *AssistantHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !checkAdminAccess(r.Context(), h.rbac, middleware.GetClaims(r.Context()).UserID) {
		response.RespondError(w, http.StatusForbidden, "forbidden: admin only")
		return false
	}
	return true
}

func writeAssistantError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, assistant.ErrInvalid):
		response.RespondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, assistant.ErrNotConfigured):
		response.RespondError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, assistant.ErrProvider):
		response.RespondError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, secretbox.ErrDecrypt):
		log.Printf("[ERROR] assistant: %s: the stored API key does not decrypt (was %s changed?)", op, secretbox.EnvKey)
		response.RespondError(w, http.StatusInternalServerError, "could not decrypt the provider API key")
	default:
		log.Printf("[ERROR] assistant: %s: %v", op, err)
		response.RespondError(w, http.StatusInternalServerError, "could not "+op)
	}
}

// Status tells any user whether the assistant is available and which models they can pick.
func (h *AssistantHandler) Status(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Status(r.Context())
	if err != nil {
		writeAssistantError(w, err, "get assistant status")
		return
	}
	response.RespondJSON(w, http.StatusOK, st)
}

func (h *AssistantHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		writeAssistantError(w, err, "get assistant settings")
		return
	}
	response.RespondJSON(w, http.StatusOK, st)
}

func (h *AssistantHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var in assistant.SettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	st, err := h.svc.SaveSettings(r.Context(), in, middleware.GetClaims(r.Context()).UserID)
	if err != nil {
		writeAssistantError(w, err, "save assistant settings")
		return
	}
	response.RespondJSON(w, http.StatusOK, st)
}

// Models lists the models the configured provider serves (admins, to choose them in the settings).
func (h *AssistantHandler) Models(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	models, err := h.svc.ProviderModels(r.Context())
	if err != nil {
		writeAssistantError(w, err, "list provider models")
		return
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{"models": models})
}

// callerAuthorization is the credential tool calls run with: the request's Authorization header, or
// the session cookie when the browser called the API directly.
func callerAuthorization(r *http.Request) string {
	if a := r.Header.Get("Authorization"); a != "" {
		return a
	}
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return "Bearer " + c.Value
	}
	return ""
}

// Chat runs one chat turn and streams it as server-sent events: reasoning, delta, tool_call,
// tool_result, confirm, done and error.
func (h *AssistantHandler) Chat(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	var req assistant.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	api := h.api.Load()
	if api == nil || h.index == nil {
		response.RespondError(w, http.StatusServiceUnavailable, "the assistant is not ready")
		return
	}
	if ok, retry := assistantChatLimiter.Allow(claims.UserID); !ok {
		middleware.WriteRateLimited(w, retry)
		return
	}
	// Reject a disabled assistant before switching to a stream, so the client gets a plain status.
	if st, err := h.svc.Status(r.Context()); err != nil || !st.Enabled {
		if err == nil {
			err = assistant.ErrNotConfigured
		}
		writeAssistantError(w, err, "start chat")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.RespondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	caller := mcpserver.Caller{Authorization: callerAuthorization(r), RemoteAddr: r.RemoteAddr}
	exec := func(ctx context.Context, operationID string, params map[string]any, body any) (*mcpserver.Result, error) {
		op, found := h.index.Operation(operationID)
		if !found {
			return nil, fmt.Errorf("%w %q: use search_endpoints", mcpserver.ErrUnknownOperation, operationID)
		}
		return mcpserver.Execute(ctx, *api, op, params, body, caller)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)

	var mu sync.Mutex
	emit := func(e assistant.Event) {
		data, err := json.Marshal(e.Data)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
		flusher.Flush()
	}
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Keepalive comments while the model thinks or a tool waits, so proxies (the panel's BFF closes
	// streams idle for 90 s) do not cut the turn.
	stop := make(chan struct{})
	stopped := make(chan struct{})
	defer func() {
		close(stop)
		<-stopped // never write to w after the handler returns
	}()
	go func() {
		defer close(stopped)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-t.C:
				mu.Lock()
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
				mu.Unlock()
			}
		}
	}()

	if err := h.svc.Chat(r.Context(), req, exec, emit); err != nil {
		msg := "the assistant failed"
		switch {
		case errors.Is(err, assistant.ErrInvalid), errors.Is(err, assistant.ErrNotConfigured):
			msg = err.Error()
		case errors.Is(err, assistant.ErrProvider):
			// Details (URLs, provider messages) go to the log; admins see them in the settings.
			log.Printf("[ERROR] assistant: provider: %v", err)
			msg = "the model provider failed; an admin can check the assistant settings"
		case errors.Is(err, context.Canceled):
			return
		default:
			log.Printf("[ERROR] assistant: chat for %s: %v", claims.UserID, err)
		}
		emit(assistant.Event{Type: "error", Data: map[string]string{"error": msg}})
	}
}
