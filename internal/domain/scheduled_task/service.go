package scheduledtask

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Service wraps the Repository with business logic.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(task *ScheduledTask) (*ScheduledTask, error) {
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

func (s *Service) List() ([]*ScheduledTask, error) {
	return s.repo.List()
}

func (s *Service) ListByUserID(userID string) ([]*ScheduledTask, error) {
	return s.repo.ListByUserID(userID)
}

func (s *Service) ListEnabled() ([]*ScheduledTask, error) {
	return s.repo.ListEnabled()
}

func (s *Service) FindByID(id string) (*ScheduledTask, error) {
	return s.repo.FindByID(id)
}

func (s *Service) Update(task *ScheduledTask) (*ScheduledTask, error) {
	if task.ID == "" {
		return nil, ErrInvalidTaskData
	}
	next, err := computeNextRun(task)
	if err != nil {
		return nil, ErrInvalidTaskData
	}
	task.NextRunAt = next
	return s.repo.Update(task)
}

func (s *Service) Delete(id string) error {
	return s.repo.Delete(id)
}

// MarkRan advances next_run_at and records the last job ID.
func (s *Service) MarkRan(id string, jobID *string) error {
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
