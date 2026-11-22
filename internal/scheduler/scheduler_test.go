// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
)

type taskRepo struct{ task *jobs.ScheduledTask }

func (r *taskRepo) Create(t *jobs.ScheduledTask) (*jobs.ScheduledTask, error) { return t, nil }
func (r *taskRepo) List() ([]*jobs.ScheduledTask, error)                      { return []*jobs.ScheduledTask{r.task}, nil }
func (r *taskRepo) ListByUserID(string) ([]*jobs.ScheduledTask, error)        { return nil, nil }
func (r *taskRepo) ListEnabled() ([]*jobs.ScheduledTask, error) {
	if !r.task.Enabled {
		return nil, nil
	}
	return []*jobs.ScheduledTask{r.task}, nil
}
func (r *taskRepo) FindByID(string) (*jobs.ScheduledTask, error) { return r.task, nil }
func (r *taskRepo) Update(t *jobs.ScheduledTask) (*jobs.ScheduledTask, error) {
	r.task = t
	return t, nil
}
func (r *taskRepo) Delete(string) error                                 { return nil }
func (r *taskRepo) MarkRan(string, time.Time, time.Time, *string) error { return nil }

type jobRepo struct{ created int }

func (r *jobRepo) Create(j *jobs.Job) (*jobs.Job, error) {
	r.created++
	j.ID = "job-1"
	return j, nil
}
func (r *jobRepo) FindByID(string) (*jobs.Job, error)       { return nil, jobs.ErrJobNotFound }
func (r *jobRepo) FindByNodeID(string) ([]*jobs.Job, error) { return nil, nil }
func (r *jobRepo) FindPendingByNodeID(string, jobs.CommandType) ([]*jobs.Job, error) {
	return nil, nil
}
func (r *jobRepo) UpdateStatus(string, jobs.JobStatus, string, string) error { return nil }

type noopDispatcher struct{}

func (noopDispatcher) DispatchJob(string, string, string, jobs.CommandType) error { return nil }

type fixedPerms struct {
	ok  bool
	err error
}

func (p fixedPerms) HasNodePermission(context.Context, string, string, string) (bool, error) {
	return p.ok, p.err
}

// Scheduled tasks run on behalf of their owner, so they stop when the owner loses access to the node.
func TestScheduler_RechecksOwnerPermission(t *testing.T) {
	interval := 3600
	for _, tc := range []struct {
		name        string
		perms       fixedPerms
		wantJobs    int
		wantEnabled bool
	}{
		{"owner still allowed", fixedPerms{ok: true}, 1, true},
		{"owner lost access", fixedPerms{ok: false}, 0, false},
		{"permission check failed", fixedPerms{err: errors.New("db down")}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &taskRepo{task: &jobs.ScheduledTask{
				ID: "task-1", UserID: "user-1", NodeID: "node-1", CommandID: "cmd-1", CommandName: "backup-db",
				ScheduleType: jobs.ScheduleInterval, IntervalSeconds: &interval, Enabled: true,
			}}
			jr := &jobRepo{}
			s := &Scheduler{
				taskSvc:    jobs.NewScheduledTaskService(tasks),
				jobSvc:     jobs.NewJobService(jr),
				dispatcher: noopDispatcher{},
				perms:      tc.perms,
			}
			s.runDue()
			if jr.created != tc.wantJobs {
				t.Errorf("jobs created = %d, want %d", jr.created, tc.wantJobs)
			}
			if tasks.task.Enabled != tc.wantEnabled {
				t.Errorf("task enabled = %v, want %v", tasks.task.Enabled, tc.wantEnabled)
			}
		})
	}
}
