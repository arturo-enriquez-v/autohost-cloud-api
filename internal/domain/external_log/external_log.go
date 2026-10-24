// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package externallog

import (
	"context"
	"encoding/json"
	"time"
)

type ExternalLogToken struct {
	ID          string     `db:"id" json:"id"`
	UserID      string     `db:"user_id" json:"user_id"`
	Name        string     `db:"name" json:"name"`
	TokenHash   string     `db:"token_hash" json:"-"`
	TokenPrefix string     `db:"token_prefix" json:"token_prefix"`
	NodeID      *string    `db:"node_id" json:"node_id,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	LastSeenAt  *time.Time `db:"last_seen_at" json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
}

type ExternalLog struct {
	ID        string          `db:"id" json:"id"`
	UserID    string          `db:"user_id" json:"user_id"`
	NodeID    *string         `db:"node_id" json:"node_id,omitempty"`
	TokenID   *string         `db:"token_id" json:"token_id,omitempty"`
	Title     string          `db:"title" json:"title"`
	Level     string          `db:"level" json:"level"`
	Message   *string         `db:"message" json:"message,omitempty"`
	Tags      []string        `db:"tags" json:"tags"`
	Metadata  json.RawMessage `db:"metadata" json:"metadata,omitempty"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

type CreateTokenInput struct {
	Name   string  `json:"name"`
	NodeID *string `json:"node_id,omitempty"`
}

type CreateTokenResponse struct {
	RawToken string            `json:"raw_token"`
	Token    *ExternalLogToken `json:"token"`
}

type IngestLogInput struct {
	Title    string      `json:"title"`
	Level    string      `json:"level"`
	Message  string      `json:"message"`
	Tags     interface{} `json:"tags"` // array of strings or string
	NodeID   *string     `json:"node_id,omitempty"`
	Metadata interface{} `json:"metadata,omitempty"`
}

type LogFilter struct {
	NodeID *string
	Level  *string
	Tag    *string
	Search *string
	From   *time.Time
	To     *time.Time
}

type Repository interface {
	CreateToken(ctx context.Context, userID, name, tokenHash, tokenPrefix string, nodeID *string) (*ExternalLogToken, error)
	GetTokenByHash(ctx context.Context, tokenHash string) (*ExternalLogToken, error)
	UpdateTokenLastSeen(ctx context.Context, tokenID string) error
	ListTokensByUserID(ctx context.Context, userID string) ([]*ExternalLogToken, error)
	RevokeToken(ctx context.Context, userID, tokenID string) error

	InsertLog(ctx context.Context, log *ExternalLog) (*ExternalLog, error)
	ListLogsFiltered(ctx context.Context, userID string, filter LogFilter, limit, offset int) ([]*ExternalLog, int, error)
	DeleteLog(ctx context.Context, userID, logID string) error
	ClearLogs(ctx context.Context, userID string) error
}
