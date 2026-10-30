// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/jmoiron/sqlx"
)

// TelegramBotRepository implements identity.TelegramBotRepository using PostgreSQL.
type TelegramBotRepository struct {
	db *sqlx.DB
}

func NewTelegramBotRepository(db *sqlx.DB) *TelegramBotRepository {
	return &TelegramBotRepository{db: db}
}

func (r *TelegramBotRepository) Create(bot *identity.TelegramBot) (*identity.TelegramBot, error) {
	var result identity.TelegramBot
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO telegram_bots (user_id, name, bot_token, chat_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, name, bot_token, chat_id, created_at, updated_at`,
		bot.UserID, bot.Name, bot.BotToken, bot.ChatID,
	).Scan(&result.ID, &result.UserID, &result.Name, &result.BotToken, &result.ChatID, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *TelegramBotRepository) Update(bot *identity.TelegramBot) (*identity.TelegramBot, error) {
	var result identity.TelegramBot
	err := r.db.QueryRowContext(context.Background(), `
		UPDATE telegram_bots
		SET name = $1, bot_token = $2, chat_id = $3, updated_at = NOW()
		WHERE id = $4 AND user_id = $5
		RETURNING id, user_id, name, bot_token, chat_id, created_at, updated_at`,
		bot.Name, bot.BotToken, bot.ChatID, bot.ID, bot.UserID,
	).Scan(&result.ID, &result.UserID, &result.Name, &result.BotToken, &result.ChatID, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, identity.ErrTelegramBotNotFound
		}
		return nil, err
	}
	return &result, nil
}

func (r *TelegramBotRepository) Delete(id, userID string) error {
	res, err := r.db.ExecContext(context.Background(), `
		DELETE FROM telegram_bots WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return identity.ErrTelegramBotNotFound
	}
	return nil
}

func (r *TelegramBotRepository) FindByID(id string) (*identity.TelegramBot, error) {
	var bot identity.TelegramBot
	err := r.db.GetContext(context.Background(), &bot, `
		SELECT id, user_id, name, bot_token, chat_id, created_at, updated_at
		FROM telegram_bots
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, identity.ErrTelegramBotNotFound
		}
		return nil, err
	}
	return &bot, nil
}

func (r *TelegramBotRepository) ListByUserID(userID string) ([]*identity.TelegramBot, error) {
	var bots []*identity.TelegramBot
	err := r.db.SelectContext(context.Background(), &bots, `
		SELECT id, user_id, name, bot_token, chat_id, created_at, updated_at
		FROM telegram_bots
		WHERE user_id = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	if bots == nil {
		bots = []*identity.TelegramBot{}
	}
	return bots, nil
}
