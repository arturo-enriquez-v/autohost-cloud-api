// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package externallog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// GenerateToken creates a new token string and persists the hashed token metadata.
func (s *Service) CreateToken(ctx context.Context, userID string, input CreateTokenInput) (*CreateTokenResponse, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("token name is required")
	}

	rawBytes := make([]byte, 24)
	if _, err := rand.Read(rawBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random token: %w", err)
	}

	secret := hex.EncodeToString(rawBytes)
	rawToken := "ahl_sec_" + secret
	prefix := rawToken[:12] + "..."

	hash := HashToken(rawToken)

	token, err := s.repo.CreateToken(ctx, userID, strings.TrimSpace(input.Name), hash, prefix, input.NodeID)
	if err != nil {
		return nil, err
	}

	return &CreateTokenResponse{
		RawToken: rawToken,
		Token:    token,
	}, nil
}

func (s *Service) AuthenticateToken(ctx context.Context, rawToken string) (*ExternalLogToken, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, errors.New("empty token")
	}

	hash := HashToken(rawToken)
	token, err := s.repo.GetTokenByHash(ctx, hash)
	if err != nil {
		return nil, errors.New("invalid or expired token")
	}

	if token.RevokedAt != nil {
		return nil, errors.New("token has been revoked")
	}

	// Async/non-blocking update of last_seen_at
	go func(id string) {
		_ = s.repo.UpdateTokenLastSeen(context.Background(), id)
	}(token.ID)

	return token, nil
}

func (s *Service) ListTokens(ctx context.Context, userID string) ([]*ExternalLogToken, error) {
	return s.repo.ListTokensByUserID(ctx, userID)
}

func (s *Service) RevokeToken(ctx context.Context, userID, tokenID string) error {
	return s.repo.RevokeToken(ctx, userID, tokenID)
}

func (s *Service) IngestLog(ctx context.Context, token *ExternalLogToken, input IngestLogInput) (*ExternalLog, error) {
	if strings.TrimSpace(input.Title) == "" {
		return nil, errors.New("log title is required")
	}

	level := strings.ToLower(strings.TrimSpace(input.Level))
	if level == "" {
		level = "info"
	}
	switch level {
	case "info", "warning", "alerta", "error", "critical", "debug":
		if level == "alerta" {
			level = "warning"
		}
	default:
		level = "info"
	}

	// Target Node ID priority: Payload NodeID > Token NodeID
	var targetNodeID *string
	if input.NodeID != nil && strings.TrimSpace(*input.NodeID) != "" {
		targetNodeID = input.NodeID
	} else if token.NodeID != nil && strings.TrimSpace(*token.NodeID) != "" {
		targetNodeID = token.NodeID
	}

	// Parse tags
	tags := parseTags(input.Tags)

	// Parse metadata
	var metaJSON json.RawMessage
	if input.Metadata != nil {
		b, err := json.Marshal(input.Metadata)
		if err == nil {
			metaJSON = json.RawMessage(b)
		}
	}

	var msg *string
	if strings.TrimSpace(input.Message) != "" {
		m := strings.TrimSpace(input.Message)
		msg = &m
	}

	logEntry := &ExternalLog{
		UserID:    token.UserID,
		NodeID:    targetNodeID,
		TokenID:   &token.ID,
		Title:     strings.TrimSpace(input.Title),
		Level:     level,
		Message:   msg,
		Tags:      tags,
		Metadata:  metaJSON,
		CreatedAt: time.Now().UTC(),
	}

	return s.repo.InsertLog(ctx, logEntry)
}

func (s *Service) ListLogs(ctx context.Context, userID string, filter LogFilter, limit, offset int) ([]*ExternalLog, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListLogsFiltered(ctx, userID, filter, limit, offset)
}

func (s *Service) DeleteLog(ctx context.Context, userID, logID string) error {
	return s.repo.DeleteLog(ctx, userID, logID)
}

func (s *Service) ClearLogs(ctx context.Context, userID string) error {
	return s.repo.ClearLogs(ctx, userID)
}

func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func parseTags(raw interface{}) []string {
	if raw == nil {
		return []string{}
	}

	var result []string
	switch v := raw.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	case string:
		parts := strings.Split(v, ",")
		for _, p := range parts {
			if strings.TrimSpace(p) != "" {
				result = append(result, strings.TrimSpace(p))
			}
		}
	}
	if result == nil {
		result = []string{}
	}
	return result
}
