// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func pbCommandType(t pb.CommandType) nodecommand.CommandType {
	switch t {
	case pb.CommandType_COMMAND_TYPE_CUSTOM:
		return nodecommand.CommandTypeCustom
	case pb.CommandType_COMMAND_TYPE_INSTALL_APP:
		return nodecommand.CommandTypeInstallApp
	case pb.CommandType_COMMAND_TYPE_SWARM:
		return nodecommand.CommandTypeSwarm
	default:
		return nodecommand.CommandTypeDefault
	}
}

func pbJobStatus(s pb.JobStatus) string {
	switch s {
	case pb.JobStatus_JOB_STATUS_RUNNING:
		return string(job.StatusRunning)
	case pb.JobStatus_JOB_STATUS_FAILED:
		return string(job.StatusFailed)
	default:
		return string(job.StatusCompleted)
	}
}
