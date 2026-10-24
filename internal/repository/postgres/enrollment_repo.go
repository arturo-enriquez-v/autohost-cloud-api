// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	"github.com/jmoiron/sqlx"
)

type EnrollTokenRepo struct{ DB *sqlx.DB }

func NewEnrollmentRepository(db *sqlx.DB) *EnrollTokenRepo { return &EnrollTokenRepo{DB: db} }

func (r *EnrollTokenRepo) CreateEnrollToken(token string, userID string, expiresAt time.Time) error {
	_, err := r.DB.Exec(`
		INSERT INTO enroll_tokens (token, user_id, expires_at)
		VALUES ($1, $2, $3)
	`, token, userID, expiresAt)
	if err != nil {
		return err
	}
	return nil
}

func (r *EnrollTokenRepo) FindEnrollTokenByHash(token string) (*enrollment.EnrollToken, error) {
	var model enrollment.EnrollToken
	err := r.DB.Get(&model, `
		SELECT id, token, user_id, expires_at, consumed_at, created_at
		FROM enroll_tokens
		WHERE token = $1
	`, token)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func (r *EnrollTokenRepo) MarkTokenAsUsed(token string, usedAt time.Time) error {
	_, err := r.DB.Exec(`
		UPDATE enroll_tokens
		SET consumed_at = $1
		WHERE token = $2
	`, usedAt, token)
	if err != nil {
		return err
	}
	return nil
}
