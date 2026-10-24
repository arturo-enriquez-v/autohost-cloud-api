// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package job

import nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Dispatch creates a new pending job and returns it so the caller can send it
// via WebSocket.
func (s *Service) Dispatch(nodeID, commandName string, commandType nodecommand.CommandType) (*Job, error) {
	return s.DispatchWithParams(nodeID, commandName, commandType, "")
}

// DispatchWithParams creates a new pending job with an optional JSON-encoded
// params string that is persisted in the DB and re-sent on node reconnect.
func (s *Service) DispatchWithParams(nodeID, commandName string, commandType nodecommand.CommandType, params string) (*Job, error) {
	if nodeID == "" || commandName == "" {
		return nil, ErrInvalidJobData
	}
	if commandType == "" {
		commandType = nodecommand.CommandTypeDefault
	}
	j := &Job{
		NodeID:      nodeID,
		CommandName: commandName,
		CommandType: commandType,
		Params:      params,
		Status:      StatusPending,
	}
	return s.repo.Create(j)
}

// FindPendingByNodeID returns pending jobs of the given command type for re-dispatch.
func (s *Service) FindPendingByNodeID(nodeID string, commandType nodecommand.CommandType) ([]*Job, error) {
	return s.repo.FindPendingByNodeID(nodeID, commandType)
}

// GetByID returns a job by its ID.
func (s *Service) GetByID(id string) (*Job, error) {
	return s.repo.FindByID(id)
}

// ListByNode returns all jobs for a given node.
func (s *Service) ListByNode(nodeID string) ([]*Job, error) {
	return s.repo.FindByNodeID(nodeID)
}

// UpdateResult is called when the node reports back the execution result.
func (s *Service) UpdateResult(id string, status JobStatus, output, errMsg string) error {
	return s.repo.UpdateStatus(id, status, output, errMsg)
}
