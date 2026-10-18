// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package enrollment

import "time"

type EnrollToken struct {
	ID         string     `db:"id"`
	Token      string     `db:"token"`
	UserID     string     `db:"user_id"`
	ExpiresAt  time.Time  `db:"expires_at"`
	ConsumedAt *time.Time `db:"consumed_at"`
	CreatedAt  time.Time  `db:"created_at"`
}

type Repository interface {
	CreateEnrollToken(token string, userID string, expiresAt time.Time) error
	MarkTokenAsUsed(token string, consumedAt time.Time) error
	FindEnrollTokenByHash(token string) (*EnrollToken, error)
}
