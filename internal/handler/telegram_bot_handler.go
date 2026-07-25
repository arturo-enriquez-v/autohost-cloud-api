package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	telegrambot "github.com/arturo/autohost-cloud-api/internal/domain/telegram_bot"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

type TelegramBotHandler struct {
	service *telegrambot.Service
}

func NewTelegramBotHandler(service *telegrambot.Service) *TelegramBotHandler {
	return &TelegramBotHandler{service: service}
}

func (h *TelegramBotHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Post("/test", h.Test)
	r.Put("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	return r
}

func (h *TelegramBotHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	bots, err := h.service.ListByUserID(claims.UserID)
	if err != nil {
		http.Error(w, "could not fetch telegram bots", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bots)
}

type createBotRequest struct {
	Name     string `json:"name"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

func (h *TelegramBotHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.BotToken == "" || req.ChatID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	bot := &telegrambot.TelegramBot{
		UserID:   claims.UserID,
		Name:     req.Name,
		BotToken: req.BotToken,
		ChatID:   req.ChatID,
	}

	created, err := h.service.Create(bot)
	if err != nil {
		http.Error(w, "could not create telegram bot", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

func (h *TelegramBotHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	botID := chi.URLParam(r, "id")
	if botID == "" {
		http.Error(w, "missing bot id", http.StatusBadRequest)
		return
	}

	var req createBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.BotToken == "" || req.ChatID == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	bot := &telegrambot.TelegramBot{
		ID:       botID,
		UserID:   claims.UserID,
		Name:     req.Name,
		BotToken: req.BotToken,
		ChatID:   req.ChatID,
	}

	updated, err := h.service.Update(bot)
	if err != nil {
		if errors.Is(err, telegrambot.ErrNotFound) {
			http.Error(w, "telegram bot not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not update telegram bot", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (h *TelegramBotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	botID := chi.URLParam(r, "id")
	if botID == "" {
		http.Error(w, "missing bot id", http.StatusBadRequest)
		return
	}

	err := h.service.Delete(botID, claims.UserID)
	if err != nil {
		if errors.Is(err, telegrambot.ErrNotFound) {
			http.Error(w, "telegram bot not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not delete telegram bot", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type testBotRequest struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

func (h *TelegramBotHandler) Test(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req testBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BotToken == "" || req.ChatID == "" {
		http.Error(w, "bot token and chat id are required", http.StatusBadRequest)
		return
	}

	msg := "✅ <b>AutoHost Test Notification</b>\nYour Telegram bot configuration is working correctly!"
	if err := platform.SendTelegramMessage(req.BotToken, req.ChatID, msg); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Test notification sent successfully"})
}
