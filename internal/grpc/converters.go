// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func pbCommandType(t pb.CommandType) jobs.CommandType {
	switch t {
	case pb.CommandType_COMMAND_TYPE_CUSTOM:
		return jobs.CommandTypeCustom
	case pb.CommandType_COMMAND_TYPE_INSTALL_APP:
		return jobs.CommandTypeInstallApp
	case pb.CommandType_COMMAND_TYPE_SWARM:
		return jobs.CommandTypeSwarm
	default:
		return jobs.CommandTypeDefault
	}
}

func pbJobStatus(s pb.JobStatus) string {
	switch s {
	case pb.JobStatus_JOB_STATUS_RUNNING:
		return string(jobs.StatusRunning)
	case pb.JobStatus_JOB_STATUS_FAILED:
		return string(jobs.StatusFailed)
	default:
		return string(jobs.StatusCompleted)
	}
}
