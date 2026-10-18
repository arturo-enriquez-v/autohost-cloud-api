// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	scheduledtask "github.com/arturo/autohost-cloud-api/internal/domain/scheduled_task"
)

// Dispatcher sends a job execution request to a connected node.
type Dispatcher interface {
	DispatchJob(nodeID, jobID, commandName string, commandType nodecommand.CommandType) error
}

// Scheduler polls for due tasks and dispatches them via the node dispatcher.
type Scheduler struct {
	taskSvc    *scheduledtask.Service
	jobSvc     *job.Service
	dispatcher Dispatcher
}

func NewScheduler(taskSvc *scheduledtask.Service, jobSvc *job.Service, dispatcher Dispatcher) *Scheduler {
	return &Scheduler{
		taskSvc:    taskSvc,
		jobSvc:     jobSvc,
		dispatcher: dispatcher,
	}
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

func (s *Scheduler) dispatch(task *scheduledtask.ScheduledTask) {
	j, err := s.jobSvc.Dispatch(task.NodeID, task.CommandName, nodecommand.CommandTypeCustom)
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
