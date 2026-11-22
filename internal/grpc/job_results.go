// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// handleJobResult records a job result reported by a node and syncs the records that depend on it
// (backups, marketplace apps, VMs).
func (s *NodeAgentServer) handleJobResult(nodeID string, r *pb.JobResultPayload) {
	// A node may only report results for jobs dispatched to itself.
	job, err := s.jobSvc.GetByID(r.GetJobId())
	if err != nil || job == nil || job.NodeID != nodeID {
		log.Printf("[gRPC] rejecting result for job %s from node %s: job not found or owned by another node", r.GetJobId(), nodeID)
		return
	}

	jobStatus := jobs.JobStatus(pbJobStatus(r.GetStatus()))
	if err := s.jobSvc.UpdateResult(r.GetJobId(), jobStatus, r.GetOutput(), r.GetError()); err != nil {
		log.Printf("[gRPC] update job %s: %v", r.GetJobId(), err)
	} else {
		log.Printf("[gRPC] job %s -> %s (node %s)", r.GetJobId(), r.GetStatus(), nodeID)
	}

	// Broadcast job completion to Redis for decoupled consumers.
	result := map[string]any{
		"job_id":  r.GetJobId(),
		"node_id": nodeID,
		"status":  string(jobStatus),
		"output":  r.GetOutput(),
		"error":   r.GetError(),
	}
	s.publish(fmt.Sprintf("nodes:%s:jobs:result", nodeID), result)
	s.publish(fmt.Sprintf("jobs:%s:result", r.GetJobId()), result)

	succeeded := jobStatus == jobs.StatusCompleted
	s.syncBackupRecord(nodeID, r, succeeded)
	s.syncMarketplaceApp(r.GetJobId(), succeeded)
	s.syncVM(nodeID, job, r.GetError(), succeeded)
}

// syncBackupRecord updates the backup record of a backup or restore job, if the job is one.
func (s *NodeAgentServer) syncBackupRecord(nodeID string, r *pb.JobResultPayload, succeeded bool) {
	if s.backupSvc == nil {
		return
	}
	var out struct {
		S3Key         string `json:"s3_key"`
		LocalPath     string `json:"local_path"`
		ParentPath    string `json:"parent_path"`
		FileSizeBytes int64  `json:"file_size_bytes"`
	}
	_ = json.Unmarshal([]byte(r.GetOutput()), &out)
	// parent_path refers to the API server's own disk: never trust an agent-supplied value
	// outside the backups directory (it is later served and deleted by the API).
	if out.ParentPath != "" && !platform.IsWithinDir(platform.BackupsDir(), out.ParentPath) {
		log.Printf("[gRPC] ignoring parent_path %q reported by node %s for job %s: outside backups dir", out.ParentPath, nodeID, r.GetJobId())
		out.ParentPath = ""
	}

	var errMsg *string
	if r.GetError() != "" {
		e := r.GetError()
		errMsg = &e
	}
	status := "completed"
	if !succeeded {
		status = "failed"
	}
	if err := s.backupSvc.UpdateRecordByJobID(r.GetJobId(), status, out.S3Key, out.LocalPath, out.ParentPath, out.FileSizeBytes, errMsg); err != nil {
		log.Printf("[gRPC] update backup record for job %s: %v", r.GetJobId(), err)
	} else {
		log.Printf("[gRPC] backup record updated for job %s -> %s", r.GetJobId(), status)
	}
}

// syncMarketplaceApp updates the status of the marketplace app installed by the job, if any.
func (s *NodeAgentServer) syncMarketplaceApp(jobID string, succeeded bool) {
	if s.marketplaceRepo == nil {
		return
	}
	mApp, err := s.marketplaceRepo.FindByJobID(jobID)
	if err != nil {
		return
	}
	appStatus := apps.StatusFailed
	if succeeded {
		appStatus = apps.StatusRunning
	}
	if err := s.marketplaceRepo.UpdateStatus(mApp.ID, appStatus); err != nil {
		log.Printf("[gRPC] update marketplace app %s status: %v", mApp.ID, err)
	}
}

// syncVM updates the VM affected by a vm.* job: vm.create jobs carry the VM ID in their params; lifecycle
// jobs are named vm.<action>.<vm name> and target a VM on the reporting host.
func (s *NodeAgentServer) syncVM(nodeID string, job *jobs.Job, jobErr string, succeeded bool) {
	if s.vmRepo == nil || !strings.HasPrefix(job.CommandName, "vm.") {
		return
	}
	ctx := context.Background()

	if job.CommandName == "vm.create" {
		var params struct {
			VMID string `json:"vm_id"`
		}
		if err := json.Unmarshal([]byte(job.Params), &params); err != nil || params.VMID == "" {
			return
		}
		vm, err := s.vmRepo.GetByID(ctx, params.VMID)
		if err != nil || vm == nil {
			return
		}
		if !succeeded {
			vm.Status = string(infra.StatusError)
			vm.ErrorMessage = jobErr
			if _, err := s.vmRepo.Update(ctx, vm); err != nil {
				log.Printf("[gRPC] update vm %s status to error: %v", vm.ID, err)
			} else {
				log.Printf("[gRPC] updated vm %s (%s) status to error: %s", vm.ID, vm.Name, jobErr)
			}
			return
		}
		vm.ErrorMessage = ""
		_, _ = s.vmRepo.Update(ctx, vm)
		return
	}

	parts := strings.Split(job.CommandName, ".")
	if len(parts) < 3 {
		return
	}
	action, vmName := parts[1], parts[2]
	vm, err := s.vmRepo.GetByHostAndName(ctx, nodeID, vmName)
	if err != nil || vm == nil {
		return
	}
	if !succeeded {
		vm.ErrorMessage = jobErr
		if action == "start" {
			vm.Status = string(infra.StatusStopped)
		}
	} else {
		vm.ErrorMessage = ""
		switch action {
		case "start":
			vm.Status = string(infra.StatusRunning)
		case "stop":
			vm.Status = string(infra.StatusStopped)
		}
	}
	_, _ = s.vmRepo.Update(ctx, vm)
}
