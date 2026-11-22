// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// fakeJobRepo holds jobs by ID and records status updates.
type fakeJobRepo struct {
	jobs    map[string]*jobs.Job
	updated map[string]jobs.JobStatus
}

func (r *fakeJobRepo) Create(j *jobs.Job) (*jobs.Job, error) { return j, nil }
func (r *fakeJobRepo) FindByID(id string) (*jobs.Job, error) {
	if j, ok := r.jobs[id]; ok {
		return j, nil
	}
	return nil, jobs.ErrJobNotFound
}
func (r *fakeJobRepo) FindByNodeID(string) ([]*jobs.Job, error) { return nil, nil }
func (r *fakeJobRepo) FindPendingByNodeID(string, jobs.CommandType) ([]*jobs.Job, error) {
	return nil, nil
}
func (r *fakeJobRepo) UpdateStatus(id string, status jobs.JobStatus, _, _ string) error {
	r.updated[id] = status
	return nil
}

// fakeVMRepo holds VMs by ID.
type fakeVMRepo struct{ vms map[string]*infra.VM }

func (r *fakeVMRepo) GetByID(_ context.Context, id string) (*infra.VM, error) {
	if v, ok := r.vms[id]; ok {
		return v, nil
	}
	return nil, infra.ErrNotFound
}
func (r *fakeVMRepo) GetByHostAndName(_ context.Context, host, name string) (*infra.VM, error) {
	for _, v := range r.vms {
		if v.HostNodeID == host && v.Name == name {
			return v, nil
		}
	}
	return nil, infra.ErrNotFound
}
func (r *fakeVMRepo) Update(_ context.Context, v *infra.VM) (*infra.VM, error) {
	r.vms[v.ID] = v
	return v, nil
}
func (r *fakeVMRepo) Create(context.Context, *infra.VM) (*infra.VM, error)     { return nil, nil }
func (r *fakeVMRepo) ListByHost(context.Context, string) ([]*infra.VM, error)  { return nil, nil }
func (r *fakeVMRepo) ListByOwner(context.Context, string) ([]*infra.VM, error) { return nil, nil }
func (r *fakeVMRepo) Delete(context.Context, string) error                     { return nil }
func (r *fakeVMRepo) LinkNodeByHostname(context.Context, string, string, string, string) error {
	return nil
}

func newJobResultServer(js ...*jobs.Job) (*NodeAgentServer, *fakeJobRepo, *fakeVMRepo) {
	jr := &fakeJobRepo{jobs: map[string]*jobs.Job{}, updated: map[string]jobs.JobStatus{}}
	for _, j := range js {
		jr.jobs[j.ID] = j
	}
	vr := &fakeVMRepo{vms: map[string]*infra.VM{}}
	return &NodeAgentServer{jobSvc: jobs.NewJobService(jr), vmRepo: vr}, jr, vr
}

func result(jobID string, status pb.JobStatus, errMsg string) *pb.JobResultPayload {
	return &pb.JobResultPayload{JobId: jobID, Status: status, Error: errMsg}
}

// A node may only report results for jobs dispatched to itself.
func TestHandleJobResult_RejectsOtherNodesJobs(t *testing.T) {
	s, jr, _ := newJobResultServer(&jobs.Job{ID: "job-1", NodeID: "node-a", CommandName: "container.list"})

	s.handleJobResult("node-b", result("job-1", pb.JobStatus_JOB_STATUS_COMPLETED, ""))
	if _, ok := jr.updated["job-1"]; ok {
		t.Fatal("node-b updated a job dispatched to node-a")
	}
	s.handleJobResult("node-b", result("job-missing", pb.JobStatus_JOB_STATUS_COMPLETED, ""))
	if len(jr.updated) != 0 {
		t.Fatal("an unknown job was updated")
	}

	s.handleJobResult("node-a", result("job-1", pb.JobStatus_JOB_STATUS_COMPLETED, ""))
	if jr.updated["job-1"] != jobs.StatusCompleted {
		t.Errorf("job status = %q, want completed", jr.updated["job-1"])
	}
}

func TestHandleJobResult_SyncsVMs(t *testing.T) {
	tests := []struct {
		name       string
		job        *jobs.Job
		status     pb.JobStatus
		errMsg     string
		vm         infra.VM
		wantStatus string
		wantErrMsg string
	}{
		{
			name:       "vm.create failed",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "vm.create", Params: `{"vm_id":"vm-1"}`},
			status:     pb.JobStatus_JOB_STATUS_FAILED,
			errMsg:     "no space left",
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusCreating)},
			wantStatus: string(infra.StatusError),
			wantErrMsg: "no space left",
		},
		{
			name:       "vm.create succeeded keeps status and clears error",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "vm.create", Params: `{"vm_id":"vm-1"}`},
			status:     pb.JobStatus_JOB_STATUS_COMPLETED,
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusCreating), ErrorMessage: "old"},
			wantStatus: string(infra.StatusCreating),
		},
		{
			name:       "vm.start succeeded",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "vm.start.web"},
			status:     pb.JobStatus_JOB_STATUS_COMPLETED,
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusStopped)},
			wantStatus: string(infra.StatusRunning),
		},
		{
			name:       "vm.start failed",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "vm.start.web"},
			status:     pb.JobStatus_JOB_STATUS_FAILED,
			errMsg:     "domain not found",
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusRunning)},
			wantStatus: string(infra.StatusStopped),
			wantErrMsg: "domain not found",
		},
		{
			name:       "vm.stop succeeded",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "vm.stop.web"},
			status:     pb.JobStatus_JOB_STATUS_COMPLETED,
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusRunning)},
			wantStatus: string(infra.StatusStopped),
		},
		{
			name:       "other jobs leave VMs alone",
			job:        &jobs.Job{ID: "j", NodeID: "host", CommandName: "container.restart.web"},
			status:     pb.JobStatus_JOB_STATUS_FAILED,
			errMsg:     "boom",
			vm:         infra.VM{ID: "vm-1", HostNodeID: "host", Name: "web", Status: string(infra.StatusRunning)},
			wantStatus: string(infra.StatusRunning),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, vr := newJobResultServer(tt.job)
			vm := tt.vm
			vr.vms[vm.ID] = &vm

			s.handleJobResult("host", result("j", tt.status, tt.errMsg))

			got := vr.vms["vm-1"]
			if got.Status != tt.wantStatus || got.ErrorMessage != tt.wantErrMsg {
				t.Errorf("vm = {status %q, error %q}, want {status %q, error %q}", got.Status, got.ErrorMessage, tt.wantStatus, tt.wantErrMsg)
			}
		})
	}
}
