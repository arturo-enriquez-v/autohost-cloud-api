// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package jobs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrTaskNotFound    = errors.New("scheduled task not found")
	ErrNotFound        = ErrTaskNotFound
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

// ScheduledTaskRepository defines the persistence contract for scheduled tasks.
type ScheduledTaskRepository interface {
	Create(task *ScheduledTask) (*ScheduledTask, error)
	List() ([]*ScheduledTask, error)
	ListByUserID(userID string) ([]*ScheduledTask, error)
	ListEnabled() ([]*ScheduledTask, error)
	FindByID(id string) (*ScheduledTask, error)
	Update(task *ScheduledTask) (*ScheduledTask, error)
	Delete(id string) error
	MarkRan(id string, lastRunAt, nextRunAt time.Time, jobID *string) error
}

// ScheduledTaskService wraps the ScheduledTaskRepository with business logic.
type ScheduledTaskService struct {
	repo ScheduledTaskRepository
}

func NewScheduledTaskService(repo ScheduledTaskRepository) *ScheduledTaskService {
	return &ScheduledTaskService{repo: repo}
}

func (s *ScheduledTaskService) Create(task *ScheduledTask) (*ScheduledTask, error) {
	if task.NodeID == "" || task.CommandID == "" || task.CommandName == "" {
		return nil, ErrInvalidTaskData
	}
	if task.ScheduleType == "" {
		task.ScheduleType = ScheduleInterval
	}
	next, err := computeNextRun(task)
	if err != nil {
		return nil, ErrInvalidTaskData
	}
	task.NextRunAt = next
	return s.repo.Create(task)
}

func (s *ScheduledTaskService) List() ([]*ScheduledTask, error) {
	return s.repo.List()
}

func (s *ScheduledTaskService) ListByUserID(userID string) ([]*ScheduledTask, error) {
	return s.repo.ListByUserID(userID)
}

func (s *ScheduledTaskService) ListEnabled() ([]*ScheduledTask, error) {
	return s.repo.ListEnabled()
}

func (s *ScheduledTaskService) FindByID(id string) (*ScheduledTask, error) {
	return s.repo.FindByID(id)
}

func (s *ScheduledTaskService) Update(task *ScheduledTask) (*ScheduledTask, error) {
	next, err := computeNextRun(task)
	if err != nil {
		return nil, ErrInvalidTaskData
	}
	task.NextRunAt = next
	return s.repo.Update(task)
}

func (s *ScheduledTaskService) Delete(id string) error {
	return s.repo.Delete(id)
}

// MarkRan advances next_run_at and records the last job ID.
func (s *ScheduledTaskService) MarkRan(id string, jobID *string) error {
	task, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	next, err := computeNextRunFrom(task, now)
	if err != nil {
		return fmt.Errorf("computing next run for task %s: %w", id, err)
	}
	return s.repo.MarkRan(id, now, next, jobID)
}

// computeNextRun calculates next_run_at from now.
func computeNextRun(task *ScheduledTask) (time.Time, error) {
	return computeNextRunFrom(task, time.Now().UTC())
}

func computeNextRunFrom(task *ScheduledTask, from time.Time) (time.Time, error) {
	switch task.ScheduleType {
	case ScheduleInterval:
		if task.IntervalSeconds == nil || *task.IntervalSeconds <= 0 {
			return time.Time{}, fmt.Errorf("interval_seconds must be > 0")
		}
		return from.Add(time.Duration(*task.IntervalSeconds) * time.Second), nil
	case ScheduleDaily:
		if task.RunAtTime == nil {
			return time.Time{}, fmt.Errorf("run_at_time is required for daily schedule")
		}
		return nextDailyRun(*task.RunAtTime, from)
	default:
		return time.Time{}, fmt.Errorf("unknown schedule type: %s", task.ScheduleType)
	}
}

// nextDailyRun returns the next UTC occurrence of runAtTime ("HH:MM") after `from`.
func nextDailyRun(runAtTime string, from time.Time) (time.Time, error) {
	parts := strings.SplitN(runAtTime, ":", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid run_at_time %q: expected HH:MM", runAtTime)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("invalid hour in run_at_time %q", runAtTime)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("invalid minute in run_at_time %q", runAtTime)
	}
	candidate := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, time.UTC)
	if !candidate.After(from) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate, nil
}
