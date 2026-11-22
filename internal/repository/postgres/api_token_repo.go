// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

type APITokenRepository struct {
	db *sqlx.DB
}

func NewAPITokenRepository(db *sqlx.DB) *APITokenRepository {
	return &APITokenRepository{db: db}
}

const apiTokenColumns = `id, user_id, name, token_hash, token_prefix, scope, expires_at, created_at, last_used_at, revoked_at`

func (r *APITokenRepository) Create(ctx context.Context, t *identity.APIToken) (*identity.APIToken, error) {
	var out identity.APIToken
	err := r.db.QueryRowxContext(ctx, `
		INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, scope, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+apiTokenColumns,
		t.UserID, t.Name, t.TokenHash, t.TokenPrefix, t.Scope, t.ExpiresAt,
	).StructScan(&out)
	if err != nil {
		return nil, fmt.Errorf("create api token: %w", err)
	}
	return &out, nil
}

func (r *APITokenRepository) GetByHash(ctx context.Context, tokenHash string) (*identity.APIToken, error) {
	var out identity.APIToken
	err := r.db.GetContext(ctx, &out, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_hash = $1`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, identity.ErrAPITokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api token: %w", err)
	}
	return &out, nil
}

func (r *APITokenRepository) ListByUser(ctx context.Context, userID string) ([]*identity.APIToken, error) {
	tokens := []*identity.APIToken{}
	err := r.db.SelectContext(ctx, &tokens, `
		SELECT `+apiTokenColumns+` FROM api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return tokens, nil
}

func (r *APITokenRepository) CountActiveByUser(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `
		SELECT COUNT(*) FROM api_tokens
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > NOW()`, userID)
	if err != nil {
		return 0, fmt.Errorf("count api tokens: %w", err)
	}
	return n, nil
}

func (r *APITokenRepository) Revoke(ctx context.Context, userID, tokenID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = NOW()
		WHERE id::text = $1 AND user_id = $2 AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return identity.ErrAPITokenNotFound
	}
	return nil
}

// TouchLastUsed records a use at most once a minute, so busy tokens do not write on every request.
func (r *APITokenRepository) TouchLastUsed(ctx context.Context, tokenID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE api_tokens SET last_used_at = NOW()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < NOW() - INTERVAL '1 minute')`, tokenID)
	return err
}
