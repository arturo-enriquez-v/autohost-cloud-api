// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package nodetoken

import "errors"

var (
	ErrInvalidNodeTokenData = errors.New("invalid node token data")
	ErrNodeTokenNotFound    = errors.New("node token not found")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateNodeToken(nodeID, tokenHash string) error {
	if nodeID == "" || tokenHash == "" {
		return ErrInvalidNodeTokenData
	}
	return s.repo.CreateNodeToken(nodeID, tokenHash)
}

func (s *Service) FindNodeTokenByHash(tokenHash string) (*NodeToken, error) {
	if tokenHash == "" {
		return nil, ErrInvalidNodeTokenData
	}
	return s.repo.FindNodeTokenByHash(tokenHash)
}

func (s *Service) RevokeByNodeID(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeTokenData
	}
	return s.repo.RevokeByNodeID(nodeID)
}
