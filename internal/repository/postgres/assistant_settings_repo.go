// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/assistant"
)

// AssistantSettingsRepository implements assistant.SettingsRepository.
type AssistantSettingsRepository struct {
	db *sqlx.DB
}

func NewAssistantSettingsRepository(db *sqlx.DB) *AssistantSettingsRepository {
	return &AssistantSettingsRepository{db: db}
}

type assistantSettingsRow struct {
	Enabled          bool      `db:"enabled"`
	BaseURL          string    `db:"base_url"`
	APIKeyCiphertext string    `db:"api_key_ciphertext"`
	DefaultModel     string    `db:"default_model"`
	Models           []byte    `db:"models"`
	ReasoningEffort  string    `db:"reasoning_effort"`
	UpdatedAt        time.Time `db:"updated_at"`
}

const assistantSettingsColumns = `enabled, base_url, api_key_ciphertext, default_model, models, reasoning_effort, updated_at`

func (r assistantSettingsRow) toDomain() *assistant.Settings {
	s := &assistant.Settings{
		Enabled: r.Enabled, BaseURL: r.BaseURL, APIKeyCiphertext: r.APIKeyCiphertext, DefaultModel: r.DefaultModel,
		ReasoningEffort: r.ReasoningEffort, UpdatedAt: r.UpdatedAt, Models: []string{},
	}
	_ = json.Unmarshal(r.Models, &s.Models)
	return s
}

func (r *AssistantSettingsRepository) Get(ctx context.Context) (*assistant.Settings, error) {
	var row assistantSettingsRow
	err := r.db.GetContext(ctx, &row, `SELECT `+assistantSettingsColumns+` FROM assistant_settings WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *AssistantSettingsRepository) Save(ctx context.Context, s *assistant.Settings, updatedBy string) (*assistant.Settings, error) {
	models, err := json.Marshal(s.Models)
	if err != nil {
		return nil, err
	}
	var by any
	if updatedBy != "" {
		by = updatedBy
	}
	var row assistantSettingsRow
	err = r.db.GetContext(ctx, &row, `
		INSERT INTO assistant_settings (id, enabled, base_url, api_key_ciphertext, default_model, models, reasoning_effort, updated_by, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, base_url = EXCLUDED.base_url,
			api_key_ciphertext = EXCLUDED.api_key_ciphertext, default_model = EXCLUDED.default_model,
			models = EXCLUDED.models, reasoning_effort = EXCLUDED.reasoning_effort,
			updated_by = EXCLUDED.updated_by, updated_at = NOW()
		RETURNING `+assistantSettingsColumns,
		s.Enabled, s.BaseURL, s.APIKeyCiphertext, s.DefaultModel, models, s.ReasoningEffort, by)
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}
