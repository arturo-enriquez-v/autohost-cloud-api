package job

import (
	"errors"
	"time"

	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
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
	ID          string                  `db:"id"`
	NodeID      string                  `db:"node_id"`
	CommandName string                  `db:"command_name"`
	CommandType nodecommand.CommandType `db:"command_type"`
	Status      JobStatus               `db:"status"`
	Output      string                  `db:"output"`
	Error       string                  `db:"error"`
	CreatedAt   time.Time               `db:"created_at"`
	StartedAt   *time.Time              `db:"started_at"`
	FinishedAt  *time.Time              `db:"finished_at"`
}

// Repository defines the persistence contract for jobs.
type Repository interface {
	Create(j *Job) (*Job, error)
	FindByID(id string) (*Job, error)
	FindByNodeID(nodeID string) ([]*Job, error)
	UpdateStatus(id string, status JobStatus, output, errMsg string) error
}
