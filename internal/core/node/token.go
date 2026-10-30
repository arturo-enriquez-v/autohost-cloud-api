// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"errors"
	"time"
)

var (
	ErrInvalidNodeTokenData = errors.New("invalid node token data")
	ErrNodeTokenNotFound    = errors.New("node token not found")
)

type NodeToken struct {
	ID         string     `db:"id"`
	NodeID     string     `db:"node_id"`
	Token      string     `db:"token"`
	CreatedAt  time.Time  `db:"created_at"`
	LastSeenAt *time.Time `db:"last_seen_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
}

type NodeTokenRepository interface {
	CreateNodeToken(nodeID, token string) error
	FindNodeTokenByHash(tokenHash string) (*NodeToken, error)
	UpdateLastSeen(tokenID string, lastSeenAt time.Time) error
	RevokeByNodeID(nodeID string) error
}

type NodeTokenService struct {
	repo NodeTokenRepository
}

func NewNodeTokenService(repo NodeTokenRepository) *NodeTokenService {
	return &NodeTokenService{repo: repo}
}

func (s *NodeTokenService) CreateNodeToken(nodeID, tokenHash string) error {
	if nodeID == "" || tokenHash == "" {
		return ErrInvalidNodeTokenData
	}
	return s.repo.CreateNodeToken(nodeID, tokenHash)
}

func (s *NodeTokenService) FindNodeTokenByHash(tokenHash string) (*NodeToken, error) {
	if tokenHash == "" {
		return nil, ErrInvalidNodeTokenData
	}
	return s.repo.FindNodeTokenByHash(tokenHash)
}

func (s *NodeTokenService) RevokeByNodeID(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeTokenData
	}
	return s.repo.RevokeByNodeID(nodeID)
}
