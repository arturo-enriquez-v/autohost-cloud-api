// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduledtask

import (
	"errors"
	"time"
)

var (
	ErrNotFound        = errors.New("scheduled task not found")
	ErrInvalidTaskData = errors.New("invalid scheduled task data")
)

// ScheduleType controls how often the task is triggered.
// "interval": every N seconds; "daily": once a day at a fixed UTC time.
type ScheduleType string

const (
	ScheduleInterval ScheduleType = "interval"
	ScheduleDaily    ScheduleType = "daily"
)

// ScheduledTask represents a recurring execution of a custom command on a node.
type ScheduledTask struct {
	ID              string       `db:"id"               json:"id"`
	UserID          string       `db:"user_id"          json:"user_id"`
	NodeID          string       `db:"node_id"          json:"node_id"`
	CommandID       string       `db:"command_id"       json:"command_id"`
	CommandName     string       `db:"command_name"     json:"command_name"`
	ScheduleType    ScheduleType `db:"schedule_type"    json:"schedule_type"`
	IntervalSeconds *int         `db:"interval_seconds" json:"interval_seconds"`
	RunAtTime       *string      `db:"run_at_time"      json:"run_at_time"` // "HH:MM" UTC
	Enabled         bool         `db:"enabled"          json:"enabled"`
	LastRunAt       *time.Time   `db:"last_run_at"      json:"last_run_at"`
	NextRunAt       time.Time    `db:"next_run_at"      json:"next_run_at"`
	LastJobID       *string      `db:"last_job_id"      json:"last_job_id"`
	CreatedAt       time.Time    `db:"created_at"       json:"created_at"`
	UpdatedAt       time.Time    `db:"updated_at"       json:"updated_at"`

	// Populated via LEFT JOIN in List(); nil in other queries.
	LastJobStatus     *string    `db:"last_job_status"      json:"last_job_status,omitempty"`
	LastJobOutput     *string    `db:"last_job_output"      json:"last_job_output,omitempty"`
	LastJobError      *string    `db:"last_job_error"       json:"last_job_error,omitempty"`
	LastJobFinishedAt *time.Time `db:"last_job_finished_at" json:"last_job_finished_at,omitempty"`
}

// Repository defines the persistence contract for scheduled tasks.
type Repository interface {
	Create(task *ScheduledTask) (*ScheduledTask, error)
	List() ([]*ScheduledTask, error)
	ListByUserID(userID string) ([]*ScheduledTask, error)
	ListEnabled() ([]*ScheduledTask, error)
	FindByID(id string) (*ScheduledTask, error)
	Update(task *ScheduledTask) (*ScheduledTask, error)
	Delete(id string) error
	MarkRan(id string, lastRunAt, nextRunAt time.Time, jobID *string) error
}
