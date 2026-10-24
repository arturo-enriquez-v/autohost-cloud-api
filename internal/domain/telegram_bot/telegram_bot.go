// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package telegrambot

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("telegram bot not found")
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

// Repository defines persistence operations for TelegramBot.
type Repository interface {
	Create(bot *TelegramBot) (*TelegramBot, error)
	Update(bot *TelegramBot) (*TelegramBot, error)
	Delete(id, userID string) error
	FindByID(id string) (*TelegramBot, error)
	ListByUserID(userID string) ([]*TelegramBot, error)
}

// Service handles business logic for TelegramBot configs.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(bot *TelegramBot) (*TelegramBot, error) {
	return s.repo.Create(bot)
}

func (s *Service) Update(bot *TelegramBot) (*TelegramBot, error) {
	return s.repo.Update(bot)
}

func (s *Service) Delete(id, userID string) error {
	return s.repo.Delete(id, userID)
}

func (s *Service) FindByID(id string) (*TelegramBot, error) {
	return s.repo.FindByID(id)
}

func (s *Service) ListByUserID(userID string) ([]*TelegramBot, error) {
	return s.repo.ListByUserID(userID)
}
