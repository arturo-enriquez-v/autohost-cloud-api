// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/jmoiron/sqlx"
)

// JobRepository implements jobs.JobRepository using PostgreSQL.
type JobRepository struct {
	db *sqlx.DB
}

func NewJobRepository(db *sqlx.DB) *JobRepository {
	return &JobRepository{db: db}
}

// Create inserts a new job record with status=pending.
func (r *JobRepository) Create(j *jobs.Job) (*jobs.Job, error) {
	var m JobModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO jobs (node_id, command_name, command_type, status, params)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, node_id, command_name, command_type, status, params, output, error, created_at, started_at, finished_at`,
		j.NodeID, j.CommandName, j.CommandType, j.Status, j.Params,
	).Scan(
		&m.ID, &m.NodeID, &m.CommandName, &m.CommandType, &m.Status, &m.Params,
		&m.Output, &m.Error, &m.CreatedAt, &m.StartedAt, &m.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	return modelToJob(m), nil
}

// FindByID returns a job by its UUID.
func (r *JobRepository) FindByID(id string) (*jobs.Job, error) {
	var m JobModel
	err := r.db.GetContext(context.Background(), &m,
		`SELECT id, node_id, command_name, command_type, status, params, output, error, created_at, started_at, finished_at
		 FROM jobs WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, jobs.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelToJob(m), nil
}

// FindByNodeID returns all jobs for a node ordered by creation time desc.
func (r *JobRepository) FindByNodeID(nodeID string) ([]*jobs.Job, error) {
	var models []JobModel
	err := r.db.SelectContext(context.Background(), &models,
		`SELECT id, node_id, command_name, command_type, status, params, output, error, created_at, started_at, finished_at
		 FROM jobs WHERE node_id = $1 ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]*jobs.Job, len(models))
	for i, m := range models {
		out[i] = modelToJob(m)
	}
	return out, nil
}

// FindPendingByNodeID returns pending jobs of a specific command type for a node.
func (r *JobRepository) FindPendingByNodeID(nodeID string, commandType jobs.CommandType) ([]*jobs.Job, error) {
	var models []JobModel
	err := r.db.SelectContext(context.Background(), &models,
		`SELECT id, node_id, command_name, command_type, status, params, output, error, created_at, started_at, finished_at
		 FROM jobs WHERE node_id = $1 AND command_type = $2 AND status = 'pending'
		 ORDER BY created_at ASC`, nodeID, string(commandType))
	if err != nil {
		return nil, err
	}
	out := make([]*jobs.Job, len(models))
	for i, m := range models {
		out[i] = modelToJob(m)
	}
	return out, nil
}

// UpdateStatus updates a job's status, output and error. It also sets
// started_at / finished_at timestamps automatically from the status transition.
func (r *JobRepository) UpdateStatus(id string, status jobs.JobStatus, output, errMsg string) error {
	now := time.Now()
	var query string
	var args []interface{}

	switch status {
	case jobs.StatusRunning:
		query = `UPDATE jobs
		         SET status = $1, output = $2, error = $3, started_at = $4
		         WHERE id = $5`
		args = []interface{}{status, output, errMsg, now, id}
	case jobs.StatusCompleted, jobs.StatusFailed:
		query = `UPDATE jobs
		         SET status = $1, output = $2, error = $3, finished_at = $4
		         WHERE id = $5`
		args = []interface{}{status, output, errMsg, now, id}
	default:
		query = `UPDATE jobs SET status = $1, output = $2, error = $3 WHERE id = $4`
		args = []interface{}{status, output, errMsg, id}
	}

	res, err := r.db.ExecContext(context.Background(), query, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return jobs.ErrJobNotFound
	}
	return nil
}

func modelToJob(m JobModel) *jobs.Job {
	return &jobs.Job{
		ID:          m.ID,
		NodeID:      m.NodeID,
		CommandName: m.CommandName,
		CommandType: jobs.CommandType(m.CommandType),
		Status:      jobs.JobStatus(m.Status),
		Params:      m.Params,
		Output:      m.Output.String,
		Error:       m.Error.String,
		CreatedAt:   m.CreatedAt,
		StartedAt:   m.StartedAt,
		FinishedAt:  m.FinishedAt,
	}
}
