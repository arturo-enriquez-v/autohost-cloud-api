// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"errors"
	"time"
)

var (
	ErrEnrollTokenNotFound    = errors.New("enroll token not found")
	ErrInvalidEnrollTokenData = errors.New("invalid enroll token data")
)

type EnrollToken struct {
	ID         string     `db:"id"`
	Token      string     `db:"token"`
	UserID     string     `db:"user_id"`
	ExpiresAt  time.Time  `db:"expires_at"`
	ConsumedAt *time.Time `db:"consumed_at"`
	CreatedAt  time.Time  `db:"created_at"`
}

type EnrollmentRepository interface {
	CreateEnrollToken(token string, userID string, expiresAt time.Time) error
	MarkTokenAsUsed(token string, consumedAt time.Time) error
	FindEnrollTokenByHash(token string) (*EnrollToken, error)
}

type EnrollmentService struct {
	repo EnrollmentRepository
}

func NewEnrollmentService(repo EnrollmentRepository) *EnrollmentService {
	return &EnrollmentService{repo: repo}
}

func (s *EnrollmentService) CreateEnrollToken(token string, userID string, expiresAt time.Time) error {
	if token == "" || userID == "" {
		return ErrInvalidEnrollTokenData
	}
	return s.repo.CreateEnrollToken(token, userID, expiresAt)
}

func (s *EnrollmentService) FindEnrollTokenByHash(token string) (*EnrollToken, error) {
	if token == "" {
		return nil, ErrInvalidEnrollTokenData
	}
	return s.repo.FindEnrollTokenByHash(token)
}

func (s *EnrollmentService) MarkTokenAsUsed(token string, usedAt time.Time) error {
	if token == "" {
		return ErrInvalidEnrollTokenData
	}
	return s.repo.MarkTokenAsUsed(token, usedAt)
}
