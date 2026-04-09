package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/jmoiron/sqlx"
)

// JobRepository implements job.Repository using PostgreSQL.
type JobRepository struct {
	db *sqlx.DB
}

func NewJobRepository(db *sqlx.DB) *JobRepository {
	return &JobRepository{db: db}
}

// Create inserts a new job record with status=pending.
func (r *JobRepository) Create(j *job.Job) (*job.Job, error) {
	var m JobModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO jobs (node_id, command_name, command_type, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, node_id, command_name, command_type, status, output, error, created_at, started_at, finished_at`,
		j.NodeID, j.CommandName, j.CommandType, j.Status,
	).Scan(
		&m.ID, &m.NodeID, &m.CommandName, &m.CommandType, &m.Status,
		&m.Output, &m.Error, &m.CreatedAt, &m.StartedAt, &m.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	return modelToJob(m), nil
}

// FindByID returns a job by its UUID.
func (r *JobRepository) FindByID(id string) (*job.Job, error) {
	var m JobModel
	err := r.db.GetContext(context.Background(), &m,
		`SELECT id, node_id, command_name, command_type, status, output, error, created_at, started_at, finished_at
		 FROM jobs WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, job.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelToJob(m), nil
}

// FindByNodeID returns all jobs for a node ordered by creation time desc.
func (r *JobRepository) FindByNodeID(nodeID string) ([]*job.Job, error) {
	var models []JobModel
	err := r.db.SelectContext(context.Background(), &models,
		`SELECT id, node_id, command_name, command_type, status, output, error, created_at, started_at, finished_at
		 FROM jobs WHERE node_id = $1 ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]*job.Job, len(models))
	for i, m := range models {
		out[i] = modelToJob(m)
	}
	return out, nil
}

// UpdateStatus updates a job's status, output and error. It also sets
// started_at / finished_at timestamps automatically from the status transition.
func (r *JobRepository) UpdateStatus(id string, status job.JobStatus, output, errMsg string) error {
	now := time.Now()
	var query string
	var args []interface{}

	switch status {
	case job.StatusRunning:
		query = `UPDATE jobs
		         SET status = $1, output = $2, error = $3, started_at = $4
		         WHERE id = $5`
		args = []interface{}{status, output, errMsg, now, id}
	case job.StatusCompleted, job.StatusFailed:
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
		return job.ErrJobNotFound
	}
	return nil
}

func modelToJob(m JobModel) *job.Job {
	return &job.Job{
		ID:          m.ID,
		NodeID:      m.NodeID,
		CommandName: m.CommandName,
		CommandType: nodecommand.CommandType(m.CommandType),
		Status:      job.JobStatus(m.Status),
		Output:      m.Output.String,
		Error:       m.Error.String,
		CreatedAt:   m.CreatedAt,
		StartedAt:   m.StartedAt,
		FinishedAt:  m.FinishedAt,
	}
}
