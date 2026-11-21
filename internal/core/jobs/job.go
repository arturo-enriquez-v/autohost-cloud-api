// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package jobs

import (
	"errors"
	"time"
)

// JobStatus represents the lifecycle of a dispatched job.
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

var (
	ErrJobNotFound    = errors.New("job not found")
	ErrInvalidJobData = errors.New("invalid job data")
)

// Job represents a single execution request sent to a node.
type Job struct {
	ID          string      `db:"id" json:"id"`
	NodeID      string      `db:"node_id" json:"node_id"`
	CommandName string      `db:"command_name" json:"command_name"`
	CommandType CommandType `db:"command_type" json:"command_type"`
	Status      JobStatus   `db:"status" json:"status"`
	// Params is the JSON-encoded input of parameterized commands. It is never serialized to API clients:
	// internal jobs carry secrets in it (e.g. vm.create's enrollment token, swarm join tokens).
	Params     string     `db:"params" json:"-"`
	Output     string     `db:"output" json:"output"`
	Error      string     `db:"error" json:"error"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	StartedAt  *time.Time `db:"started_at" json:"started_at"`
	FinishedAt *time.Time `db:"finished_at" json:"finished_at"`
}

// JobRepository defines the persistence contract for jobs.
type JobRepository interface {
	Create(j *Job) (*Job, error)
	FindByID(id string) (*Job, error)
	FindByNodeID(nodeID string) ([]*Job, error)
	FindPendingByNodeID(nodeID string, commandType CommandType) ([]*Job, error)
	UpdateStatus(id string, status JobStatus, output, errMsg string) error
}

// JobService manages the lifecycle and dispatch of jobs.
type JobService struct {
	repo JobRepository
}

func NewJobService(repo JobRepository) *JobService {
	return &JobService{repo: repo}
}

// Dispatch creates a new pending job and returns it so the caller can send it
// via WebSocket or gRPC.
func (s *JobService) Dispatch(nodeID, commandName string, commandType CommandType) (*Job, error) {
	return s.DispatchWithParams(nodeID, commandName, commandType, "")
}

// DispatchWithParams creates a new pending job with an optional JSON-encoded
// params string that is persisted in the DB and re-sent on node reconnect.
func (s *JobService) DispatchWithParams(nodeID, commandName string, commandType CommandType, params string) (*Job, error) {
	if nodeID == "" || commandName == "" {
		return nil, ErrInvalidJobData
	}
	if commandType == "" {
		commandType = CommandTypeDefault
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
func (s *JobService) FindPendingByNodeID(nodeID string, commandType CommandType) ([]*Job, error) {
	return s.repo.FindPendingByNodeID(nodeID, commandType)
}

// GetByID returns a job by its ID.
func (s *JobService) GetByID(id string) (*Job, error) {
	return s.repo.FindByID(id)
}

// ListByNode returns all jobs for a given node.
func (s *JobService) ListByNode(nodeID string) ([]*Job, error) {
	return s.repo.FindByNodeID(nodeID)
}

// UpdateResult is called when the node reports back the execution result.
func (s *JobService) UpdateResult(id string, status JobStatus, output, errMsg string) error {
	return s.repo.UpdateStatus(id, status, output, errMsg)
}
