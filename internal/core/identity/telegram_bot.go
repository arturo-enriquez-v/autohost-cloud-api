// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"errors"
	"time"
)

var (
	ErrTelegramBotNotFound = errors.New("telegram bot not found")
	ErrNotFound            = ErrTelegramBotNotFound
)

// TelegramBot stores a saved Telegram Bot configuration for sending notifications.
type TelegramBot struct {
	ID        string    `db:"id"         json:"id"`
	UserID    string    `db:"user_id"    json:"user_id"`
	Name      string    `db:"name"       json:"name"`
	BotToken  string    `db:"bot_token"  json:"bot_token"`
	ChatID    string    `db:"chat_id"    json:"chat_id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// TelegramBotRepository defines persistence operations for TelegramBot.
type TelegramBotRepository interface {
	Create(bot *TelegramBot) (*TelegramBot, error)
	Update(bot *TelegramBot) (*TelegramBot, error)
	Delete(id, userID string) error
	FindByID(id string) (*TelegramBot, error)
	ListByUserID(userID string) ([]*TelegramBot, error)
}

// TelegramBotService handles business logic for TelegramBot configs.
type TelegramBotService struct {
	repo TelegramBotRepository
}

func NewTelegramBotService(repo TelegramBotRepository) *TelegramBotService {
	return &TelegramBotService{repo: repo}
}

func (s *TelegramBotService) Create(bot *TelegramBot) (*TelegramBot, error) {
	return s.repo.Create(bot)
}

func (s *TelegramBotService) Update(bot *TelegramBot) (*TelegramBot, error) {
	return s.repo.Update(bot)
}

func (s *TelegramBotService) Delete(id, userID string) error {
	return s.repo.Delete(id, userID)
}

func (s *TelegramBotService) FindByID(id string) (*TelegramBot, error) {
	return s.repo.FindByID(id)
}

func (s *TelegramBotService) ListByUserID(userID string) ([]*TelegramBot, error) {
	return s.repo.ListByUserID(userID)
}
