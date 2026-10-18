// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package enrollment

import (
	"errors"
	"time"
)

var (
	ErrEnrollTokenNotFound    = errors.New("enroll token not found")
	ErrInvalidEnrollTokenData = errors.New("invalid enroll token data")
	ErrUnauthorized           = errors.New("unauthorized")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateEnrollToken(token string, userID string, expiresAt time.Time) error {
	if token == "" || userID == "" {
		return ErrInvalidEnrollTokenData
	}
	return s.repo.CreateEnrollToken(token, userID, expiresAt)
}

func (s *Service) FindEnrollTokenByHash(token string) (*EnrollToken, error) {
	if token == "" {
		return nil, ErrInvalidEnrollTokenData
	}
	return s.repo.FindEnrollTokenByHash(token)
}

func (s *Service) MarkTokenAsUsed(token string, usedAt time.Time) error {
	if token == "" {
		return ErrInvalidEnrollTokenData
	}
	return s.repo.MarkTokenAsUsed(token, usedAt)
}
