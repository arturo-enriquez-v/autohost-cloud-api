// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package nodemetric

import (
	"errors"
	"time"
)

var (
	ErrInvalidNodeMetricData = errors.New("invalid node metric data")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) StoreNodeMetric(req *CreateNodeMetricRequest) (*NodeMetric, error) {
	if req == nil || req.NodeID == "" {
		return nil, ErrInvalidNodeMetricData
	}
	return s.repo.StoreNodeMetric(req)
}

func (s *Service) GetHistory(params HistoryParams) ([]*NodeMetricHistoryPoint, error) {
	if params.NodeID == "" {
		return nil, ErrInvalidNodeMetricData
	}
	return s.repo.GetHistoryByNodeID(params)
}

// GetNodePeriodStats returns per-node AVG/MAX metrics for all nodes of an owner
// over the window [since, now].
func (s *Service) GetNodePeriodStats(ownerID string, since time.Time) ([]*NodePeriodStats, error) {
	return s.repo.GetNodePeriodStatsByOwnerID(ownerID, since)
}

// func (s *Service) GetMetricsByNodeID(nodeID string, limit int) ([]*NodeMetric, error) {
// 	if nodeID == "" || limit <= 0 {
// 		return nil, ErrInvalidNodeMetricData
// 	}
// 	return s.repo.GetMetricsByNodeID(nodeID, limit)
// }

// func (s *Service) GetLatestMetricByNodeID(nodeID string) (*NodeMetric, error) {
// 	if nodeID == "" {
// 		return nil, ErrInvalidNodeMetricData
// 	}
// 	return s.repo.GetLatestMetricByNodeID(nodeID)
// }
