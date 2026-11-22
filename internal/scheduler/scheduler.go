// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
)

// Dispatcher sends a job execution request to a connected node.
type Dispatcher interface {
	DispatchJob(nodeID, jobID, commandName string, commandType jobs.CommandType) error
}

// PermissionChecker reports whether a user holds a permission on a node (implemented by identity.RBACService).
type PermissionChecker interface {
	HasNodePermission(ctx context.Context, userID, nodeID, perm string) (bool, error)
}

// Scheduler polls for due tasks and dispatches them via the node dispatcher.
type Scheduler struct {
	taskSvc    *jobs.ScheduledTaskService
	jobSvc     *jobs.JobService
	dispatcher Dispatcher
	perms      PermissionChecker
}

func NewScheduler(taskSvc *jobs.ScheduledTaskService, jobSvc *jobs.JobService, dispatcher Dispatcher, rbac *identity.RBACService) *Scheduler {
	s := &Scheduler{
		taskSvc:    taskSvc,
		jobSvc:     jobSvc,
		dispatcher: dispatcher,
	}
	if rbac != nil {
		s.perms = rbac
	}
	return s
}

// Start runs the scheduler loop until the context is cancelled.
// It checks for due tasks every minute and dispatches them.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	log.Println("[scheduler] started, checking every 1 minute for due tasks")

	for {
		select {
		case <-ctx.Done():
			log.Println("[scheduler] shutting down")
			return
		case <-ticker.C:
			s.runDue()
		}
	}
}

func (s *Scheduler) runDue() {
	tasks, err := s.taskSvc.ListEnabled()
	if err != nil {
		log.Printf("[scheduler] error fetching due tasks: %v", err)
		return
	}
	for _, task := range tasks {
		s.dispatch(task)
	}
}

func (s *Scheduler) dispatch(task *jobs.ScheduledTask) {
	// Tasks created before the command allow-list existed may reference commands that are no longer
	// dispatchable without validated parameters. Skip them instead of running them.
	perm, err := jobs.ResolveUserJobPermission(task.CommandName, "")
	if err != nil {
		log.Printf("[scheduler] skipping task %s: command %q not allowed: %v", task.ID, task.CommandName, err)
		if err := s.taskSvc.MarkRan(task.ID, nil); err != nil {
			log.Printf("[scheduler] error advancing skipped task %s: %v", task.ID, err)
		}
		return
	}
	// The task runs on behalf of its owner: re-check their permission on every run, so revoking a user's
	// access to the node also stops their scheduled tasks. A failed check only skips this run.
	if s.perms != nil {
		ok, err := s.perms.HasNodePermission(context.Background(), task.UserID, task.NodeID, perm)
		if err != nil {
			log.Printf("[scheduler] skipping task %s: permission check failed: %v", task.ID, err)
			return
		}
		if !ok {
			log.Printf("[scheduler] disabling task %s: user %s no longer has %s on node %s", task.ID, task.UserID, perm, task.NodeID)
			task.Enabled = false
			if _, err := s.taskSvc.Update(task); err != nil {
				log.Printf("[scheduler] error disabling task %s: %v", task.ID, err)
			}
			return
		}
	}
	j, err := s.jobSvc.Dispatch(task.NodeID, task.CommandName, jobs.CommandTypeCustom)
	if err != nil {
		log.Printf("[scheduler] error creating job for task %s: %v", task.ID, err)
		return
	}

	if err := s.dispatcher.DispatchJob(task.NodeID, j.ID, j.CommandName, j.CommandType); err != nil {
		log.Printf("[scheduler] node %s not connected for task %s, job %s stays pending: %v",
			task.NodeID, task.ID, j.ID, err)
	} else {
		log.Printf("[scheduler] dispatched task %s (command: %s) to node %s",
			task.ID, task.CommandName, task.NodeID)
	}

	// Always advance next_run_at so the task fires again at the right interval.
	if err := s.taskSvc.MarkRan(task.ID, &j.ID); err != nil {
		log.Printf("[scheduler] error marking task %s as ran: %v", task.ID, err)
	}
}
